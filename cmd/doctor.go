package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/CyberSecAuto-Labs/aimd/internal/exclude"
	"github.com/CyberSecAuto-Labs/aimd/internal/link"
	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
	"github.com/CyberSecAuto-Labs/aimd/internal/store"
)

var doctorAll bool

// errDoctorProblems is the sentinel returned when one or more checks fail. It
// drives doctor's non-zero exit code (CI and scripts rely on it) without a
// noisy Go error dump — the per-check detail has already been printed to
// stdout, and the command silences usage/error output so only this one tidy
// line reaches stderr.
var errDoctorProblems = errors.New("doctor found problems — see the suggested fixes above")

var doctorCmd = &cobra.Command{
	Use:     "doctor",
	GroupID: "inspect",
	Short:   "Diagnose the health of the store and tracked files",
	Long: `Run a series of read-only health checks and report each one with a clear
✓ / ⚠ / ✗ status plus a suggested fix command for every failure.

Checks performed:
  • store remote reachable (git fetch --dry-run)
  • every tracked symlink resolves to its overlay
  • every tracked file has a .git/info/exclude entry
  • registry and store agree (each tracked file exists in the store)

By default doctor inspects the current project. Use --all to check every
tracked project. doctor never modifies anything; it exits non-zero when any
check fails so it can gate scripts and CI.`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return RunDoctor(storePath, machine, doctorAll, cmd.OutOrStdout())
	},
}

// checkStatus is the outcome of a single health check, ordered worst-last so
// the summary can pick the most severe.
type checkStatus int

const (
	checkOK checkStatus = iota
	checkWarn
	checkFail
)

func (s checkStatus) icon() string {
	switch s {
	case checkFail:
		return "✗"
	case checkWarn:
		return "⚠"
	default:
		return "✓"
	}
}

// RunDoctor is the testable core of the doctor command.
//
// storeDir is the resolved path to ~/.aimd/store.
// machineName identifies the current machine.
// all checks every tracked project instead of just the current one.
// out receives all user-facing output.
//
// It returns errDoctorProblems when any check fails (so the command exits
// non-zero), or a wrapped error for an operational failure that prevents the
// checks from running at all (store missing, registry unreadable).
func RunDoctor(storeDir, machineName string, all bool, out io.Writer) error {
	if err := verifyStore(storeDir); err != nil {
		return err
	}

	registryPath := filepath.Join(storeDir, ".aimd", "registry.json")
	reg, err := registry.LoadOrNew(registryPath)
	if err != nil {
		return fmt.Errorf("loading registry: %w", err)
	}

	linkMode, err := loadLinkMode()
	if err != nil {
		return fmt.Errorf("link mode: %w", err)
	}

	var failures, warnings int

	// Store-level checks (run once, regardless of scope).
	_, _ = fmt.Fprintln(out, "Store")
	f, w := reportCheck(out, doctorReachable(storeDir))
	failures += f
	warnings += w

	// Per-project file checks.
	targets, err := selectProjects(reg, machineName, all)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		_, _ = fmt.Fprintln(out)
		_, _ = fmt.Fprintf(out, "No projects tracked. Run `aimd track <file>` to get started.\n")
		return nil
	}

	for _, t := range targets {
		pf, pw := doctorProject(out, storeDir, reg.Projects[t.key], t.key, t.root, linkMode)
		failures += pf
		warnings += pw
	}

	_, _ = fmt.Fprintln(out)
	switch {
	case failures > 0:
		_, _ = fmt.Fprintf(out, "✗ %s found. Run the suggested fix commands above.\n", pluralize(failures, "problem"))
		return errDoctorProblems
	case warnings > 0:
		// Warnings (e.g. an unreachable remote while offline) are surfaced but do
		// not gate: the exit stays 0 while the summary honestly reflects them
		// rather than claiming an all-clear.
		_, _ = fmt.Fprintf(out, "⚠ %s found; no blocking problems.\n", pluralize(warnings, "warning"))
		return nil
	default:
		_, _ = fmt.Fprintf(out, "✓ All checks passed.\n")
		return nil
	}
}

// checkResult is one line of doctor output: a status, the thing being checked,
// and (for non-OK results) a fix suggestion.
type checkResult struct {
	status checkStatus
	label  string // what was checked, e.g. "remote reachable" or "CLAUDE.md"
	detail string // why it failed (empty for OK)
	fix    string // suggested command (empty for OK / warn without an action)
}

// reportCheck prints one result and returns its contribution to the failure and
// warning tallies: (1,0) for a hard failure, (0,1) for a warning, (0,0) for OK.
// Only failures gate the exit; warnings are surfaced so the summary can report
// them without claiming an all-clear.
func reportCheck(out io.Writer, r checkResult) (failures, warnings int) {
	line := fmt.Sprintf("  %s %s", r.status.icon(), r.label)
	if r.detail != "" {
		line += " — " + r.detail
	}
	if r.fix != "" {
		line += " → " + r.fix
	}
	_, _ = fmt.Fprintln(out, line)
	switch r.status {
	case checkFail:
		return 1, 0
	case checkWarn:
		return 0, 1
	default:
		return 0, 0
	}
}

// doctorReachable probes the store's origin remote. An unreachable remote is a
// warning, not a failure: the user may simply be offline, which doctor reports
// without failing the whole run.
func doctorReachable(storeDir string) checkResult {
	if err := store.FetchDryRun(storeDir); err != nil {
		return checkResult{
			status: checkWarn,
			label:  "remote reachable",
			detail: "could not reach origin (offline or misconfigured)",
			fix:    "check your network, then `git -C " + storeDir + " fetch`",
		}
	}
	return checkResult{status: checkOK, label: "remote reachable"}
}

// doctorProject runs the per-file checks for one project and returns its failure
// and warning tallies. When root is empty (the project is registered but not
// checked out on this machine) the symlink and exclude checks are skipped with a
// note, since there is no working tree to inspect; the store-consistency check
// still runs.
func doctorProject(out io.Writer, storeDir string, proj *registry.Project, key, root string, linkMode link.LinkMode) (failures, warnings int) {
	if proj == nil {
		return 0, 0
	}

	name := proj.DisplayName
	if name == "" {
		name = key
	}
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, name)

	// Use the project key, not the display name: names collide across remotes and
	// `aimd remove` rejects an ambiguous name, whereas the key is always unique.
	if len(proj.Tracked) == 0 {
		_, _ = fmt.Fprintf(out, "  (no tracked files — run `aimd remove %s` to forget this project)\n", key)
		return 0, 0
	}

	checkedOut := root != ""
	if !checkedOut {
		_, _ = fmt.Fprintf(out, "  (not checked out on this machine — symlink and exclude checks skipped)\n")
	}

	for _, tf := range proj.Tracked {
		for _, r := range doctorFileChecks(storeDir, key, root, tf.Path, linkMode, checkedOut) {
			f, w := reportCheck(out, r)
			failures += f
			warnings += w
		}
	}
	return failures, warnings
}

// doctorFileChecks returns the ordered check results for a single tracked file:
// store consistency first (the overlay must exist), then — only when the project
// is checked out locally — the symlink and exclude-entry checks. A file in good
// health produces a single OK line; each problem produces its own line.
func doctorFileChecks(storeDir, key, root, relPath string, linkMode link.LinkMode, checkedOut bool) []checkResult {
	overlaySrc := filepath.Join(storeDir, "repos", key, relPath)

	// Store consistency: the registry says this file is tracked, so the overlay
	// must exist in the store. A missing overlay is the most serious failure —
	// without it nothing else can be valid, so report it alone.
	if _, err := os.Stat(overlaySrc); err != nil {
		return []checkResult{{
			status: checkFail,
			label:  relPath,
			detail: "missing from store (registry and store disagree)",
			fix:    "re-run `aimd track " + relPath + "` from the project",
		}}
	}

	if !checkedOut {
		// No working tree to inspect; the overlay exists, which is all we can
		// verify from here.
		return []checkResult{{status: checkOK, label: relPath, detail: "in store"}}
	}

	var problems []checkResult

	projectDst := filepath.Join(root, relPath)
	if !symlinkResolves(projectDst, overlaySrc, linkMode) {
		problems = append(problems, checkResult{
			status: checkFail,
			label:  relPath,
			detail: "symlink missing or broken",
			fix:    "run `aimd restore` from the project",
		})
	}

	excludePath := filepath.Join(root, ".git", "info", "exclude")
	if present, err := exclude.HasEntry(excludePath, relPath); err != nil || !present {
		detail := "no .git/info/exclude entry (file would show in git status)"
		if err != nil {
			detail = "could not read .git/info/exclude"
		}
		problems = append(problems, checkResult{
			status: checkFail,
			label:  relPath,
			detail: detail,
			fix:    "run `aimd restore` from the project",
		})
	}

	if len(problems) == 0 {
		return []checkResult{{status: checkOK, label: relPath, detail: "symlink + exclude ok"}}
	}
	return problems
}

// symlinkResolves reports whether projectDst is a symlink that resolves to the
// expected overlay under the given link mode.
func symlinkResolves(projectDst, overlaySrc string, linkMode link.LinkMode) bool {
	fi, err := os.Lstat(projectDst)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return false
	}
	ok, verr := link.VerifyLink(projectDst, overlaySrc, linkMode)
	return verr == nil && ok
}

// pluralize renders a count with its noun, adding a trailing "s" for any count
// other than one (e.g. "1 problem", "2 problems", "1 warning").
func pluralize(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorAll, "all", false, "Check every tracked project, not just the current one")
	rootCmd.AddCommand(doctorCmd)
}

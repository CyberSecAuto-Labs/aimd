package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CyberSecAuto-Labs/aimd/internal/link"
	"github.com/CyberSecAuto-Labs/aimd/internal/lock"
	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
	"github.com/CyberSecAuto-Labs/aimd/internal/store"
)

var (
	resetYes    bool
	resetRemote bool
)

var resetCmd = &cobra.Command{
	Use:     "reset",
	GroupID: "tracking",
	Short:   "Restore every tracked file on this machine before uninstalling",
	Long: `Tear aimd down on this machine: restore every tracked file in every project
checked out here back to a real file (overlay → file, remove the symlink, strip
the .git/info/exclude entry), then forget those projects from the registry and
store.

Run this immediately before uninstalling aimd. Homebrew's --zap only removes
~/.aimd; it cannot reach into your project directories, so without reset a zap
would leave broken symlinks and stale exclude entries behind. reset is the
all-projects extension of "untrack .".

reset does NOT push: the store and registry live under ~/.aimd (which a
subsequent uninstall removes), so the remote and any other machines are left
untouched. Projects not checked out on this machine are skipped — reset them
from the machine where they live.

With --remote, reset also WIPES the shared remote store: after restoring this
machine's files it removes every project everywhere and replaces the remote's
history with a single empty commit (force-push). Every other machine then sees
an empty store on its next sync and must run its own aimd reset to clean up.
--remote requires typing the remote URL to confirm (--yes does not bypass it).

reset prints what it will do and requires --yes to skip the confirmation prompt.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return RunReset(storePath, machine, resetYes, dryRun, resetRemote, os.Stdin, cmd.OutOrStdout())
	},
}

// resetTarget is one in-scope project: checked out on the current machine and
// therefore restorable from here.
type resetTarget struct {
	key       string
	name      string
	localPath string
	proj      *registry.Project
}

// RunReset is the testable core of the reset command.
//
// storeDir is the resolved path to ~/.aimd/store. machineName identifies the
// current machine; only projects checked out here are restored. yes skips the
// confirmation prompt. dryRun lists what would happen without touching anything.
// remote also wipes the shared remote store and its history after the local
// teardown. in is used for reading confirmation input; out receives all
// user-facing output.
func RunReset(storeDir, machineName string, yes, dryRun, remote bool, in io.Reader, out io.Writer) error {
	if err := verifyStore(storeDir); err != nil {
		return err
	}

	// Guard against a concurrent watcher and hold the exclusive store lock for
	// the whole teardown (a dry-run mutates nothing, so it takes no lock).
	release, err := resetGuards(storeDir, dryRun)
	if err != nil {
		return err
	}
	defer release()

	linkMode, err := loadLinkMode()
	if err != nil {
		return fmt.Errorf("link mode: %w", err)
	}

	registryPath := filepath.Join(storeDir, ".aimd", "registry.json")
	reg, err := registry.LoadOrNew(registryPath)
	if err != nil {
		return fmt.Errorf("loading registry: %w", err)
	}

	// For --remote, resolve the remote URL up front so a missing remote fails
	// before any teardown.
	var remoteURL string
	if remote {
		remoteURL, err = store.RemoteURL(storeDir)
		if err != nil {
			return fmt.Errorf("--remote: %w", err)
		}
	}

	targets, skipped := planReset(reg, machineName)

	// Plain reset with nothing checked out here is a no-op. A --remote wipe still
	// has work to do (empty the remote), so it falls through.
	if len(targets) == 0 && !remote {
		_, _ = fmt.Fprintf(out, "No projects checked out on %q — nothing to reset.\n", machineName)
		for _, k := range skipped {
			_, _ = fmt.Fprintf(out, "  (skipped %s — not checked out here)\n", k)
		}
		return nil
	}

	totalFiles := printResetPlan(out, machineName, targets, skipped, remote, remoteURL)

	if dryRun {
		printResetDryRun(out, totalFiles, len(targets), remote)
		return nil
	}

	if !confirmReset(out, in, yes, remote, remoteURL) {
		if remote {
			_, _ = fmt.Fprintf(out, "Confirmation did not match — nothing was changed.\n")
		} else {
			_, _ = fmt.Fprintf(out, "Aborted.\n")
		}
		return nil
	}

	// Local teardown: restore this machine's files and forget those projects.
	firstErr := runLocalTeardown(storeDir, registryPath, machineName, reg, targets, linkMode, out)

	// Remote wipe — only after a fully successful local teardown, so the remote
	// is never destroyed while local state is incomplete and retryable.
	if remote {
		if firstErr != nil {
			_, _ = fmt.Fprintf(out, "⚠ local teardown did not complete — the remote was left untouched. Fix the errors above and re-run `aimd reset --remote`.\n")
			return firstErr
		}
		if werr := wipeRemote(storeDir, machineName, out); werr != nil {
			return werr
		}
	}

	return firstErr
}

// printResetPlan prints what the teardown will do and returns the total tracked
// file count across the local targets.
func printResetPlan(out io.Writer, machineName string, targets []resetTarget, skipped []string, remote bool, remoteURL string) int {
	if len(targets) > 0 {
		_, _ = fmt.Fprintf(out, "aimd reset will restore tracked files and forget these projects on %q:\n", machineName)
	}
	var totalFiles int
	for _, t := range targets {
		totalFiles += len(t.proj.Tracked)
		_, _ = fmt.Fprintf(out, "  %s (%d file(s)) → %s\n", t.name, len(t.proj.Tracked), t.localPath)
	}

	if remote {
		_, _ = fmt.Fprintf(out, "\nThis will WIPE the remote store and replace its history:\n")
		_, _ = fmt.Fprintf(out, "  remote: %s\n", remoteURL)
		_, _ = fmt.Fprintf(out, "  every project is removed everywhere; all store history is discarded.\n")
		if len(skipped) > 0 {
			_, _ = fmt.Fprintf(out, "  WARNING: these projects are not checked out here and cannot be restored from this machine —\n")
			_, _ = fmt.Fprintf(out, "           their other machines will be left with broken symlinks until each runs `aimd reset`:\n")
			for _, k := range skipped {
				_, _ = fmt.Fprintf(out, "             %s\n", k)
			}
		}
		return totalFiles
	}

	for _, k := range skipped {
		_, _ = fmt.Fprintf(out, "  (skipped %s — not checked out on this machine)\n", k)
	}
	_, _ = fmt.Fprintf(out, "(no push — the remote and other machines are untouched; finish with `brew uninstall --zap aimd`)\n")
	return totalFiles
}

// confirmRemoteWipe requires the user to type the remote URL exactly. --yes does
// not satisfy this gate, so a script cannot wipe the remote unattended; no input
// (EOF) reads as an empty string and fails the match.
func confirmRemoteWipe(out io.Writer, in io.Reader, remoteURL string) bool {
	_, _ = fmt.Fprintf(out, "Type the remote URL to confirm the wipe: ")
	var typed string
	_, _ = fmt.Fscan(in, &typed)
	return strings.TrimSpace(typed) == remoteURL
}

// wipeRemote rewrites the store to a single empty root commit and force-pushes
// it, replacing the remote history. On a push failure the local store is already
// wiped but the remote is retained for other machines, and the push is
// retryable.
func wipeRemote(storeDir, machineName string, out io.Writer) error {
	if err := store.ResetHistory(storeDir, machineName); err != nil {
		return fmt.Errorf("wiping local store: %w", err)
	}
	if err := store.ForcePush(storeDir); err != nil {
		_, _ = fmt.Fprintf(out, "⚠ local store wiped, but the remote force-push failed — the remote still holds the old data.\n  Re-run `aimd reset --remote` to retry the push.\n")
		return fmt.Errorf("force-pushing wiped store: %w", err)
	}
	_, _ = fmt.Fprintf(out, "✓ Remote store wiped and history replaced. Other machines must run `aimd reset` (then re-run `aimd init`) to recover.\n")
	return nil
}

// resetGuards refuses to run while a watcher is live (it would keep waking up to
// sync a store this command is dismantling) and takes the exclusive store lock
// for the duration. A dry-run mutates nothing, so it skips both and returns a
// no-op release.
func resetGuards(storeDir string, dryRun bool) (release func(), err error) {
	if dryRun {
		return func() {}, nil
	}
	running, werr := lock.WatchRunning(storeDir)
	if werr != nil {
		return nil, fmt.Errorf("checking for a running watcher: %w", werr)
	}
	if running {
		return nil, fmt.Errorf("an `aimd watch` process is running — stop it first, then re-run `aimd reset`")
	}
	r, lockErr := lockStoreExclusive(storeDir)
	if lockErr != nil {
		return nil, lockErr
	}
	return r, nil
}

// printResetDryRun prints the dry-run summary line.
func printResetDryRun(out io.Writer, totalFiles, projectCount int, remote bool) {
	if remote {
		_, _ = fmt.Fprintf(out, "dry-run: would restore %d file(s) across %d project(s) and WIPE the remote store\n", totalFiles, projectCount)
		return
	}
	_, _ = fmt.Fprintf(out, "dry-run: would restore %d file(s) across %d project(s)\n", totalFiles, projectCount)
}

// confirmReset gates the teardown. --remote demands typing the remote URL (no
// --yes shortcut); plain reset accepts --yes or a y/N prompt.
func confirmReset(out io.Writer, in io.Reader, yes, remote bool, remoteURL string) bool {
	if remote {
		return confirmRemoteWipe(out, in, remoteURL)
	}
	if yes {
		return true
	}
	confirmed, _ := confirmPrompt(out, in, "Continue?")
	return confirmed
}

// runLocalTeardown restores and forgets each target project, printing a summary.
// It returns the first error encountered; per-project failures do not abort the
// rest (each persists what it restored, cf. the partial-failure handling).
func runLocalTeardown(
	storeDir, registryPath, machineName string,
	reg *registry.Registry,
	targets []resetTarget,
	linkMode link.LinkMode,
	out io.Writer,
) error {
	var firstErr error
	var done int
	for _, t := range targets {
		if perr := resetProject(storeDir, registryPath, machineName, reg, t, linkMode, out); perr != nil {
			if firstErr == nil {
				firstErr = perr
			}
			continue
		}
		done++
	}
	if len(targets) > 0 {
		_, _ = fmt.Fprintf(out, "✓ Reset %d of %d project(s); tracked files restored to your working trees.\n", done, len(targets))
	}
	return firstErr
}

// planReset splits the registry into projects restorable on this machine
// (returned in stable key order) and the keys of projects checked out elsewhere.
func planReset(reg *registry.Registry, machineName string) (targets []resetTarget, skipped []string) {
	keys := make([]string, 0, len(reg.Projects))
	for key := range reg.Projects {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		proj := reg.Projects[key]
		if proj == nil {
			continue
		}
		m, ok := proj.Machines[machineName]
		if !ok || m.LocalPath == "" {
			skipped = append(skipped, key)
			continue
		}
		targets = append(targets, resetTarget{
			key:       key,
			name:      displayOr(proj.DisplayName, key),
			localPath: m.LocalPath,
			proj:      proj,
		})
	}
	return targets, skipped
}

// resetProject restores every tracked file of one project; on full success it
// also forgets the project from the registry and store, committing locally
// without pushing. restoreTrackedFile is not atomic — each success deletes an
// overlay, writes the real file, strips the exclude entry, and drops that file
// from the in-memory registry — so a partial failure must still persist the
// files that did restore: otherwise a retry would try to restore an
// already-restored file from a missing overlay, and a later project's
// registry. Save (the *Registry is shared) would persist these mutations
// inconsistently anyway.
func resetProject(
	storeDir, registryPath, machineName string,
	reg *registry.Registry,
	t resetTarget,
	linkMode link.LinkMode,
	out io.Writer,
) error {
	excludePath := filepath.Join(t.localPath, ".git", "info", "exclude")

	// Iterate a copy: restoreTrackedFile mutates proj.Tracked as it removes entries.
	tracked := make([]registry.TrackedFile, len(t.proj.Tracked))
	copy(tracked, t.proj.Tracked)

	var firstErr error
	var restored []string
	for _, tf := range tracked {
		abs := filepath.Join(t.localPath, tf.Path)
		overlayPath := filepath.Join(storeDir, "repos", t.key, tf.Path)
		if rerr := restoreTrackedFile(abs, t.localPath, overlayPath, excludePath, tf.Path, linkMode, t.proj, out); rerr != nil {
			_, _ = fmt.Fprintf(out, "error restoring %s in %s: %v\n", tf.Path, t.name, rerr)
			if firstErr == nil {
				firstErr = rerr
			}
			continue
		}
		restored = append(restored, tf.Path)
	}

	if firstErr != nil {
		// Persist the files that did restore so the on-disk registry/store match
		// reality, leaving retryable state — but keep the project entry for the
		// files that still need attention.
		if len(restored) > 0 {
			if serr := registry.Save(registryPath, reg); serr != nil {
				return errors.Join(firstErr, fmt.Errorf("saving registry after %s: %w", t.name, serr))
			}
			if serr := store.Commit(storeDir, t.key, t.localPath, "reset", machineName, restored); serr != nil && !isNothingToCommit(serr) {
				return errors.Join(firstErr, fmt.Errorf("committing restored files for %s: %w", t.name, serr))
			}
		}
		_, _ = fmt.Fprintf(out, "⚠ %s: restored %d of %d file(s); the rest failed — fix them and re-run `aimd reset`\n",
			t.name, len(restored), len(tracked))
		return firstErr
	}

	// All files restored — forget the project locally (no push).
	delete(reg.Projects, t.key)
	if serr := registry.Save(registryPath, reg); serr != nil {
		return fmt.Errorf("saving registry after %s: %w", t.name, serr)
	}
	if serr := store.RemoveProject(storeDir, t.key, t.name, machineName); serr != nil {
		return fmt.Errorf("forgetting %s from store: %w", t.name, serr)
	}
	return nil
}

func init() {
	resetCmd.Flags().BoolVar(&resetYes, "yes", false, "Skip confirmation prompt")
	resetCmd.Flags().BoolVar(&resetRemote, "remote", false, "Also wipe the shared remote store and its history (decommission everywhere)")
	rootCmd.AddCommand(resetCmd)
}

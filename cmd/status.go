package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/CyberSecAuto-Labs/aimd/internal/config"
	"github.com/CyberSecAuto-Labs/aimd/internal/link"
	"github.com/CyberSecAuto-Labs/aimd/internal/project"
	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
	"github.com/CyberSecAuto-Labs/aimd/internal/store"
)

var (
	statusAll         bool
	statusAllMachines bool
	statusFetch       bool
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the sync state of tracked AI context files",
	Long: `Inspect tracked files and the store without modifying anything.

By default status reports the current project only. Use --all to report every
tracked project. Use --all-machines to also list the other machines tracking
each reported project. status is read-only and offline by default: it compares
the store against the last-fetched origin/main without contacting the remote.
Pass --fetch to refresh the remote-tracking ref first.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return RunStatus(storePath, machine, statusAll, statusAllMachines, statusFetch, cmd.OutOrStdout())
	},
}

// fileState is one of the per-file states, ordered by precedence (highest first).
type fileState int

const (
	stateSynced fileState = iota
	stateModified
	stateBroken
	stateConflict
)

func (s fileState) icon() string {
	switch s {
	case stateConflict:
		return "⚡"
	case stateBroken:
		return "✗"
	case stateModified:
		return "✎"
	default:
		return "✓"
	}
}

// RunStatus is the testable core of the status command.
//
// storeDir is the resolved path to ~/.aimd/store.
// machineName identifies the current machine.
// all reports every tracked project instead of just the current one.
// allMachines lists the other machines tracking each reported project.
// fetch refreshes origin/main before comparing the store (otherwise offline).
// out receives all user-facing output.
func RunStatus(storeDir, machineName string, all, allMachines, fetch bool, out io.Writer) error {
	if err := verifyStore(storeDir); err != nil {
		return err
	}

	registryPath := filepath.Join(storeDir, ".aimd", "registry.json")
	reg, err := registry.LoadOrNew(registryPath)
	if err != nil {
		return fmt.Errorf("loading registry: %w", err)
	}

	// Resolve which projects to report and, per project, the destination root
	// used to locate the project-side symlinks.
	targets, err := selectProjects(reg, machineName, all)
	if err != nil {
		return err
	}

	if len(targets) == 0 {
		_, _ = fmt.Fprintf(out, "No projects tracked. Run `aimd track <file>` to get started.\n")
		return nil
	}

	linkMode, err := loadLinkMode()
	if err != nil {
		return fmt.Errorf("link mode: %w", err)
	}

	keys := make([]string, len(targets))
	for i, t := range targets {
		keys[i] = t.key
	}
	printHeader(out, storeDir, machineName, reg, keys, fetch)

	for _, t := range targets {
		printProject(out, storeDir, machineName, reg.Projects[t.key], t.key, t.root, linkMode, allMachines)
	}

	return nil
}

// projectTarget is a project to report plus the resolved destination root used
// to find its project-side symlinks.
type projectTarget struct {
	key  string
	root string
}

// selectProjects returns the projects to report. In --all mode it is every
// project in the registry, with each root taken from this machine's recorded
// LocalPath (empty when this machine has never checked it out — its file rows
// then read as broken, which is accurate). Otherwise it is the single project
// detected from the current directory, rooted at the real working tree.
func selectProjects(reg *registry.Registry, machineName string, all bool) ([]projectTarget, error) {
	if all {
		keys := make([]string, 0, len(reg.Projects))
		for k := range reg.Projects {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		targets := make([]projectTarget, 0, len(keys))
		for _, k := range keys {
			root := ""
			if m, ok := reg.Projects[k].Machines[machineName]; ok {
				root = m.LocalPath
			}
			targets = append(targets, projectTarget{key: k, root: root})
		}
		return targets, nil
	}

	proj, derr := project.Detect()
	if derr != nil {
		return nil, fmt.Errorf("not inside a tracked project — cd into one or pass --all (%w)", derr)
	}
	entry, ok := registry.GetProject(reg, proj.Key)
	if !ok || len(entry.Tracked) == 0 {
		// Not tracked / no files → treat as empty so the caller prints the
		// friendly empty-state line rather than a header with no rows.
		return nil, nil
	}
	return []projectTarget{{key: proj.Key, root: proj.Root}}, nil
}

// printHeader writes the store-level header: machine → remote, then the store's
// sync state vs origin/main and the most recent lastSeen for this machine.
func printHeader(out io.Writer, storeDir, machineName string, reg *registry.Registry, projects []string, fetch bool) {
	remote := resolveRemote(reg, projects)

	_, _ = fmt.Fprintf(out, "aimd • %s → %s\n", machineName, remote)

	last := latestLastSeen(reg, machineName, projects)
	syncLine := storeSyncLine(storeDir, fetch)
	if last.IsZero() {
		_, _ = fmt.Fprintf(out, "%s\n", syncLine)
	} else {
		_, _ = fmt.Fprintf(out, "%s · last sync %s\n", syncLine, relativeTime(last))
	}
	_, _ = fmt.Fprintln(out)
}

// resolveRemote prefers the machine config's remote, then falls back to the
// first reported project's RemoteURL, then "—".
func resolveRemote(reg *registry.Registry, projects []string) string {
	if cfgPath, err := config.DefaultPath(); err == nil {
		if cfg, err := config.Load(cfgPath); err == nil && cfg.Remote != "" {
			return cfg.Remote
		}
	}
	for _, pk := range projects {
		if p := reg.Projects[pk]; p != nil && p.RemoteURL != "" {
			return p.RemoteURL
		}
	}
	return "—"
}

// latestLastSeen returns the most recent lastSeen for machineName across the
// reported projects (zero time if this machine has never been recorded).
func latestLastSeen(reg *registry.Registry, machineName string, projects []string) time.Time {
	var latest time.Time
	for _, pk := range projects {
		p := reg.Projects[pk]
		if p == nil {
			continue
		}
		if m, ok := p.Machines[machineName]; ok && m.LastSeen.After(latest) {
			latest = m.LastSeen
		}
	}
	return latest
}

// storeSyncLine compares the store against origin/main and returns the
// "store: …" status line. Offline by default; with fetch it refreshes the
// remote-tracking ref first via store.DetectState.
func storeSyncLine(storeDir string, fetch bool) string {
	var (
		state   store.SyncState
		err     error
		offline = !fetch
	)
	if fetch {
		state, err = store.DetectState(storeDir)
	} else {
		state, err = store.DetectStateOffline(storeDir)
	}
	if err != nil {
		return "store: unknown (could not determine sync state)"
	}

	switch state {
	case store.StateBehind, store.StateDiverged:
		if offline {
			return "store: remote has new changes · run `aimd sync` or `aimd status --fetch`"
		}
		return "store: remote has new changes · run `aimd sync`"
	case store.StateAhead:
		return "store: local changes not pushed · run `aimd sync`"
	default:
		return "store: up to date"
	}
}

// printProject prints a project's name, its per-file state rows, and (only when
// allMachines is set) the cross-machine "also tracked on" block.
func printProject(out io.Writer, storeDir, machineName string, proj *registry.Project, key, root string, linkMode link.LinkMode, allMachines bool) {
	if proj == nil {
		return
	}

	name := proj.DisplayName
	if name == "" {
		name = key
	}
	_, _ = fmt.Fprintf(out, "%s\n", name)

	// A lingering project whose last file was untracked: surface it (rather than
	// dropping it silently) with a hint to forget it once `aimd remove` exists.
	if len(proj.Tracked) == 0 {
		_, _ = fmt.Fprintf(out, "  (no tracked files — run `aimd remove %s` to forget this project)\n", name)
		return
	}

	for _, tf := range proj.Tracked {
		overlaySrc := filepath.Join(storeDir, "repos", key, tf.Path)
		projectDst := filepath.Join(root, tf.Path)
		st := computeFileState(storeDir, key, tf.Path, overlaySrc, projectDst, linkMode)
		note := stateNote(st)
		if note != "" {
			_, _ = fmt.Fprintf(out, "  %s %s    %s\n", st.icon(), tf.Path, note)
		} else {
			_, _ = fmt.Fprintf(out, "  %s %s\n", st.icon(), tf.Path)
		}
	}

	if allMachines {
		printCrossMachine(out, proj, machineName)
	}
}

func stateNote(st fileState) string {
	switch st {
	case stateConflict:
		return "(conflict — run `aimd resolve`)"
	case stateBroken:
		return "(broken symlink)"
	case stateModified:
		return "(local edits not synced)"
	default:
		return ""
	}
}

// computeFileState resolves exactly one state for a tracked file, applying the
// precedence conflict > broken > modified > synced. relPath is the tracked
// file's project-relative path, used to scope the dirty check to this file.
func computeFileState(storeDir, key, relPath, overlaySrc, projectDst string, linkMode link.LinkMode) fileState {
	// conflict: an interrupted rebase left this overlay unmerged. Drive ⚡ from
	// git's index state (authoritative — catches modify/delete conflicts that
	// leave no marker text); a marker scan stays as an additional signal for a
	// content conflict whose worktree file still carries <<<<<<< markers.
	if store.RebaseInProgress(storeDir) {
		storeRel := filepath.Join("repos", key, relPath)
		if ours, theirs, err := store.UnmergedSides(storeDir, storeRel); err == nil && (ours || theirs) {
			return stateConflict
		}
		if hasMarkers, err := store.HasConflictMarkers(overlaySrc); err == nil && hasMarkers {
			return stateConflict
		}
	}

	// broken: overlay missing, destination missing/not a symlink, or link invalid.
	if _, err := os.Stat(overlaySrc); err != nil {
		return stateBroken
	}
	fi, err := os.Lstat(projectDst)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return stateBroken
	}
	if ok, verr := link.VerifyLink(projectDst, overlaySrc, linkMode); verr != nil || !ok {
		return stateBroken
	}

	// modified: link is valid but this file's overlay has uncommitted local edits.
	if dirty, err := store.OverlayFileDirty(storeDir, key, relPath); err == nil && dirty {
		return stateModified
	}

	return stateSynced
}

// printCrossMachine lists machines other than the current one that also track
// this project, with a relative lastSeen.
func printCrossMachine(out io.Writer, proj *registry.Project, machineName string) {
	var others []string
	for name := range proj.Machines {
		if name != machineName {
			others = append(others, name)
		}
	}
	if len(others) == 0 {
		return
	}
	sort.Strings(others)
	for _, name := range others {
		m := proj.Machines[name]
		when := "never"
		if m != nil && !m.LastSeen.IsZero() {
			when = relativeTime(m.LastSeen)
		}
		_, _ = fmt.Fprintf(out, "  also tracked on: %s (%s)\n", name, when)
	}
}

// relativeTime renders t as a coarse human-readable "… ago" string.
func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func init() {
	statusCmd.Flags().BoolVar(&statusAll, "all", false, "Report every tracked project, not just the current one")
	statusCmd.Flags().BoolVar(&statusAllMachines, "all-machines", false, "List the other machines tracking each reported project")
	statusCmd.Flags().BoolVar(&statusFetch, "fetch", false, "Fetch origin/main before reporting store sync state")
	rootCmd.AddCommand(statusCmd)
}

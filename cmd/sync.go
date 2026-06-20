package cmd

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CyberSecAuto-Labs/aimd/internal/project"
	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
	"github.com/CyberSecAuto-Labs/aimd/internal/store"
)

var syncAll bool

var syncCmd = &cobra.Command{
	Use:     "sync",
	GroupID: "sync",
	Short:   "Sync tracked files with the private store",
	Long: `Sync the private aimd store with the remote origin.

By default, aimd syncs only the current project (detected from the working directory).
Use --all to sync all registered projects for this machine.

Sync handles four states:
  UP_TO_DATE  — nothing to do, reports "store up to date"
  BEHIND      — fast-forward pull from origin
  AHEAD       — commits staged overlay changes and pushes
  DIVERGED    — rebases local commits on top of origin, then pushes (if clean)
  CONFLICT    — prints conflicted files and instructs user to resolve`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return RunSync(storePath, machine, syncAll, dryRun, cmd.OutOrStdout())
	},
}

// RunSync is the testable core of the sync command.
//
// storeDir is the resolved path to ~/.aimd/store.
// machineName identifies the current machine.
// all syncs all registered projects instead of just the current one.
// dryRun prints what would happen without making changes.
// out receives all user-facing output.
func RunSync(storeDir, machineName string, all, dryRun bool, out io.Writer) error {
	// Hold the exclusive store lock across the whole sync (fetch/pull/rebase +
	// commit/push for every project) so no other aimd process mutates the store
	// concurrently. A dry-run mutates nothing, so it skips the lock.
	if !dryRun {
		release, lockErr := lockStoreExclusive(storeDir)
		if lockErr != nil {
			return lockErr
		}
		defer release()
	}

	registryPath := filepath.Join(storeDir, ".aimd", "registry.json")
	reg, err := registry.LoadOrNew(registryPath)
	if err != nil {
		return fmt.Errorf("loading registry: %w", err)
	}

	if all {
		return runSyncAll(storeDir, machineName, registryPath, reg, dryRun, out)
	}

	// Single-project mode: detect project from CWD.
	proj, err := project.Detect()
	if err != nil {
		return fmt.Errorf("detecting project: %w", err)
	}

	projEntry, ok := registry.GetProject(reg, proj.Key)
	if !ok {
		return fmt.Errorf("project %q is not registered in the aimd store; run `aimd track` first", proj.Key)
	}

	displayName := filepath.Base(proj.Root)
	return syncProject(storeDir, proj.Key, displayName, projEntry, machineName, proj.Root, registryPath, dryRun, out)
}

// runSyncAll iterates all registered projects and syncs each one for which this
// machine has a localPath entry.
func runSyncAll(storeDir, machineName, registryPath string, reg *registry.Registry, dryRun bool, out io.Writer) error {
	if len(reg.Projects) == 0 {
		_, _ = fmt.Fprintln(out, "no projects registered in the aimd store")
		return nil
	}

	var lastErr error
	for key, projEntry := range reg.Projects {
		machineEntry, ok := projEntry.Machines[machineName]
		if !ok || machineEntry.LocalPath == "" {
			_, _ = fmt.Fprintf(out, "warning: skipping %q — no local path for machine %q\n", key, machineName)
			continue
		}

		displayName := projEntry.DisplayName
		if displayName == "" {
			displayName = filepath.Base(machineEntry.LocalPath)
		}

		if err := syncProject(storeDir, key, displayName, projEntry, machineName, machineEntry.LocalPath, registryPath, dryRun, out); err != nil {
			_, _ = fmt.Fprintf(out, "error syncing %q: %v\n", key, err)
			lastErr = err
		}
	}
	return lastErr
}

// syncProject performs the sync state-machine for a single project.
//
// projEntry is the caller's view of the project; it is only a fallback. After
// store.Sync, syncProject reloads the registry from disk and prefers the fresh
// project entry, so a long-lived caller (watch) cannot persist a stale snapshot.
func syncProject(
	storeDir, projectKey, displayName string,
	projEntry *registry.Project,
	machineName, localPath, registryPath string,
	dryRun bool,
	out io.Writer,
) error {
	if dryRun {
		_, _ = fmt.Fprintf(out, "dry-run: would sync %s\n", displayName)
		return nil
	}

	// Step 1: Run the store sync state machine.
	state, err := store.Sync(storeDir)

	// Handle conflict separately (error + state).
	var conflictErr *store.ConflictError
	if errors.As(err, &conflictErr) {
		_, _ = fmt.Fprintf(out, "conflict in store for %s:\n", displayName)
		for _, f := range conflictErr.Files {
			_, _ = fmt.Fprintf(out, "  %s\n", f)
			_, _ = fmt.Fprintf(out, "  Run: aimd resolve %s\n", f)
		}
		return fmt.Errorf("store conflict: %w", err)
	}
	if err != nil {
		return fmt.Errorf("syncing store: %w", err)
	}

	// store.Sync may have fast-forwarded remote commits onto disk (BEHIND),
	// including a registry.json another machine changed. Reload so a later
	// persistChange merges this machine's update onto the latest registry instead
	// of clobbering remote track/untracks with the pre-sync snapshot the caller
	// handed us. Reloading is a no-op for UP_TO_DATE/AHEAD.
	reg, err := registry.LoadOrNew(registryPath)
	if err != nil {
		return fmt.Errorf("reloading registry after sync: %w", err)
	}
	if reloaded := reg.Projects[projectKey]; reloaded != nil {
		projEntry = reloaded
	}

	// Step 2: A dirty overlay must always be committed and pushed, regardless of
	// how the local commit graph compares to origin. Local edits to a tracked
	// file leave the overlay worktree dirty while HEAD still equals origin/main
	// (StateUpToDate) — keying only off StateAhead would silently drop them.
	dirty, err := store.OverlayProjectDirty(storeDir, projectKey)
	if err != nil {
		return fmt.Errorf("checking overlay status: %w", err)
	}

	files := trackedFilePaths(projEntry)

	if dirty {
		if perr := persistChange(storeDir, projectKey, localPath, "sync", machineName, reg, projEntry, registryPath, files, out); perr != nil {
			return perr
		}
		_, _ = fmt.Fprintf(out, "✓ Synced: %s/%s\n", displayName, strings.Join(files, ", "))
		return nil
	}

	// Clean overlay. AHEAD only because of prior unpushed commits → just push them.
	if state == store.StateAhead {
		if pushErr := store.Push(storeDir); pushErr != nil {
			warnOnPushError(pushErr, storeDir, out)
			return nil
		}
		_, _ = fmt.Fprintf(out, "✓ Synced: %s/%s\n", displayName, strings.Join(files, ", "))
		return nil
	}

	// UP_TO_DATE, or UP_TO_DATE reached after a BEHIND fast-forward. Nothing to
	// commit; deliberately do NOT write the registry here — a lastSeen-only write
	// that sync never commits is exactly what left the store worktree dirty and
	// could later break a DIVERGED rebase. lastSeen is refreshed by
	// track/untrack/restore and by a sync that actually commits a dirty overlay.
	_, _ = fmt.Fprintf(out, "✓ %s: store up to date\n", displayName)
	return nil
}

// trackedFilePaths returns the tracked file paths from the project entry.
func trackedFilePaths(proj *registry.Project) []string {
	if proj == nil {
		return nil
	}
	paths := make([]string, 0, len(proj.Tracked))
	for _, tf := range proj.Tracked {
		paths = append(paths, tf.Path)
	}
	return paths
}

// isNothingToCommit returns true when the error from a store commit indicates that
// git exited because there was nothing staged, not because of a real failure.
// Git uses two variants: "nothing to commit" and "nothing added to commit".
func isNothingToCommit(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "nothing to commit") || strings.Contains(msg, "nothing added to commit")
}

func init() {
	syncCmd.Flags().BoolVar(&syncAll, "all", false, "Sync all registered projects")
	rootCmd.AddCommand(syncCmd)
}

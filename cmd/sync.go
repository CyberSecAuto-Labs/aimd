package cmd

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/CyberSecAuto-Labs/aimd/internal/project"
	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
	"github.com/CyberSecAuto-Labs/aimd/internal/store"
)

var syncAll bool

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync tracked files with the private store",
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
	return syncProject(storeDir, proj.Key, displayName, projEntry, machineName, proj.Root, registryPath, reg, dryRun, out)
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

		if err := syncProject(storeDir, key, displayName, projEntry, machineName, machineEntry.LocalPath, registryPath, reg, dryRun, out); err != nil {
			_, _ = fmt.Fprintf(out, "error syncing %q: %v\n", key, err)
			lastErr = err
		}
	}
	return lastErr
}

// syncProject performs the sync state-machine for a single project.
func syncProject(
	storeDir, projectKey, displayName string,
	projEntry *registry.Project,
	machineName, localPath, registryPath string,
	reg *registry.Registry,
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

	// Step 2: Handle state.
	switch state {
	case store.StateUpToDate:
		_, _ = fmt.Fprintf(out, "✓ %s: store up to date\n", displayName)
		updateLastSeen(projEntry, machineName, localPath, reg, registryPath)
		return nil

	case store.StateAhead:
		// We are ahead — stage modified overlays and push if there's anything to commit.

		// Build commit message.
		// Title: "sync: <project> [<machine> <timestamp>]"
		// Body:  "Synced files:\n  <file>\n  ..."
		files := trackedFilePaths(projEntry)
		title := fmt.Sprintf("sync: %s [%s %s]",
			displayName, machineName,
			time.Now().UTC().Format(time.RFC3339))

		var msg string
		if len(files) > 0 {
			var sb strings.Builder
			sb.WriteString(title)
			sb.WriteString("\n\nSynced files:\n")
			for _, f := range files {
				sb.WriteString("  ")
				sb.WriteString(f)
				sb.WriteString("\n")
			}
			msg = sb.String()
		} else {
			msg = title
		}

		filesStr := strings.Join(files, ", ")

		// CommitMsg stages repos/<key>/ (-u) and commits with the sync message.
		// Returns an error if nothing was staged (nothing to commit).
		if commitErr := store.CommitMsg(storeDir, projectKey, msg); commitErr != nil {
			if isNothingToCommit(commitErr) {
				_, _ = fmt.Fprintf(out, "✓ %s: nothing to sync\n", displayName)
				updateLastSeen(projEntry, machineName, localPath, reg, registryPath)
				return nil
			}
			return fmt.Errorf("committing to store: %w", commitErr)
		}

		// Push (warn on failure, don't fail).
		if pushErr := store.Push(storeDir); pushErr != nil {
			var pe *store.PushError
			if errors.As(pushErr, &pe) && !pe.Transient {
				_, _ = fmt.Fprintf(out, "warning: push rejected (may need manual intervention): %s\n", pe.Output)
			} else {
				_, _ = fmt.Fprintf(out, "warning: could not push to remote — changes committed locally; will retry on next sync. Run `git -C %s push` manually if needed.\n", storeDir)
			}
		} else {
			_, _ = fmt.Fprintf(out, "✓ Synced: %s/%s\n", displayName, filesStr)
		}

		updateLastSeen(projEntry, machineName, localPath, reg, registryPath)
		return nil

	default:
		// StateUpToDate after BEHIND pull.
		_, _ = fmt.Fprintf(out, "✓ %s: store up to date\n", displayName)
		updateLastSeen(projEntry, machineName, localPath, reg, registryPath)
		return nil
	}
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

// updateLastSeen updates the machine's lastSeen timestamp in the registry and saves it.
// Errors are silently ignored (lastSeen update is best-effort).
func updateLastSeen(proj *registry.Project, machineName, localPath string, reg *registry.Registry, registryPath string) {
	registry.UpsertMachine(proj, machineName, &registry.Machine{
		LocalPath: localPath,
		LastSeen:  time.Now().UTC(),
	})
	_ = registry.Save(registryPath, reg)
}

// isNothingToCommit returns true when the error from CommitMsg indicates that
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

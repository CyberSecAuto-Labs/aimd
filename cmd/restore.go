package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/CyberSecAuto-Labs/aimd/internal/config"
	"github.com/CyberSecAuto-Labs/aimd/internal/exclude"
	"github.com/CyberSecAuto-Labs/aimd/internal/link"
	"github.com/CyberSecAuto-Labs/aimd/internal/project"
	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
	"github.com/CyberSecAuto-Labs/aimd/internal/store"
)

var restoreForce bool

var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Restore tracked files as symlinks in the current project",
	Long: `Pull the latest store state, then re-create symlinks for every tracked
file that belongs to the current project.

By default restore warns and skips any destination that is an existing real
file. Use --force to replace real files with store overlays.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return RunRestore(storePath, machine, restoreForce, dryRun, cmd.OutOrStdout())
	},
}

// RunRestore is the testable core of the restore command.
//
// storeDir is the resolved path to ~/.aimd/store.
// machineName identifies the current machine.
// force replaces existing real files when true.
// dryRun prints what would happen without making changes.
// out receives all user-facing output.
func RunRestore(storeDir, machineName string, force, dryRun bool, out io.Writer) error {
	// Step 1: Pull the store (warn on failure, continue).
	pullOut, pullErr := exec.Command("git", "-C", storeDir, "pull", "--ff-only", "origin", "main").CombinedOutput()
	if pullErr != nil {
		_, _ = fmt.Fprintf(out, "warning: could not pull store — restoring from local state: %s\n", strings.TrimSpace(string(pullOut)))
	}

	// Step 2: Determine link mode from config (fall back to symlink).
	linkMode := link.LinkModeSymlink
	if cfgPath, err := config.DefaultPath(); err == nil {
		if cfg, err := config.Load(cfgPath); err == nil && cfg.LinkMode != "" {
			linkMode = link.LinkMode(cfg.LinkMode)
		}
	}

	// Step 3: Detect project.
	proj, err := project.Detect()
	if err != nil {
		return fmt.Errorf("detecting project: %w", err)
	}

	// Step 4: Load registry.
	registryPath := filepath.Join(storeDir, ".aimd", "registry.json")
	reg, err := registry.LoadOrNew(registryPath)
	if err != nil {
		return fmt.Errorf("loading registry: %w", err)
	}

	projEntry, ok := registry.GetProject(reg, proj.Key)
	if !ok || len(projEntry.Tracked) == 0 {
		_, _ = fmt.Fprintf(out, "no tracked files for project %q\n", proj.Key)
		return nil
	}

	if dryRun {
		_, _ = fmt.Fprintf(out, "dry-run: would restore %d file(s) for %s\n", len(projEntry.Tracked), proj.Key)
		return nil
	}

	// Step 5: Restore each tracked file.
	var restoredPaths []string
	for _, tf := range projEntry.Tracked {
		overlaySrc := filepath.Join(storeDir, "repos", proj.Key, tf.Path)
		projectDst := filepath.Join(proj.Root, tf.Path)

		// State 1: overlay missing → warn and skip.
		if _, statErr := os.Stat(overlaySrc); os.IsNotExist(statErr) {
			_, _ = fmt.Fprintf(out, "warning: %s not in store, skipping\n", tf.Path)
			continue
		}

		fi, lstatErr := os.Lstat(projectDst)

		if os.IsNotExist(lstatErr) {
			// State 5: destination missing → create symlink.
			if err := os.MkdirAll(filepath.Dir(projectDst), 0o755); err != nil {
				return fmt.Errorf("creating parent directory for %s: %w", tf.Path, err)
			}
			if err := link.CreateLink(overlaySrc, projectDst, linkMode); err != nil {
				return fmt.Errorf("creating link for %s: %w", tf.Path, err)
			}
			restoredPaths = append(restoredPaths, tf.Path)
			continue
		}

		if lstatErr != nil {
			return fmt.Errorf("stat %s: %w", tf.Path, lstatErr)
		}

		if fi.Mode()&os.ModeSymlink != 0 {
			// It's a symlink — check if it's already correct.
			ok, verifyErr := link.VerifyLink(projectDst, overlaySrc, linkMode)
			if verifyErr == nil && ok {
				// State 2: correct symlink → skip (idempotent).
				continue
			}
			// State 3: broken or wrong symlink → remove and recreate.
			if err := os.Remove(projectDst); err != nil {
				return fmt.Errorf("removing broken symlink %s: %w", tf.Path, err)
			}
			if err := link.CreateLink(overlaySrc, projectDst, linkMode); err != nil {
				return fmt.Errorf("creating link for %s: %w", tf.Path, err)
			}
			restoredPaths = append(restoredPaths, tf.Path)
			continue
		}

		// State 4: real file → warn unless --force.
		if !force {
			_, _ = fmt.Fprintf(out, "warning: %s is a real file; use --force to replace with store overlay\n", tf.Path)
			continue
		}
		// --force: remove real file and replace with symlink.
		if err := os.Remove(projectDst); err != nil {
			return fmt.Errorf("removing real file %s: %w", tf.Path, err)
		}
		if err := link.CreateLink(overlaySrc, projectDst, linkMode); err != nil {
			return fmt.Errorf("creating link for %s: %w", tf.Path, err)
		}
		restoredPaths = append(restoredPaths, tf.Path)
	}

	// Step 6: Update .git/info/exclude for all tracked files (idempotent).
	excludePath := filepath.Join(proj.Root, ".git", "info", "exclude")
	for _, tf := range projEntry.Tracked {
		if err := exclude.AppendEntry(excludePath, tf.Path); err != nil {
			return fmt.Errorf("updating .git/info/exclude for %s: %w", tf.Path, err)
		}
	}

	// Step 7: Registry machine upsert + save + writeProjectMetadata.
	registry.UpsertMachine(projEntry, machineName, &registry.Machine{
		LocalPath: proj.Root,
		LastSeen:  time.Now().UTC(),
	})
	registry.UpsertProject(reg, proj.Key, projEntry)

	if err := registry.Save(registryPath, reg); err != nil {
		return fmt.Errorf("saving registry: %w", err)
	}

	if err := writeProjectMetadata(storeDir, proj.Key, projEntry); err != nil {
		return fmt.Errorf("writing project metadata: %w", err)
	}

	// Step 8: Commit store (only restored files).
	if len(restoredPaths) > 0 {
		if commitErr := store.Commit(storeDir, proj.Key, proj.Root, "restore", machineName, restoredPaths); commitErr != nil {
			if !isNothingToCommit(commitErr) {
				return fmt.Errorf("committing to store: %w", commitErr)
			}
		}
	}

	// Step 9: Push (warn on failure, don't fail the command).
	if pushErr := store.Push(storeDir); pushErr != nil {
		var pe *store.PushError
		if errors.As(pushErr, &pe) && !pe.Transient {
			_, _ = fmt.Fprintf(out, "warning: push rejected (may need manual intervention): %s\n", pe.Output)
		} else {
			_, _ = fmt.Fprintf(out, "warning: could not push to remote — changes committed locally; will retry on next sync. Run `git -C %s push` manually if needed.\n", storeDir)
		}
	}

	_, _ = fmt.Fprintf(out, "✓ Restored %d file(s) in %s\n", len(restoredPaths), filepath.Base(proj.Root))
	return nil
}

func init() {
	restoreCmd.Flags().BoolVar(&restoreForce, "force", false, "Replace existing real files with store overlays")
	rootCmd.AddCommand(restoreCmd)
}

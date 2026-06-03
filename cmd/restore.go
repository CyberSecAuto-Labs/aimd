package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CyberSecAuto-Labs/aimd/internal/exclude"
	"github.com/CyberSecAuto-Labs/aimd/internal/link"
	"github.com/CyberSecAuto-Labs/aimd/internal/project"
	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
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
	// Pre-check: store must exist and be a git repo before anything else.
	if err := verifyStore(storeDir); err != nil {
		return err
	}

	// Step 1: Pull the store (warn on failure, continue).
	pullOut, pullErr := exec.Command("git", "-C", storeDir, "pull", "--ff-only", "origin", "main").CombinedOutput()
	if pullErr != nil {
		_, _ = fmt.Fprintf(out, "warning: could not pull store — restoring from local state: %s\n", strings.TrimSpace(string(pullOut)))
	}

	// Step 2: Determine link mode from config (fail fast on an unsupported mode,
	// before any destructive file replacement under --force).
	linkMode, err := loadLinkMode()
	if err != nil {
		return fmt.Errorf("link mode: %w", err)
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
		// Reject a registry path that escapes the project root before any file op.
		if pathEscapesRoot(tf.Path) {
			return fmt.Errorf("%s is outside the project root — skipping", tf.Path)
		}

		overlaySrc := filepath.Join(storeDir, "repos", proj.Key, tf.Path)
		projectDst := filepath.Join(proj.Root, tf.Path)

		restored, err := restoreFile(overlaySrc, projectDst, tf.Path, linkMode, force, out)
		if err != nil {
			return err
		}
		if restored {
			restoredPaths = append(restoredPaths, tf.Path)
		}
	}

	// Step 6: Update .git/info/exclude for all tracked files (idempotent).
	excludePath := filepath.Join(proj.Root, ".git", "info", "exclude")
	for _, tf := range projEntry.Tracked {
		if err := exclude.AppendEntry(excludePath, tf.Path); err != nil {
			return fmt.Errorf("updating .git/info/exclude for %s: %w", tf.Path, err)
		}
	}

	// Step 7: Persist via the shared ritual — but only when something was actually
	// restored, so an idempotent re-run neither writes the registry nor creates an
	// empty commit.
	if len(restoredPaths) > 0 {
		if perr := persistChange(storeDir, proj.Key, proj.Root, "restore", machineName, reg, projEntry, registryPath, restoredPaths, out); perr != nil {
			return perr
		}
	}

	_, _ = fmt.Fprintf(out, "✓ Restored %d file(s) in %s\n", len(restoredPaths), filepath.Base(proj.Root))
	return nil
}

func init() {
	restoreCmd.Flags().BoolVar(&restoreForce, "force", false, "Replace existing real files with store overlays")
	rootCmd.AddCommand(restoreCmd)
}

// verifyStore returns an error if storeDir is absent or is not a git repository.
func verifyStore(storeDir string) error {
	if _, err := os.Stat(storeDir); os.IsNotExist(err) {
		return fmt.Errorf("store not found at %s — run `aimd init` first", storeDir)
	}
	if _, err := os.Stat(filepath.Join(storeDir, ".git")); os.IsNotExist(err) {
		return fmt.Errorf("store at %s is not a git repository — run `aimd init` first", storeDir)
	}
	return nil
}

// restoreFile handles all possible destination states for a single tracked file.
// Returns true if a new symlink was created (i.e. something changed).
func restoreFile(overlaySrc, projectDst, tfPath string, linkMode link.LinkMode, force bool, out io.Writer) (bool, error) {
	if _, statErr := os.Stat(overlaySrc); os.IsNotExist(statErr) {
		_, _ = fmt.Fprintf(out, "warning: %s not in store, skipping\n", tfPath)
		return false, nil
	}

	fi, lstatErr := os.Lstat(projectDst)

	if os.IsNotExist(lstatErr) {
		if err := os.MkdirAll(filepath.Dir(projectDst), 0o755); err != nil {
			return false, fmt.Errorf("creating parent directory for %s: %w", tfPath, err)
		}
		if err := link.CreateLink(overlaySrc, projectDst, linkMode); err != nil {
			return false, fmt.Errorf("creating link for %s: %w", tfPath, err)
		}
		return true, nil
	}

	if lstatErr != nil {
		return false, fmt.Errorf("stat %s: %w", tfPath, lstatErr)
	}

	if fi.Mode()&os.ModeSymlink != 0 {
		ok, verifyErr := link.VerifyLink(projectDst, overlaySrc, linkMode)
		if verifyErr == nil && ok {
			return false, nil
		}
		if err := os.Remove(projectDst); err != nil {
			return false, fmt.Errorf("removing broken symlink %s: %w", tfPath, err)
		}
		if err := link.CreateLink(overlaySrc, projectDst, linkMode); err != nil {
			return false, fmt.Errorf("creating link for %s: %w", tfPath, err)
		}
		return true, nil
	}

	if fi.IsDir() {
		_, _ = fmt.Fprintf(out, "warning: %s is a directory; remove it manually to restore the symlink\n", tfPath)
		return false, nil
	}

	if !force {
		_, _ = fmt.Fprintf(out, "warning: %s is a real file; use --force to replace with store overlay\n", tfPath)
		return false, nil
	}

	// Copy-first: read the user's real file into memory so it can be restored if
	// linking fails. Unlike a naive remove-then-link, this never leaves the user
	// with no file when CreateLink errors.
	backup, readErr := os.ReadFile(projectDst)
	if readErr != nil {
		return false, fmt.Errorf("reading real file %s before replacing: %w", tfPath, readErr)
	}
	if err := os.Remove(projectDst); err != nil {
		return false, fmt.Errorf("removing real file %s: %w", tfPath, err)
	}
	if err := link.CreateLink(overlaySrc, projectDst, linkMode); err != nil {
		// Roll back: put the user's real file back from the in-memory backup.
		if restoreErr := os.WriteFile(projectDst, backup, fi.Mode().Perm()); restoreErr != nil {
			return false, fmt.Errorf("creating link for %s failed (%w) and restoring the real file failed: %w", tfPath, err, restoreErr)
		}
		return false, fmt.Errorf("creating link for %s: %w", tfPath, err)
	}
	return true, nil
}

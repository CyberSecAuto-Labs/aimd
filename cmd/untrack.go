package cmd

import (
	"fmt"
	"io"
	"os"
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

var (
	untrackDelete bool
	untrackYes    bool
)

var untrackCmd = &cobra.Command{
	Use:   "untrack <path> [<path>...]",
	Short: "Stop tracking a file and optionally restore or delete it",
	Long: `Remove a file from aimd tracking.

By default (--restore), the file is copied back from the store to the
project directory, the symlink is removed, and the overlay is deleted
from the store.

With --delete, both the symlink and the overlay are deleted without
restoring file content.  Use this flag carefully — content will be lost.

In both modes aimd prints what will happen and requires --yes to skip
the interactive confirmation prompt.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunUntrack(args, storePath, machine, untrackDelete, untrackYes, dryRun, os.Stdin, cmd.OutOrStdout())
	},
}

// RunUntrack is the testable core of the untrack command.
//
// targets are the file paths to untrack (relative to CWD or absolute).
// storeDir is the resolved path to the aimd store.
// machineName identifies the current machine.
// deleteMode removes the symlink and overlay without restoring content.
// yes skips the confirmation prompt.
// dryRun prints what would happen without making changes.
// in is used for reading confirmation input.
// out receives all user-facing output.
func RunUntrack(targets []string, storeDir, machineName string, deleteMode, yes, dryRun bool, in io.Reader, out io.Writer) error {
	// Step 1: Determine link mode from config (fall back to symlink).
	linkMode := link.LinkModeSymlink
	if cfgPath, err := config.DefaultPath(); err == nil {
		if cfg, err := config.Load(cfgPath); err == nil && cfg.LinkMode != "" {
			linkMode = link.LinkMode(cfg.LinkMode)
		}
	}

	// Step 2: Detect project (git root, key, remote URL).
	proj, err := project.Detect()
	if err != nil {
		return fmt.Errorf("detecting project: %w", err)
	}

	// Step 3: Load registry.
	registryPath := filepath.Join(storeDir, ".aimd", "registry.json")
	reg, err := registry.LoadOrNew(registryPath)
	if err != nil {
		return fmt.Errorf("loading registry: %w", err)
	}

	// Look up or create the project entry.
	projEntry, ok := registry.GetProject(reg, proj.Key)
	if !ok {
		projEntry = &registry.Project{
			DisplayName: filepath.Base(proj.Root),
			RemoteURL:   proj.RemoteURL,
			Machines:    map[string]*registry.Machine{},
			Tracked:     []registry.TrackedFile{},
		}
	}

	// Step 4: Process each target file.
	var processed int
	for _, target := range targets {
		if err := untrackFile(target, proj.Root, proj.Key, storeDir, machineName, linkMode, projEntry, deleteMode, yes, dryRun, in, out); err != nil {
			return err
		}
		processed++
	}

	if dryRun {
		_, _ = fmt.Fprintf(out, "dry-run: %d file(s) would be untracked\n", processed)
		return nil
	}

	// Step 5: Update registry machine lastSeen.
	registry.UpsertMachine(projEntry, machineName, &registry.Machine{
		LocalPath: proj.Root,
		LastSeen:  time.Now().UTC(),
	})
	registry.UpsertProject(reg, proj.Key, projEntry)

	// Step 6: Save registry.
	if err := registry.Save(registryPath, reg); err != nil {
		return fmt.Errorf("saving registry: %w", err)
	}

	// Step 7: Write metadata/<project-key>.json.
	if err := writeProjectMetadata(storeDir, proj.Key, projEntry); err != nil {
		return fmt.Errorf("writing project metadata: %w", err)
	}

	// Step 8: git add + commit + push.
	if err := store.Commit(storeDir, proj.Key, proj.Root, "untrack", machineName); err != nil {
		return fmt.Errorf("committing to store: %w", err)
	}
	if err := store.Push(storeDir); err != nil {
		_, _ = fmt.Fprintf(out, "warning: could not push to remote. Run `git -C %s push` manually.\n  (%s)\n", storeDir, err)
	}

	return nil
}

// untrackFile performs untracking of a single file.
func untrackFile(
	target, gitRoot, _ /* projectKey */, storeDir, machineName string,
	linkMode link.LinkMode,
	proj *registry.Project,
	deleteMode, yes, dryRun bool,
	in io.Reader,
	out io.Writer,
) error {
	// Resolve to absolute path.
	abs := target
	if !filepath.IsAbs(abs) {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}
		abs = filepath.Join(cwd, target)
	}

	// Compute relative path from git root.
	relPath, err := filepath.Rel(gitRoot, abs)
	if err != nil {
		return fmt.Errorf("computing relative path for %s: %w", target, err)
	}

	// Validation: file must exist.
	fi, err := os.Lstat(abs)
	if err != nil {
		return fmt.Errorf("stat %s: %w", target, err)
	}

	// Validation: file must be a symlink.
	if fi.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%s is not a symlink — only tracked files (symlinks into the store) can be untracked", relPath)
	}

	// Validation: symlink must point into storeDir.
	symlinkTarget, err := os.Readlink(abs)
	if err != nil {
		return fmt.Errorf("reading symlink %s: %w", relPath, err)
	}
	reposDir := filepath.Join(storeDir, "repos")
	if !strings.HasPrefix(symlinkTarget, reposDir) {
		return fmt.Errorf("%s is a symlink but does not point into the aimd store — skipping", relPath)
	}

	overlayPath := symlinkTarget

	if deleteMode {
		// Print warning and action for --delete mode.
		_, _ = fmt.Fprintf(out, "WARNING: --delete will remove %s from the project AND the store. Content will be lost.\n", relPath)
		_, _ = fmt.Fprintf(out, "Will delete symlink and overlay for %s\n", relPath)
	} else {
		// Print action for --restore mode.
		_, _ = fmt.Fprintf(out, "Will restore %s from store → project and remove from store\n", relPath)
	}

	if dryRun {
		if deleteMode {
			_, _ = fmt.Fprintf(out, "dry-run: would delete %s (symlink + overlay)\n", relPath)
		} else {
			_, _ = fmt.Fprintf(out, "dry-run: would restore %s to project\n", relPath)
		}
		return nil
	}

	// Confirmation prompt unless --yes.
	if !yes {
		confirmed, _ := confirmPrompt(out, in, "Continue?")
		if !confirmed {
			_, _ = fmt.Fprintf(out, "Aborted.\n")
			return nil
		}
	}

	excludePath := filepath.Join(gitRoot, ".git", "info", "exclude")

	if deleteMode {
		// --delete: remove symlink and overlay without restoring content.
		if err := link.RemoveLink(abs, linkMode); err != nil {
			return fmt.Errorf("removing symlink %s: %w", relPath, err)
		}

		if err := os.Remove(overlayPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing overlay %s: %w", overlayPath, err)
		}

		if err := exclude.RemoveEntry(excludePath, relPath); err != nil {
			return fmt.Errorf("removing .git/info/exclude entry for %s: %w", relPath, err)
		}

		registry.RemoveTrackedFile(proj, relPath)

		_, _ = fmt.Fprintf(out, "✓ Deleted %s (removed from project and store)\n", relPath)
	} else {
		// --restore (default): copy overlay → project, remove symlink.

		// Read overlay content (following the symlink via os.ReadFile which follows symlinks).
		content, err := os.ReadFile(overlayPath)
		if err != nil {
			return fmt.Errorf("reading overlay %s: %w", overlayPath, err)
		}

		// Get the overlay file permissions.
		overlayFi, err := os.Stat(overlayPath)
		if err != nil {
			return fmt.Errorf("stat overlay %s: %w", overlayPath, err)
		}

		// Remove the symlink.
		if err := link.RemoveLink(abs, linkMode); err != nil {
			return fmt.Errorf("removing symlink %s: %w", relPath, err)
		}

		// Write original file back at project path with same content.
		if err := os.WriteFile(abs, content, overlayFi.Mode().Perm()); err != nil {
			return fmt.Errorf("restoring %s to project: %w", relPath, err)
		}

		// Remove overlay file from store.
		if err := os.Remove(overlayPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing overlay %s: %w", overlayPath, err)
		}

		if err := exclude.RemoveEntry(excludePath, relPath); err != nil {
			return fmt.Errorf("removing .git/info/exclude entry for %s: %w", relPath, err)
		}

		registry.RemoveTrackedFile(proj, relPath)

		_, _ = fmt.Fprintf(out, "✓ Untracked %s (restored to project)\n", relPath)
	}

	_ = machineName // used in registry update at caller level
	return nil
}

// confirmPrompt prints msg + " [y/N]: " to out and reads an answer from in.
// Returns true only if the user types "y" or "Y".
// EOF or empty input is treated as "N" (returns false, nil).
func confirmPrompt(out io.Writer, in io.Reader, msg string) (bool, error) {
	_, _ = fmt.Fprint(out, msg+" [y/N]: ")
	var answer string
	// Ignore read errors (EOF, no input) — treat as "N".
	_, _ = fmt.Fscan(in, &answer)
	return strings.ToLower(strings.TrimSpace(answer)) == "y", nil
}

func init() {
	untrackCmd.Flags().BoolVar(&untrackDelete, "delete", false, "Remove symlink and overlay only (no restore); content will be lost")
	untrackCmd.Flags().BoolVar(&untrackYes, "yes", false, "Skip confirmation prompt")
	rootCmd.AddCommand(untrackCmd)
}

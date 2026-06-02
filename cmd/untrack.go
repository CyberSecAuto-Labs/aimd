package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CyberSecAuto-Labs/aimd/internal/exclude"
	"github.com/CyberSecAuto-Labs/aimd/internal/link"
	"github.com/CyberSecAuto-Labs/aimd/internal/project"
	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
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
	// Step 1: Determine link mode from config (fail fast on an unsupported mode).
	linkMode, err := loadLinkMode()
	if err != nil {
		return fmt.Errorf("link mode: %w", err)
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

	// Step 4: Process each target file. Stop at the first failure but remember
	// which files already succeeded so they can still be persisted.
	var processed int
	var untrackedRelPaths []string
	var untrackErr error
	for _, target := range targets {
		if err := untrackFile(target, proj.Root, proj.Key, storeDir, machineName, linkMode, projEntry, deleteMode, yes, dryRun, in, out); err != nil {
			untrackErr = err
			break
		}
		processed++
		// Compute relative path for the commit body.
		abs := target
		if !filepath.IsAbs(abs) {
			if cwd, cwdErr := os.Getwd(); cwdErr == nil {
				abs = filepath.Join(cwd, target)
			}
		}
		if relPath, relErr := filepath.Rel(proj.Root, abs); relErr == nil {
			untrackedRelPaths = append(untrackedRelPaths, relPath)
		}
	}

	if dryRun {
		if untrackErr != nil {
			return untrackErr
		}
		_, _ = fmt.Fprintf(out, "dry-run: %d file(s) would be untracked\n", processed)
		return nil
	}

	// Step 5: Persist whatever succeeded — even when a later target failed — so the
	// registry and store always reflect the actual on-disk state. A
	// mid-batch failure can otherwise permanently delete an earlier file while the
	// saved registry still lists it as tracked.
	if len(untrackedRelPaths) > 0 {
		if perr := persistChange(storeDir, proj.Key, proj.Root, "untrack", machineName, reg, projEntry, registryPath, untrackedRelPaths, out); perr != nil {
			return errors.Join(untrackErr, perr)
		}
	}

	return untrackErr
}

// untrackFile performs untracking of a single file.
func untrackFile(
	target, gitRoot, projectKey, storeDir, _ /* machineName */ string,
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

	// Validation: the symlink must point inside THIS project's overlay directory.
	// A bare prefix check on repos/ is not enough — it would let untrack delete
	// another project's overlay (or a sibling like repos-backup/) while this
	// project's registry entry survives.
	symlinkTarget, err := os.Readlink(abs)
	if err != nil {
		return fmt.Errorf("reading symlink %s: %w", relPath, err)
	}
	projOverlayDir := filepath.Join(storeDir, "repos", projectKey)
	rel, relErr := filepath.Rel(projOverlayDir, symlinkTarget)
	if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("%s does not point into this project's overlay (%s) — skipping for safety", relPath, projOverlayDir)
	}

	// Reject a target that escapes the project root.
	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("%s is outside the project root — skipping", target)
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
		return deleteTrackedFile(abs, overlayPath, excludePath, relPath, linkMode, proj, out)
	}
	return restoreTrackedFile(abs, overlayPath, excludePath, relPath, linkMode, proj, out)
}

// deleteTrackedFile removes the overlay and symlink without restoring content.
// The overlay is removed BEFORE the symlink so that if overlay removal fails the
// project symlink remains intact and the file stays re-untrackable, rather than
// being left as an orphaned overlay plus a missing symlink.
func deleteTrackedFile(abs, overlayPath, excludePath, relPath string, linkMode link.LinkMode, proj *registry.Project, out io.Writer) error {
	if err := os.Remove(overlayPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing overlay %s: %w", overlayPath, err)
	}
	if err := link.RemoveLink(abs, linkMode); err != nil {
		return fmt.Errorf("removing symlink %s: %w", relPath, err)
	}
	if err := exclude.RemoveEntry(excludePath, relPath); err != nil {
		return fmt.Errorf("removing .git/info/exclude entry for %s: %w", relPath, err)
	}
	registry.RemoveTrackedFile(proj, relPath)
	_, _ = fmt.Fprintf(out, "✓ Deleted %s (removed from project and store)\n", relPath)
	return nil
}

// restoreTrackedFile copies the overlay back to the project directory and removes the symlink and overlay.
func restoreTrackedFile(abs, overlayPath, excludePath, relPath string, linkMode link.LinkMode, proj *registry.Project, out io.Writer) error {
	content, err := os.ReadFile(overlayPath)
	if err != nil {
		return fmt.Errorf("reading overlay %s: %w", overlayPath, err)
	}
	overlayFi, err := os.Stat(overlayPath)
	if err != nil {
		return fmt.Errorf("stat overlay %s: %w", overlayPath, err)
	}
	if err := link.RemoveLink(abs, linkMode); err != nil {
		return fmt.Errorf("removing symlink %s: %w", relPath, err)
	}
	if err := os.WriteFile(abs, content, overlayFi.Mode().Perm()); err != nil {
		return fmt.Errorf("restoring %s to project: %w", relPath, err)
	}
	if err := os.Remove(overlayPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing overlay %s: %w", overlayPath, err)
	}
	if err := exclude.RemoveEntry(excludePath, relPath); err != nil {
		return fmt.Errorf("removing .git/info/exclude entry for %s: %w", relPath, err)
	}
	registry.RemoveTrackedFile(proj, relPath)
	_, _ = fmt.Fprintf(out, "✓ Untracked %s (restored to project)\n", relPath)
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

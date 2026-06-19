package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
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

var trackCmd = &cobra.Command{
	Use:     "track <path> [<path>...]",
	GroupID: "tracking",
	Short:   "Start tracking a file or directory in the private store",
	Long: `Copy a file (or all files in a directory) into the private aimd store,
replace it with a symlink, and hide it from git status via .git/info/exclude.

Multiple paths may be given. Directories are walked recursively.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunTrack(args, storePath, machine, dryRun, cmd.OutOrStdout())
	},
}

// RunTrack is the testable core of the track command.
//
// targets are the file or directory paths to track (relative to CWD or absolute).
// storeDir is the resolved path to ~/.aimd/store.
// machineName identifies the current machine.
// dryRun prints what would happen without making changes.
// out receives all user-facing output.
func RunTrack(targets []string, storeDir, machineName string, dryRun bool, out io.Writer) error {
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

	// Step 4: Collect all files from targets.
	filePaths, err := expandTargets(targets)
	if err != nil {
		return err
	}

	// Step 5: Track each file. Stop at the first failure but remember which files
	// already succeeded so they can still be persisted.
	var tracked int
	var trackedRelPaths []string
	var trackErr error
	for _, filePath := range filePaths {
		if err := trackFile(filePath, proj.Root, proj.Key, storeDir, machineName, linkMode, projEntry, dryRun, out); err != nil {
			trackErr = err
			break
		}
		tracked++
		if relPath, relErr := filepath.Rel(proj.Root, filePath); relErr == nil {
			trackedRelPaths = append(trackedRelPaths, relPath)
		}
	}

	if dryRun {
		if trackErr != nil {
			return trackErr
		}
		_, _ = fmt.Fprintf(out, "dry-run: %d file(s) would be tracked\n", tracked)
		return nil
	}

	// Step 6: Persist whatever succeeded — even when a later target failed — so the
	// registry and store always reflect the actual on-disk state.
	if len(trackedRelPaths) > 0 {
		if perr := persistChange(storeDir, proj.Key, proj.Root, "track", machineName, reg, projEntry, registryPath, trackedRelPaths, out); perr != nil {
			return errors.Join(trackErr, perr)
		}
	}

	return trackErr
}

// trackFile performs the copy-first safety sequence for a single file.
func trackFile(
	filePath, gitRoot, projectKey, storeDir, machineName string,
	linkMode link.LinkMode,
	proj *registry.Project,
	dryRun bool,
	out io.Writer,
) error {
	// Compute relative path from git root.
	relPath, err := filepath.Rel(gitRoot, filePath)
	if err != nil {
		return fmt.Errorf("computing relative path for %s: %w", filePath, err)
	}

	// Reject a target that escapes the project root before any file op.
	if pathEscapesRoot(relPath) {
		return fmt.Errorf("%s is outside the project root — skipping", filePath)
	}

	// Validation: file must exist and not already be a symlink.
	fi, err := os.Lstat(filePath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", filePath, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is already a symlink — skipping", relPath)
	}

	// Validation: not already tracked.
	for _, tf := range proj.Tracked {
		if tf.Path == relPath {
			return fmt.Errorf("%s is already tracked", relPath)
		}
	}

	if dryRun {
		overlayPath := filepath.Join(storeDir, "repos", projectKey, relPath)
		_, _ = fmt.Fprintf(out, "dry-run: would track %s → %s\n", relPath, overlayPath)
		return nil
	}

	// Compute overlay destination.
	overlayPath := filepath.Join(storeDir, "repos", projectKey, relPath)
	overlayDir := filepath.Dir(overlayPath)

	// 5a: Create overlay directory.
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		return fmt.Errorf("creating overlay directory: %w", err)
	}

	// 5b: Copy file bytes to overlay.
	srcData, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", relPath, err)
	}
	if err := os.WriteFile(overlayPath, srcData, fi.Mode().Perm()); err != nil {
		return fmt.Errorf("writing overlay %s: %w", overlayPath, err)
	}

	// 5c: Verify overlay exists and has same byte count.
	overlayFi, err := os.Stat(overlayPath)
	if err != nil {
		return fmt.Errorf("verifying overlay copy: %w", err)
	}
	if overlayFi.Size() != fi.Size() {
		return fmt.Errorf("overlay size mismatch for %s: src=%d overlay=%d", relPath, fi.Size(), overlayFi.Size())
	}

	// 5d: Remove original file before creating symlink (os.Symlink fails if dest exists).
	if err := os.Remove(filePath); err != nil {
		return fmt.Errorf("removing original %s before symlinking: %w", relPath, err)
	}

	// 5e: Create symlink at filePath → overlayPath.
	if err := link.CreateLink(overlayPath, filePath, linkMode); err != nil {
		// Attempt to restore the original from the overlay copy. If that restore
		// also fails the original is gone from the project (it survives only as the
		// uncommitted overlay), so surface both errors rather than swallowing it.
		if restoreErr := os.WriteFile(filePath, srcData, fi.Mode().Perm()); restoreErr != nil {
			return fmt.Errorf("creating link for %s failed (%w) and restoring the original failed: %w — content remains in overlay %s", relPath, err, restoreErr, overlayPath)
		}
		return fmt.Errorf("creating link for %s: %w", relPath, err)
	}

	// 5f: Verify symlink resolves correctly.
	ok, err := link.VerifyLink(filePath, overlayPath, linkMode)
	if err != nil {
		return fmt.Errorf("verifying link for %s: %w", relPath, err)
	}
	if !ok {
		return fmt.Errorf("link verification failed for %s", relPath)
	}

	// Step 6: Add to exclude.
	excludePath := filepath.Join(gitRoot, ".git", "info", "exclude")
	if err := exclude.AppendEntry(excludePath, relPath); err != nil {
		return fmt.Errorf("updating .git/info/exclude for %s: %w", relPath, err)
	}

	// Update project tracked list.
	registry.AddTrackedFile(proj, registry.TrackedFile{
		Path:    relPath,
		AddedAt: time.Now().UTC(),
		AddedBy: machineName,
	})

	_, _ = fmt.Fprintf(out, "✓ Tracking %s → private store\n", relPath)
	return nil
}

// writeProjectMetadata writes metadata/<project-key>.json to the store.
func writeProjectMetadata(storeDir, projectKey string, proj *registry.Project) error {
	metaDir := filepath.Join(storeDir, "metadata")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return fmt.Errorf("creating metadata directory: %w", err)
	}

	data, err := json.MarshalIndent(proj, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding project metadata: %w", err)
	}
	data = append(data, '\n')

	metaPath := filepath.Join(metaDir, projectKey+".json")
	tmp := metaPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing metadata temp file: %w", err)
	}
	if err := os.Rename(tmp, metaPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming metadata file into place: %w", err)
	}
	return nil
}

func init() {
	rootCmd.AddCommand(trackCmd)
}

// warnOnPushError prints a warning when a push fails without failing the command.
// Distinguishes hard rejections (non-transient) from transient network failures.
func warnOnPushError(err error, storeDir string, out io.Writer) {
	var pe *store.PushError
	if errors.As(err, &pe) && !pe.Transient {
		_, _ = fmt.Fprintf(out, "warning: push rejected (may need manual intervention): %s\n", pe.Output)
	} else {
		_, _ = fmt.Fprintf(out, "warning: could not push to remote — changes committed locally; will retry on next sync. Run `git -C %s push` manually if needed.\n", storeDir)
	}
}

// loadLinkMode reads the configured link mode (defaulting to symlink) and
// validates it, so callers fail fast on an unsupported mode before any
// destructive file operation rather than after the user's file is removed.
func loadLinkMode() (link.LinkMode, error) {
	mode := link.LinkModeSymlink
	if cfgPath, err := config.DefaultPath(); err == nil {
		if cfg, err := config.Load(cfgPath); err == nil && cfg.LinkMode != "" {
			mode = link.LinkMode(cfg.LinkMode)
		}
	}
	return validateLinkMode(mode)
}

// validateLinkMode returns mode unchanged when it is supported, or an error
// describing why it cannot be used. Only symlink is implemented in v1.
func validateLinkMode(mode link.LinkMode) (link.LinkMode, error) {
	switch mode {
	case link.LinkModeSymlink:
		return mode, nil
	case link.LinkModeHardlink, link.LinkModeCopy:
		return "", fmt.Errorf("link mode %q is not implemented in v1 (only %q is supported)", mode, link.LinkModeSymlink)
	default:
		return "", fmt.Errorf("unknown link mode %q in config (expected %q)", mode, link.LinkModeSymlink)
	}
}

// isVCSDir reports whether name is a version-control metadata directory that must
// never be walked into when expanding a directory target.
func isVCSDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", ".bzr":
		return true
	default:
		return false
	}
}

// hasVCSComponent reports whether any element of path is a version-control
// metadata directory. The directory walk skips these, but an explicitly-named
// target inside one (e.g. `.git/config`) would otherwise slip through.
func hasVCSComponent(path string) bool {
	return slices.ContainsFunc(strings.Split(filepath.ToSlash(path), "/"), isVCSDir)
}

// expandTargets resolves each target (file or directory path) to a list of absolute file paths.
// Directories are walked recursively; symlinks to directories are not followed.
func expandTargets(targets []string) ([]string, error) {
	var filePaths []string
	for _, target := range targets {
		abs := target
		if !filepath.IsAbs(abs) {
			cwd, err := os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("getting working directory: %w", err)
			}
			abs = filepath.Join(cwd, target)
		}

		// A VCS metadata path named explicitly bypasses the walk's SkipDir guard,
		// so reject it up front before any file is copied or relinked.
		if hasVCSComponent(abs) {
			return nil, fmt.Errorf("refusing to track %s: path is inside a version-control directory", target)
		}

		fi, err := os.Lstat(abs)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", target, err)
		}

		if !fi.IsDir() {
			filePaths = append(filePaths, abs)
			continue
		}

		if err := filepath.WalkDir(abs, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				// Never descend into a VCS metadata directory: tracking `.` from a
				// repo root must not relocate the repo's own git internals into the
				// store and replace them with symlinks.
				if isVCSDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			filePaths = append(filePaths, path)
			return nil
		}); err != nil {
			return nil, fmt.Errorf("walking directory %s: %w", target, err)
		}
	}
	return filePaths, nil
}

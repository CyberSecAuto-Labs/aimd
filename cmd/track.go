package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	Use:   "track <path> [<path>...]",
	Short: "Start tracking a file or directory in the private store",
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
	// Step 1: Determine link mode from config (fall back to symlink).
	linkMode := loadLinkMode()

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

	// Step 5: Track each file; collect relative paths for the commit body.
	var tracked int
	var trackedRelPaths []string
	for _, filePath := range filePaths {
		if err := trackFile(filePath, proj.Root, proj.Key, storeDir, machineName, linkMode, projEntry, dryRun, out); err != nil {
			return err
		}
		tracked++
		if relPath, relErr := filepath.Rel(proj.Root, filePath); relErr == nil {
			trackedRelPaths = append(trackedRelPaths, relPath)
		}
	}

	if dryRun {
		_, _ = fmt.Fprintf(out, "dry-run: %d file(s) would be tracked\n", tracked)
		return nil
	}

	// Step 6: Update registry entry with machine info.
	registry.UpsertMachine(projEntry, machineName, &registry.Machine{
		LocalPath: proj.Root,
		LastSeen:  time.Now().UTC(),
	})
	registry.UpsertProject(reg, proj.Key, projEntry)

	// Step 7: Save registry.
	if err := registry.Save(registryPath, reg); err != nil {
		return fmt.Errorf("saving registry: %w", err)
	}

	// Step 8: Write metadata/<project-key>.json.
	if err := writeProjectMetadata(storeDir, proj.Key, projEntry); err != nil {
		return fmt.Errorf("writing project metadata: %w", err)
	}

	// Step 9: git add + commit + push.
	if err := store.Commit(storeDir, proj.Key, proj.Root, "track", machineName, trackedRelPaths); err != nil {
		return fmt.Errorf("committing to store: %w", err)
	}
	if pushErr := store.Push(storeDir); pushErr != nil {
		warnOnPushError(pushErr, storeDir, out)
	}

	return nil
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
		// Attempt to restore the original from the overlay copy.
		_ = os.WriteFile(filePath, srcData, fi.Mode().Perm())
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

// loadLinkMode reads the configured link mode, defaulting to symlink when config is absent.
func loadLinkMode() link.LinkMode {
	if cfgPath, err := config.DefaultPath(); err == nil {
		if cfg, err := config.Load(cfgPath); err == nil && cfg.LinkMode != "" {
			return link.LinkMode(cfg.LinkMode)
		}
	}
	return link.LinkModeSymlink
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
			if !d.IsDir() {
				filePaths = append(filePaths, path)
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("walking directory %s: %w", target, err)
		}
	}
	return filePaths, nil
}

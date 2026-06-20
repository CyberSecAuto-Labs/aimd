package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/CyberSecAuto-Labs/aimd/internal/link"
	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
	"github.com/CyberSecAuto-Labs/aimd/internal/store"
)

var resetYes bool

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

reset prints what it will do and requires --yes to skip the confirmation prompt.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return RunReset(storePath, machine, resetYes, dryRun, os.Stdin, cmd.OutOrStdout())
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
// in is used for reading confirmation input; out receives all user-facing output.
func RunReset(storeDir, machineName string, yes, dryRun bool, in io.Reader, out io.Writer) error {
	if err := verifyStore(storeDir); err != nil {
		return err
	}

	// Hold the exclusive store lock across the whole teardown (every project's
	// restore + local registry/store updates) and across the confirmation
	// prompt, so no other aimd process mutates the store concurrently. A dry-run
	// mutates nothing.
	if !dryRun {
		release, lockErr := lockStoreExclusive(storeDir)
		if lockErr != nil {
			return lockErr
		}
		defer release()
	}

	linkMode, err := loadLinkMode()
	if err != nil {
		return fmt.Errorf("link mode: %w", err)
	}

	registryPath := filepath.Join(storeDir, ".aimd", "registry.json")
	reg, err := registry.LoadOrNew(registryPath)
	if err != nil {
		return fmt.Errorf("loading registry: %w", err)
	}

	targets, skipped := planReset(reg, machineName)

	if len(targets) == 0 {
		_, _ = fmt.Fprintf(out, "No projects checked out on %q — nothing to reset.\n", machineName)
		for _, k := range skipped {
			_, _ = fmt.Fprintf(out, "  (skipped %s — not checked out here)\n", k)
		}
		return nil
	}

	// Print the plan before acting so the user knows exactly what changes.
	_, _ = fmt.Fprintf(out, "aimd reset will restore tracked files and forget these projects on %q:\n", machineName)
	var totalFiles int
	for _, t := range targets {
		totalFiles += len(t.proj.Tracked)
		_, _ = fmt.Fprintf(out, "  %s (%d file(s)) → %s\n", t.name, len(t.proj.Tracked), t.localPath)
	}
	for _, k := range skipped {
		_, _ = fmt.Fprintf(out, "  (skipped %s — not checked out on this machine)\n", k)
	}
	_, _ = fmt.Fprintf(out, "(no push — the remote and other machines are untouched; finish with `brew uninstall --zap aimd`)\n")

	if dryRun {
		_, _ = fmt.Fprintf(out, "dry-run: would restore %d file(s) across %d project(s)\n", totalFiles, len(targets))
		return nil
	}

	if !yes {
		confirmed, _ := confirmPrompt(out, in, "Continue?")
		if !confirmed {
			_, _ = fmt.Fprintf(out, "Aborted.\n")
			return nil
		}
	}

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

	_, _ = fmt.Fprintf(out, "✓ Reset %d of %d project(s); tracked files restored to your working trees.\n", done, len(targets))
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
	rootCmd.AddCommand(resetCmd)
}

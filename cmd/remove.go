package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CyberSecAuto-Labs/aimd/internal/project"
	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
	"github.com/CyberSecAuto-Labs/aimd/internal/store"
)

var (
	removeForce bool
	removeYes   bool
)

var removeCmd = &cobra.Command{
	Use:     "remove [<project>]",
	GroupID: "tracking",
	Short:   "Forget a project entirely — drop it from the store and registry",
	Long: `Remove a project from aimd: drop its registry entry and delete its
overlays (repos/<key>/) and metadata from the store. This never touches the
project's working tree — it only cleans up aimd's own bookkeeping.

Unlike untrack (which is per-file), remove forgets the whole project. With no
argument it targets the current project; pass a project key or display name to
forget a project that is not checked out on this machine.

A project that still has tracked files is refused unless --force. remove prints
what it will do and requires --yes to skip the confirmation prompt.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunRemove(args, storePath, machine, removeForce, removeYes, dryRun, os.Stdin, cmd.OutOrStdout())
	},
}

// RunRemove is the testable core of the remove command.
//
// args is empty (target the current project) or a single project key/display name.
// storeDir is the resolved path to the aimd store.
// machineName identifies the current machine.
// force allows removing a project that still has tracked files.
// yes skips the confirmation prompt.
// dryRun prints what would happen without making changes.
// in is used for reading confirmation input; out receives all user-facing output.
func RunRemove(args []string, storeDir, machineName string, force, yes, dryRun bool, in io.Reader, out io.Writer) error {
	if err := verifyStore(storeDir); err != nil {
		return err
	}

	// Hold the exclusive store lock across registry load → store removal →
	// push, and across the confirmation prompt so the plan can't be invalidated
	// by another aimd process mid-prompt. A dry-run mutates nothing.
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

	key, projEntry, err := resolveRemoveTarget(args, reg)
	if err != nil {
		return err
	}

	if len(projEntry.Tracked) > 0 && !force {
		return fmt.Errorf(
			"project %q still has %d tracked file(s) — run `aimd untrack` first, or pass --force",
			displayOr(projEntry.DisplayName, key), len(projEntry.Tracked))
	}

	// Print the plan before acting so the user knows exactly what changes.
	name := displayOr(projEntry.DisplayName, key)
	_, _ = fmt.Fprintf(out, "Will forget project %s (key %s):\n", name, key)
	_, _ = fmt.Fprintf(out, "  - remove its overlays and metadata from the store\n")
	_, _ = fmt.Fprintf(out, "  - drop its entry from the registry\n")
	_, _ = fmt.Fprintf(out, "  (your project working tree is never touched)\n")
	if len(projEntry.Tracked) > 0 && force {
		_, _ = fmt.Fprintf(out,
			"WARNING: --force will also remove %d tracked overlay(s) from the store.\n",
			len(projEntry.Tracked))
	}

	if dryRun {
		_, _ = fmt.Fprintf(out, "dry-run: would remove project %s (key %s)\n", name, key)
		return nil
	}

	if !yes {
		confirmed, _ := confirmPrompt(out, in, "Continue?")
		if !confirmed {
			_, _ = fmt.Fprintf(out, "Aborted.\n")
			return nil
		}
	}

	delete(reg.Projects, key)
	if err := registry.Save(registryPath, reg); err != nil {
		return fmt.Errorf("saving registry: %w", err)
	}

	if err := store.RemoveProject(storeDir, key, name, machineName); err != nil {
		return fmt.Errorf("removing project from store: %w", err)
	}

	if pushErr := store.Push(storeDir); pushErr != nil {
		warnOnPushError(pushErr, storeDir, out)
	}

	_, _ = fmt.Fprintf(out, "✓ Removed project %s from the store and registry\n", name)
	return nil
}

// resolveRemoveTarget identifies which registered project to remove.
//
// With an explicit argument it matches by key first, then by display name. A
// display-name match that is ambiguous (more than one project shares the name)
// is rejected with the candidate keys so the user can disambiguate by key. With
// no argument it detects the current project and looks up its key.
func resolveRemoveTarget(args []string, reg *registry.Registry) (string, *registry.Project, error) {
	if len(args) == 1 {
		arg := args[0]
		if p, ok := registry.GetProject(reg, arg); ok {
			return arg, p, nil
		}

		var matches []string
		for key, p := range reg.Projects {
			if p != nil && p.DisplayName == arg {
				matches = append(matches, key)
			}
		}
		sort.Strings(matches)
		switch len(matches) {
		case 0:
			return "", nil, fmt.Errorf("no such project %q in the registry", arg)
		case 1:
			return matches[0], reg.Projects[matches[0]], nil
		default:
			return "", nil, fmt.Errorf(
				"%q matches multiple projects — pass one of these keys instead: %s",
				arg, strings.Join(matches, ", "))
		}
	}

	proj, derr := project.Detect()
	if derr != nil {
		return "", nil, fmt.Errorf(
			"not inside a tracked project — cd into one or pass a project name/key (%w)", derr)
	}
	p, ok := registry.GetProject(reg, proj.Key)
	if !ok {
		return "", nil, fmt.Errorf(
			"the current project (key %s) is not tracked — nothing to remove", proj.Key)
	}
	return proj.Key, p, nil
}

func init() {
	removeCmd.Flags().BoolVar(&removeForce, "force", false, "Remove even if the project still has tracked files")
	removeCmd.Flags().BoolVar(&removeYes, "yes", false, "Skip confirmation prompt")
	rootCmd.AddCommand(removeCmd)
}

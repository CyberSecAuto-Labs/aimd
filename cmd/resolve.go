package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CyberSecAuto-Labs/aimd/internal/store"
)

var (
	resolveOurs   bool
	resolveTheirs bool
	resolveAbort  bool
)

var resolveCmd = &cobra.Command{
	Use:   "resolve [file]",
	Short: "Resolve a sync conflict in the private store",
	Long: `Resolve a rebase conflict left behind by a failed aimd sync.

When aimd sync rebases local store commits onto origin and a tracked overlay was
edited on two machines, the rebase stops with conflicts. aimd resolve drives the
resolution to completion and pushes the result.

Pass the conflicted file path exactly as aimd sync printed it (relative to the
store, e.g. repos/<project-key>/CLAUDE.md):

  aimd resolve repos/github.com~org~app/CLAUDE.md

By default the file is opened in $EDITOR (or $VISUAL); after the editor closes
aimd verifies no conflict markers remain, then runs git rebase --continue and
pushes. With no editor configured, aimd prints the path and instructions and you
re-run the same command once the markers are gone.

Shorthands skip the editor:
  --ours    keep the upstream version (origin/main — what was rebased onto)
  --theirs  keep your local commit's version being replayed
  (during a rebase these sides are inverted relative to a normal merge)

  --abort   abort the rebase and restore the store to its pre-sync state`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var file string
		if len(args) > 0 {
			file = args[0]
		}
		return RunResolve(storePath, file, resolveOurs, resolveTheirs, resolveAbort, dryRun, cmd.OutOrStdout())
	},
}

// RunResolve is the testable core of the resolve command.
//
// storeDir is the resolved path to ~/.aimd/store. fileArg is the conflicted path
// as printed by aimd sync (store-relative, or an absolute path under the store).
// ours/theirs select a side without opening an editor; abort aborts the rebase.
// dryRun prints what would happen without touching the store. out receives all
// user-facing output.
func RunResolve(storeDir, fileArg string, ours, theirs, abort, dryRun bool, out io.Writer) error {
	if err := verifyStore(storeDir); err != nil {
		return err
	}
	if ours && theirs {
		return fmt.Errorf("--ours and --theirs are mutually exclusive")
	}
	if !store.RebaseInProgress(storeDir) {
		return fmt.Errorf("no rebase in progress — nothing to resolve (run `aimd sync` first)")
	}

	if abort {
		return resolveAbortRebase(storeDir, dryRun, out)
	}

	relPath, absPath, err := resolveTarget(storeDir, fileArg)
	if err != nil {
		return err
	}

	if dryRun {
		return resolveDryRun(relPath, ours, theirs, out)
	}

	switch {
	case ours:
		if rerr := store.ResolveOurs(storeDir, relPath); rerr != nil {
			return fmt.Errorf("resolving with our version: %w", rerr)
		}
		_, _ = fmt.Fprintf(out, "resolved %s using our version\n", relPath)
	case theirs:
		if rerr := store.ResolveTheirs(storeDir, relPath); rerr != nil {
			return fmt.Errorf("resolving with their version: %w", rerr)
		}
		_, _ = fmt.Fprintf(out, "resolved %s using their version\n", relPath)
	default:
		done, eerr := resolveWithEditor(storeDir, relPath, absPath, out)
		if eerr != nil || !done {
			return eerr
		}
	}

	return continueAndPush(storeDir, out)
}

// resolveAbortRebase aborts the in-progress rebase (or reports the intent in
// dry-run mode).
func resolveAbortRebase(storeDir string, dryRun bool, out io.Writer) error {
	if dryRun {
		_, _ = fmt.Fprintln(out, "dry-run: would abort the in-progress rebase")
		return nil
	}
	if err := store.AbortRebase(storeDir); err != nil {
		return fmt.Errorf("aborting rebase: %w", err)
	}
	_, _ = fmt.Fprintln(out, "✓ Rebase aborted — store restored to its pre-sync (DIVERGED) state. Run `aimd sync` to retry.")
	return nil
}

// resolveTarget normalises fileArg to a (store-relative, absolute) path pair and
// guards against a path that escapes the store directory.
func resolveTarget(storeDir, fileArg string) (relPath, absPath string, err error) {
	if fileArg == "" {
		return "", "", fmt.Errorf("a conflicted file path is required (see the path printed by `aimd sync`)")
	}
	relPath = filepath.Clean(fileArg)
	if filepath.IsAbs(relPath) {
		rel, relErr := filepath.Rel(storeDir, relPath)
		if relErr != nil {
			return "", "", fmt.Errorf("resolving %s against the store path: %w", fileArg, relErr)
		}
		relPath = rel
	}
	if pathEscapesRoot(relPath) {
		return "", "", fmt.Errorf("refusing to resolve %q: path escapes the store directory", fileArg)
	}
	return relPath, filepath.Join(storeDir, relPath), nil
}

// resolveDryRun reports what resolve would do without modifying the store.
func resolveDryRun(relPath string, ours, theirs bool, out io.Writer) error {
	switch {
	case ours:
		_, _ = fmt.Fprintf(out, "dry-run: would resolve %s using our version, then continue the rebase\n", relPath)
	case theirs:
		_, _ = fmt.Fprintf(out, "dry-run: would resolve %s using their version, then continue the rebase\n", relPath)
	default:
		_, _ = fmt.Fprintf(out, "dry-run: would open %s in $EDITOR, then continue the rebase\n", relPath)
	}
	return nil
}

// resolveWithEditor opens the conflicted file in $EDITOR (or $VISUAL), then
// verifies no conflict markers remain and stages the file. It returns done=true
// when the file is staged and the caller should continue the rebase. It returns
// done=false with a nil error when no editor is configured and markers remain —
// the user is told to edit the file and re-run the command.
func resolveWithEditor(storeDir, relPath, absPath string, out io.Writer) (bool, error) {
	editor := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"))
	if editor != "" {
		if err := launchEditor(editor, absPath); err != nil {
			return false, fmt.Errorf("running editor %q: %w", editor, err)
		}
	}

	hasMarkers, err := store.HasConflictMarkers(absPath)
	if err != nil {
		return false, fmt.Errorf("checking %s for conflict markers: %w", relPath, err)
	}
	if hasMarkers {
		if editor == "" {
			_, _ = fmt.Fprintf(out,
				"No $EDITOR set. Edit the file to remove the conflict markers, then re-run `aimd resolve %s`:\n  %s\n",
				relPath, absPath)
			return false, nil
		}
		return false, fmt.Errorf("conflict markers remain in %s — resolve them, then run `aimd resolve %s` again", relPath, relPath)
	}

	if err := store.StageResolution(storeDir, relPath); err != nil {
		return false, fmt.Errorf("staging %s: %w", relPath, err)
	}
	return true, nil
}

// continueAndPush continues the rebase after a file has been staged and pushes on
// clean completion. Further conflicts are reported with per-file resolve hints.
func continueAndPush(storeDir string, out io.Writer) error {
	_, err := store.ContinueRebase(storeDir)

	var conflictErr *store.ConflictError
	if errors.As(err, &conflictErr) {
		_, _ = fmt.Fprintln(out, "further conflicts remain after continuing the rebase:")
		for _, f := range conflictErr.Files {
			_, _ = fmt.Fprintf(out, "  %s\n", f)
			_, _ = fmt.Fprintf(out, "  Run: aimd resolve %s\n", f)
		}
		return fmt.Errorf("rebase still has conflicts: %w", err)
	}
	if err != nil {
		return fmt.Errorf("continuing rebase: %w", err)
	}

	if pushErr := store.Push(storeDir); pushErr != nil {
		warnOnPushError(pushErr, storeDir, out)
		return nil
	}
	_, _ = fmt.Fprintln(out, "✓ Resolved and synced — store is up to date")
	return nil
}

// launchEditor runs the editor command (which may include arguments, e.g.
// "code --wait") on path, wiring the user's terminal through so they can edit
// interactively.
func launchEditor(editor, path string) error {
	fields := strings.Fields(editor)
	if len(fields) == 0 {
		return fmt.Errorf("empty editor command")
	}
	args := make([]string, 0, len(fields))
	args = append(args, fields[1:]...)
	args = append(args, path)
	c := exec.Command(fields[0], args...) //nolint:gosec // editor comes from the user's own $EDITOR/$VISUAL
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("editor exited with error: %w", err)
	}
	return nil
}

// firstNonEmpty returns the first non-empty string, or "" if all are empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func init() {
	resolveCmd.Flags().BoolVar(&resolveOurs, "ours", false, "Keep the upstream version (origin/main) without opening an editor")
	resolveCmd.Flags().BoolVar(&resolveTheirs, "theirs", false, "Keep your local commit's version without opening an editor")
	resolveCmd.Flags().BoolVar(&resolveAbort, "abort", false, "Abort the rebase and restore the store to its pre-sync state")
	rootCmd.AddCommand(resolveCmd)
}

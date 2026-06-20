package store

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// DetectState fetches origin and returns the current sync state.
// It does NOT modify the local working tree (fetch only, no pull).
func DetectState(storeDir string) (SyncState, error) {
	// A detached HEAD (e.g. an interrupted rebase) must not silently proceed to
	// a rebase onto a detached commit. Surface a clear error instead.
	if symErr := gitCmd("-C", storeDir, "symbolic-ref", "-q", "HEAD").Run(); symErr != nil {
		return StateUpToDate, fmt.Errorf("local HEAD is detached (no current branch); resolve the repository state before syncing")
	}

	// Fetch latest from origin without touching the working tree.
	fetchOut, err := gitCmd("-C", storeDir, "fetch", "origin", "main").CombinedOutput()
	if err != nil {
		// When the remote simply has no `main` ref yet (e.g. init's initial push
		// failed offline, or the remote default branch differs), the local store
		// is AHEAD: a subsequent push will create `main` from local commits.
		if remoteMissingMainRef(string(fetchOut)) {
			return StateAhead, nil
		}
		return StateUpToDate, fmt.Errorf("git fetch: %w — %s", err, strings.TrimSpace(string(fetchOut)))
	}

	return compareLocalRemote(storeDir)
}

// DetectStateOffline returns the current sync state WITHOUT contacting the
// remote: it compares local HEAD against the already-fetched origin/main
// remote-tracking ref. The caller is responsible for any prior fetch. This is
// the read-only variant used by `aimd status` (offline by default per the
// status command's design). It shares the post-fetch comparison logic with
// DetectState via compareLocalRemote.
func DetectStateOffline(storeDir string) (SyncState, error) {
	if symErr := gitCmd("-C", storeDir, "symbolic-ref", "-q", "HEAD").Run(); symErr != nil {
		return StateUpToDate, fmt.Errorf("local HEAD is detached (no current branch); resolve the repository state before syncing")
	}
	return compareLocalRemote(storeDir)
}

// compareLocalRemote resolves local HEAD and origin/main and classifies their
// relationship. It performs no network access — both DetectState (after a
// fetch) and DetectStateOffline (no fetch) call it for the comparison step.
func compareLocalRemote(storeDir string) (SyncState, error) {
	local, err := revParse(storeDir, "HEAD")
	if err != nil {
		return StateUpToDate, fmt.Errorf("resolve HEAD: %w", err)
	}

	remote, resolveErr := revParse(storeDir, "origin/main")
	if resolveErr != nil {
		// origin/main is unresolvable — the remote has no main branch (or it has
		// never been fetched). Treat as AHEAD so a later push creates it. The
		// resolve error is intentionally not propagated: a missing remote main is
		// an expected state here, not a failure.
		return StateAhead, nil //nolint:nilerr // missing remote main → AHEAD by design
	}

	if local == remote {
		return StateUpToDate, nil
	}

	// Is local an ancestor of remote? → BEHIND
	localAncestorOfRemote, err := isAncestor(storeDir, local, remote)
	if err != nil {
		return StateUpToDate, fmt.Errorf("ancestry check (local→remote): %w", err)
	}
	if localAncestorOfRemote {
		return StateBehind, nil
	}

	// Is remote an ancestor of local? → AHEAD
	remoteAncestorOfLocal, err := isAncestor(storeDir, remote, local)
	if err != nil {
		return StateUpToDate, fmt.Errorf("ancestry check (remote→local): %w", err)
	}
	if remoteAncestorOfLocal {
		return StateAhead, nil
	}

	return StateDiverged, nil
}

// remoteMissingMainRef reports whether a `git fetch origin main` failure was
// caused by the remote lacking a `main` ref (as opposed to a real transport or
// auth error). git output is forced to English (LC_ALL=C) via gitCmd, so the
// substring match is locale-stable.
func remoteMissingMainRef(fetchOutput string) bool {
	return strings.Contains(fetchOutput, "couldn't find remote ref")
}

// Sync brings the local store into sync with origin:
//   - UP_TO_DATE: returns (StateUpToDate, nil) immediately.
//   - BEHIND:     fast-forward pulls, returns (StateUpToDate, nil).
//   - AHEAD:      returns (StateAhead, nil) — caller must push.
//   - DIVERGED:   rebases; on clean rebase returns (StateAhead, nil);
//     on conflict returns (StateConflict, &ConflictError{Files: [...]}).
func Sync(storeDir string) (SyncState, error) {
	state, err := DetectState(storeDir)
	if err != nil {
		return state, err
	}

	switch state {
	case StateUpToDate:
		return StateUpToDate, nil

	case StateBehind:
		pullOut, pullErr := gitCmd(
			"-C", storeDir, "pull", "--ff-only", "origin", "main",
		).CombinedOutput()
		if pullErr != nil {
			return StateBehind, fmt.Errorf("git pull --ff-only: %w — %s", pullErr, strings.TrimSpace(string(pullOut)))
		}
		return StateUpToDate, nil

	case StateAhead:
		return StateAhead, nil

	case StateDiverged:
		rebaseArgs := append([]string{"-C", storeDir}, CommitIdentityArgs...)
		rebaseArgs = append(rebaseArgs, "pull", "--rebase", "origin", "main")
		rebaseOut, rebaseErr := gitCmd(rebaseArgs...).CombinedOutput()
		if rebaseErr == nil {
			return StateAhead, nil
		}

		// Rebase failed — detect conflicted files.
		files, conflictErr := conflictedFiles(storeDir)

		// Only treat this as a conflict when there are genuinely unmerged files.
		// A non-conflict failure (e.g. dirty worktree, or conflictedFiles itself
		// erroring) means the rebase failed for another reason: abort any started
		// rebase and surface the real git error rather than an empty ConflictError.
		if conflictErr == nil && len(files) > 0 {
			return StateConflict, &ConflictError{Files: files}
		}

		// Abort the rebase to leave the repo clean before returning the error.
		_ = gitCmd("-C", storeDir, "rebase", "--abort").Run()
		return StateConflict, fmt.Errorf("git pull --rebase: %w — %s", rebaseErr, strings.TrimSpace(string(rebaseOut)))

	default:
		return state, nil
	}
}

// Pull fast-forwards the store from origin/main and returns git's combined
// output. It is best-effort: callers decide how to handle a non-nil error
// (restore warns and continues from local state). Output is forced to English
// (LC_ALL=C) via gitCmd so any caller inspection is locale-stable.
func Pull(storeDir string) (string, error) {
	out, err := gitCmd("-C", storeDir, "pull", "--ff-only", "origin", "main").CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("git pull --ff-only: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// FetchDryRun reports whether the store's origin remote is reachable by running
// `git fetch --dry-run origin main`. It contacts the network to negotiate with
// the remote but writes nothing to the local repository, so it is safe to run
// as a read-only health probe. A nil error means the remote responded; a
// non-nil error wraps git's output (offline, auth failure, missing ref, …).
func FetchDryRun(storeDir string) error {
	out, err := gitCmd("-C", storeDir, "fetch", "--dry-run", "origin", "main").CombinedOutput()
	if err != nil {
		return fmt.Errorf("git fetch --dry-run: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// revParse returns the full SHA for the given ref.
func revParse(storeDir, ref string) (string, error) {
	out, err := gitCmd("-C", storeDir, "rev-parse", ref).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w — %s", ref, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// isAncestor reports whether commitA is an ancestor of commitB using
// `git merge-base --is-ancestor`. It returns (false, nil) when A is not
// an ancestor; a non-nil error is returned only for unexpected failures.
func isAncestor(storeDir, commitA, commitB string) (bool, error) {
	cmd := gitCmd("-C", storeDir, "merge-base", "--is-ancestor", commitA, commitB)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		// Exit code 1 means "not an ancestor" — not a true error.
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor: %w", err)
}

// conflictedFiles returns the list of paths with unmerged changes after
// a failed rebase.
func conflictedFiles(storeDir string) ([]string, error) {
	out, err := gitCmd(
		"-C", storeDir, "diff", "--name-only", "--diff-filter=U",
	).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git diff --name-only --diff-filter=U: %w — %s", err, strings.TrimSpace(string(out)))
	}

	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

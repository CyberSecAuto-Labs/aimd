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
	// Fetch latest from origin without touching the working tree.
	fetchOut, err := exec.Command("git", "-C", storeDir, "fetch", "origin", "main").CombinedOutput()
	if err != nil {
		return StateUpToDate, fmt.Errorf("git fetch: %w — %s", err, strings.TrimSpace(string(fetchOut)))
	}

	local, err := revParse(storeDir, "HEAD")
	if err != nil {
		return StateUpToDate, fmt.Errorf("resolve HEAD: %w", err)
	}

	remote, err := revParse(storeDir, "origin/main")
	if err != nil {
		return StateUpToDate, fmt.Errorf("resolve origin/main: %w", err)
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
		pullOut, pullErr := exec.Command(
			"git", "-C", storeDir, "pull", "--ff-only", "origin", "main",
		).CombinedOutput()
		if pullErr != nil {
			return StateBehind, fmt.Errorf("git pull --ff-only: %w — %s", pullErr, strings.TrimSpace(string(pullOut)))
		}
		return StateUpToDate, nil

	case StateAhead:
		return StateAhead, nil

	case StateDiverged:
		rebaseOut, rebaseErr := exec.Command(
			"git", "-C", storeDir,
			"-c", "user.email=aimd@localhost",
			"-c", "user.name=aimd",
			"pull", "--rebase", "origin", "main",
		).CombinedOutput()
		if rebaseErr == nil {
			return StateAhead, nil
		}

		// Rebase failed — detect conflicted files.
		files, conflictErr := conflictedFiles(storeDir)
		if conflictErr != nil {
			// Abort the rebase to leave the repo clean before returning the error.
			_ = exec.Command("git", "-C", storeDir, "rebase", "--abort").Run()
			return StateConflict, fmt.Errorf("git pull --rebase: %w — %s", rebaseErr, strings.TrimSpace(string(rebaseOut)))
		}

		return StateConflict, &ConflictError{Files: files}

	default:
		return state, nil
	}
}

// revParse returns the full SHA for the given ref.
func revParse(storeDir, ref string) (string, error) {
	out, err := exec.Command("git", "-C", storeDir, "rev-parse", ref).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w — %s", ref, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// isAncestor reports whether commitA is an ancestor of commitB using
// `git merge-base --is-ancestor`. It returns (false, nil) when A is not
// an ancestor; a non-nil error is returned only for unexpected failures.
func isAncestor(storeDir, commitA, commitB string) (bool, error) {
	cmd := exec.Command("git", "-C", storeDir, "merge-base", "--is-ancestor", commitA, commitB)
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
	out, err := exec.Command(
		"git", "-C", storeDir, "diff", "--name-only", "--diff-filter=U",
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

package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RebaseInProgress reports whether a rebase is currently in progress in storeDir.
// A rebase leaves a `.git/rebase-merge` or `.git/rebase-apply` directory behind
// until it is completed or aborted; aimd resolve only operates while one exists.
func RebaseInProgress(storeDir string) bool {
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(storeDir, ".git", d)); err == nil {
			return true
		}
	}
	return false
}

// HasConflictMarkers reports whether the file at path still contains git conflict
// markers. It requires both a start (`<<<<<<< `) and an end (`>>>>>>> `) marker so
// that ordinary text — including markdown setext headings that use a run of `=`
// — never produces a false positive.
func HasConflictMarkers(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	return containsConflictMarkers(string(data)), nil
}

func containsConflictMarkers(content string) bool {
	var hasStart, hasEnd bool
	for _, line := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(line, "<<<<<<< "):
			hasStart = true
		case strings.HasPrefix(line, ">>>>>>> "):
			hasEnd = true
		}
	}
	return hasStart && hasEnd
}

// StageResolution stages a resolved file (`git add -- <relPath>`) so the rebase
// can continue. relPath is relative to storeDir.
func StageResolution(storeDir, relPath string) error {
	out, err := gitCmd("-C", storeDir, "add", "--", relPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git add %s: %w — %s", relPath, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ResolveOurs resolves a conflicted file by taking "our" side, then stages it.
//
// During a rebase the sides are inverted relative to a merge: "ours" is the
// branch being rebased onto (origin/main, the upstream version) and "theirs" is
// the local commit being replayed. This matches raw `git checkout --ours`
// behaviour, and the command help documents the rebase semantics for the user.
func ResolveOurs(storeDir, relPath string) error {
	return resolveSide(storeDir, relPath, "ours")
}

// ResolveTheirs resolves a conflicted file by taking "their" side (the local
// commit being replayed during the rebase), then stages it. See ResolveOurs for
// the rebase-inverted side semantics.
func ResolveTheirs(storeDir, relPath string) error {
	return resolveSide(storeDir, relPath, "theirs")
}

func resolveSide(storeDir, relPath, side string) error {
	out, err := gitCmd("-C", storeDir, "checkout", "--"+side, "--", relPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git checkout --%s %s: %w — %s", side, relPath, err, strings.TrimSpace(string(out)))
	}
	return StageResolution(storeDir, relPath)
}

// ContinueRebase runs `git rebase --continue` with a no-op editor (so it never
// blocks waiting for a commit message) and reports the resulting state:
//
//   - clean completion → (StateAhead, nil): the local commits now sit on top of
//     origin/main and the caller should push.
//   - a further conflict at the next replayed commit → (StateConflict,
//     *ConflictError) listing the newly conflicted files.
//   - any other failure → (StateConflict, error) with the real git output.
func ContinueRebase(storeDir string) (SyncState, error) {
	cmd := gitCmd(
		"-C", storeDir,
		"-c", "user.email=aimd@localhost",
		"-c", "user.name=aimd",
		"-c", "commit.gpgsign=false",
		"rebase", "--continue",
	)
	cmd.Env = append(cmd.Env, "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return StateAhead, nil
	}

	files, conflictErr := conflictedFiles(storeDir)
	if conflictErr == nil && len(files) > 0 {
		return StateConflict, &ConflictError{Files: files}
	}

	return StateConflict, fmt.Errorf("git rebase --continue: %w — %s", err, strings.TrimSpace(string(out)))
}

// AbortRebase runs `git rebase --abort`, restoring the store to its pre-rebase
// (DIVERGED) state.
func AbortRebase(storeDir string) error {
	out, err := gitCmd("-C", storeDir, "rebase", "--abort").CombinedOutput()
	if err != nil {
		return fmt.Errorf("git rebase --abort: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

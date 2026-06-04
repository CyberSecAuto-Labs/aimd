package store_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/internal/store"
)

// setupBareWithClone creates a bare origin pre-seeded with one commit,
// then clones it. Returns (bareDir, cloneDir).
func setupBareWithClone(t *testing.T) (bareDir, cloneDir string) {
	t.Helper()

	bareDir = t.TempDir()
	initGitRepo(t, bareDir, true)

	// Seed the bare repo via a throwaway working copy.
	seedDir := t.TempDir()
	initGitRepo(t, seedDir, false)
	gitRun(t, seedDir, "config", "user.email", "test@test.com")
	gitRun(t, seedDir, "config", "user.name", "test")
	addCommitFile(t, seedDir, "init.txt", "init")
	gitRun(t, seedDir, "remote", "add", "origin", bareDir)
	gitRun(t, seedDir, "push", "origin", "HEAD:main")

	cloneDir = t.TempDir()
	cloneOut, err := exec.Command("git", "clone", bareDir, cloneDir).CombinedOutput()
	if err != nil {
		t.Fatalf("git clone: %v — %s", err, cloneOut)
	}
	gitRun(t, cloneDir, "config", "user.email", "test@test.com")
	gitRun(t, cloneDir, "config", "user.name", "test")
	return bareDir, cloneDir
}

// addCommitFile creates filename with content in dir and commits it.
func addCommitFile(t *testing.T, dir, filename, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
	gitRun(t, dir, "add", filename)
	gitRun(t, dir, "-c", "user.email=aimd@localhost", "-c", "user.name=aimd",
		"commit", "-m", "add "+filename)
}

// ── DetectState tests ────────────────────────────────────────────────────────

func TestDetectStateUpToDate(t *testing.T) {
	_, cloneDir := setupBareWithClone(t)

	state, err := store.DetectState(cloneDir)
	if err != nil {
		t.Fatalf("DetectState: %v", err)
	}
	if state != store.StateUpToDate {
		t.Errorf("want StateUpToDate, got %v", state)
	}
}

func TestDetectStateBehind(t *testing.T) {
	bareDir, cloneDir := setupBareWithClone(t)

	// Add a commit directly in a second clone and push it to origin, so the
	// original clone doesn't have it yet.
	pusherDir := t.TempDir()
	cloneOut, err := exec.Command("git", "clone", bareDir, pusherDir).CombinedOutput()
	if err != nil {
		t.Fatalf("git clone pusher: %v — %s", err, cloneOut)
	}
	gitRun(t, pusherDir, "config", "user.email", "test@test.com")
	gitRun(t, pusherDir, "config", "user.name", "test")
	addCommitFile(t, pusherDir, "remote.txt", "from remote")
	gitRun(t, pusherDir, "push", "origin", "HEAD:main")

	state, err := store.DetectState(cloneDir)
	if err != nil {
		t.Fatalf("DetectState: %v", err)
	}
	if state != store.StateBehind {
		t.Errorf("want StateBehind, got %v", state)
	}
}

func TestDetectStateAhead(t *testing.T) {
	_, cloneDir := setupBareWithClone(t)

	// Add a local commit that hasn't been pushed.
	addCommitFile(t, cloneDir, "local.txt", "local only")

	state, err := store.DetectState(cloneDir)
	if err != nil {
		t.Fatalf("DetectState: %v", err)
	}
	if state != store.StateAhead {
		t.Errorf("want StateAhead, got %v", state)
	}
}

func TestDetectStateDiverged(t *testing.T) {
	bareDir, cloneDir := setupBareWithClone(t)

	// Push a commit from another clone so origin/main advances.
	pusherDir := t.TempDir()
	cloneOut, err := exec.Command("git", "clone", bareDir, pusherDir).CombinedOutput()
	if err != nil {
		t.Fatalf("git clone pusher: %v — %s", err, cloneOut)
	}
	gitRun(t, pusherDir, "config", "user.email", "test@test.com")
	gitRun(t, pusherDir, "config", "user.name", "test")
	addCommitFile(t, pusherDir, "remote.txt", "remote side")
	gitRun(t, pusherDir, "push", "origin", "HEAD:main")

	// Also add a local commit that origin doesn't have.
	addCommitFile(t, cloneDir, "local.txt", "local side")

	state, err := store.DetectState(cloneDir)
	if err != nil {
		t.Fatalf("DetectState: %v", err)
	}
	if state != store.StateDiverged {
		t.Errorf("want StateDiverged, got %v", state)
	}
}

// ── Sync tests ───────────────────────────────────────────────────────────────

func TestSyncBehind(t *testing.T) {
	bareDir, cloneDir := setupBareWithClone(t)

	// Push a commit from another clone so cloneDir is behind.
	pusherDir := t.TempDir()
	cloneOut, err := exec.Command("git", "clone", bareDir, pusherDir).CombinedOutput()
	if err != nil {
		t.Fatalf("git clone pusher: %v — %s", err, cloneOut)
	}
	gitRun(t, pusherDir, "config", "user.email", "test@test.com")
	gitRun(t, pusherDir, "config", "user.name", "test")
	addCommitFile(t, pusherDir, "remote.txt", "from remote")
	gitRun(t, pusherDir, "push", "origin", "HEAD:main")

	// Record local HEAD before sync.
	localBefore := gitRun(t, cloneDir, "rev-parse", "HEAD")

	state, err := store.Sync(cloneDir)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if state != store.StateUpToDate {
		t.Errorf("want StateUpToDate after Sync, got %v", state)
	}

	// Local HEAD should have advanced.
	localAfter := gitRun(t, cloneDir, "rev-parse", "HEAD")
	if localBefore == localAfter {
		t.Error("local HEAD did not advance after Sync on BEHIND state")
	}

	// Verify the pulled file exists.
	if _, statErr := os.Stat(filepath.Join(cloneDir, "remote.txt")); statErr != nil {
		t.Errorf("remote.txt not present after Sync: %v", statErr)
	}
}

func TestPullFastForwards(t *testing.T) {
	bareDir, cloneDir := setupBareWithClone(t)

	// Push a commit from another clone so cloneDir is behind.
	pusherDir := t.TempDir()
	cloneOut, err := exec.Command("git", "clone", bareDir, pusherDir).CombinedOutput()
	if err != nil {
		t.Fatalf("git clone pusher: %v — %s", err, cloneOut)
	}
	gitRun(t, pusherDir, "config", "user.email", "test@test.com")
	gitRun(t, pusherDir, "config", "user.name", "test")
	addCommitFile(t, pusherDir, "remote.txt", "from remote")
	gitRun(t, pusherDir, "push", "origin", "HEAD:main")

	if _, err := store.Pull(cloneDir); err != nil {
		t.Fatalf("store.Pull: %v", err)
	}

	// The pulled file should now be present in the worktree.
	if _, statErr := os.Stat(filepath.Join(cloneDir, "remote.txt")); statErr != nil {
		t.Errorf("remote.txt not present after Pull: %v", statErr)
	}
}

func TestSyncDiverged(t *testing.T) {
	bareDir, cloneDir := setupBareWithClone(t)

	// Push a commit from another clone so origin/main diverges.
	pusherDir := t.TempDir()
	cloneOut, err := exec.Command("git", "clone", bareDir, pusherDir).CombinedOutput()
	if err != nil {
		t.Fatalf("git clone pusher: %v — %s", err, cloneOut)
	}
	gitRun(t, pusherDir, "config", "user.email", "test@test.com")
	gitRun(t, pusherDir, "config", "user.name", "test")
	addCommitFile(t, pusherDir, "remote.txt", "remote side")
	gitRun(t, pusherDir, "push", "origin", "HEAD:main")

	// Add a local-only commit (different file — no conflict).
	addCommitFile(t, cloneDir, "local.txt", "local side")

	state, err := store.Sync(cloneDir)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if state != store.StateAhead {
		t.Errorf("want StateAhead after clean rebase, got %v", state)
	}

	// Both files should exist after the rebase.
	for _, name := range []string{"remote.txt", "local.txt"} {
		if _, statErr := os.Stat(filepath.Join(cloneDir, name)); statErr != nil {
			t.Errorf("%s not present after clean rebase: %v", name, statErr)
		}
	}
}

// when `git pull --rebase` fails for a non-conflict reason (dirty
// worktree), Sync must NOT return a *ConflictError. It must surface the real
// git error and leave no rebase in progress.
func TestSyncDivergedDirtyWorktreeReturnsRealError(t *testing.T) {
	bareDir, cloneDir := setupBareWithClone(t)

	// Advance origin/main from another clone.
	pusherDir := t.TempDir()
	cloneOut, err := exec.Command("git", "clone", bareDir, pusherDir).CombinedOutput()
	if err != nil {
		t.Fatalf("git clone pusher: %v — %s", err, cloneOut)
	}
	gitRun(t, pusherDir, "config", "user.email", "test@test.com")
	gitRun(t, pusherDir, "config", "user.name", "test")
	addCommitFile(t, pusherDir, "remote.txt", "remote side")
	gitRun(t, pusherDir, "push", "origin", "HEAD:main")

	// Add a local-only commit so the state is DIVERGED.
	addCommitFile(t, cloneDir, "local.txt", "local side")

	// Dirty the worktree with uncommitted changes so `git pull --rebase` refuses
	// to start (no conflict — a pre-rebase failure).
	if err := os.WriteFile(filepath.Join(cloneDir, "local.txt"), []byte("dirty uncommitted"), 0o600); err != nil {
		t.Fatalf("dirty worktree: %v", err)
	}

	state, err := store.Sync(cloneDir)
	if err == nil {
		t.Fatal("Sync: expected error for dirty-worktree rebase failure, got nil")
	}

	// Must NOT be a ConflictError.
	var conflictErr *store.ConflictError
	if errors.As(err, &conflictErr) {
		t.Errorf("expected a real git error, got *ConflictError: %v", err)
	}
	if state != store.StateConflict && state != store.StateDiverged {
		// We tolerate either signalling state, but the key is the error type.
		t.Logf("returned state %v", state)
	}

	// No rebase should be left in progress.
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if _, statErr := os.Stat(filepath.Join(cloneDir, ".git", d)); statErr == nil {
			t.Errorf("rebase left in progress: .git/%s exists", d)
		}
	}
}

// when origin has no `main` ref, DetectState must return
// StateAhead (local commits will create main on the next push) instead of a
// hard error.
func TestDetectStateRemoteHasNoMain(t *testing.T) {
	bareDir := t.TempDir()
	initGitRepo(t, bareDir, true) // empty bare repo: no main ref

	cloneDir := t.TempDir()
	initGitRepo(t, cloneDir, false)
	gitRun(t, cloneDir, "config", "user.email", "test@test.com")
	gitRun(t, cloneDir, "config", "user.name", "test")
	addCommitFile(t, cloneDir, "local.txt", "local only")
	gitRun(t, cloneDir, "remote", "add", "origin", bareDir)

	state, err := store.DetectState(cloneDir)
	if err != nil {
		t.Fatalf("DetectState: expected nil error when remote has no main, got: %v", err)
	}
	if state != store.StateAhead {
		t.Errorf("want StateAhead when remote has no main, got %v", state)
	}
}

// a detached HEAD must yield a clear error, not a silent rebase
// onto a detached commit.
func TestDetectStateDetachedHEAD(t *testing.T) {
	bareDir, cloneDir := setupBareWithClone(t)
	_ = bareDir

	// Detach HEAD at the current commit.
	head := strings.TrimSpace(gitRun(t, cloneDir, "rev-parse", "HEAD"))
	gitRun(t, cloneDir, "checkout", "--detach", head)

	_, err := store.DetectState(cloneDir)
	if err == nil {
		t.Fatal("DetectState: expected an error for detached HEAD, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "detached") {
		t.Errorf("expected detached-HEAD error, got: %v", err)
	}
}

func TestSyncConflict(t *testing.T) {
	bareDir, cloneDir := setupBareWithClone(t)

	// Push a conflicting change from another clone.
	pusherDir := t.TempDir()
	cloneOut, err := exec.Command("git", "clone", bareDir, pusherDir).CombinedOutput()
	if err != nil {
		t.Fatalf("git clone pusher: %v — %s", err, cloneOut)
	}
	gitRun(t, pusherDir, "config", "user.email", "test@test.com")
	gitRun(t, pusherDir, "config", "user.name", "test")
	addCommitFile(t, pusherDir, "conflict.txt", "remote version\n")
	gitRun(t, pusherDir, "push", "origin", "HEAD:main")

	// Local clone also edits the same file — will conflict on rebase.
	addCommitFile(t, cloneDir, "conflict.txt", "local version\n")

	state, err := store.Sync(cloneDir)
	if err == nil {
		t.Fatal("Sync: expected an error for conflict, got nil")
	}
	if state != store.StateConflict {
		t.Errorf("want StateConflict, got %v", state)
	}

	var conflictErr *store.ConflictError
	if !errors.As(err, &conflictErr) {
		t.Errorf("want *store.ConflictError, got %T: %v", err, err)
	} else {
		found := false
		for _, f := range conflictErr.Files {
			if f == "conflict.txt" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected conflict.txt in ConflictError.Files, got %v", conflictErr.Files)
		}
	}
}

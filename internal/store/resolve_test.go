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

// setupConflict creates a DIVERGED store where local and remote edited the same
// file, runs store.Sync to start (and stall) the rebase, and returns the clone
// directory left with an in-progress conflicted rebase plus the conflicted file.
func setupConflict(t *testing.T) (cloneDir, conflictFile string) {
	t.Helper()

	bareDir, cloneDir := setupBareWithClone(t)

	// Remote edits conflict.txt.
	pusherDir := t.TempDir()
	if out, err := exec.Command("git", "clone", bareDir, pusherDir).CombinedOutput(); err != nil {
		t.Fatalf("git clone pusher: %v — %s", err, out)
	}
	gitRun(t, pusherDir, "config", "user.email", "test@test.com")
	gitRun(t, pusherDir, "config", "user.name", "test")
	addCommitFile(t, pusherDir, "conflict.txt", "remote version\n")
	gitRun(t, pusherDir, "push", "origin", "HEAD:main")

	// Local edits the same file differently.
	addCommitFile(t, cloneDir, "conflict.txt", "local version\n")

	state, err := store.Sync(cloneDir)
	if err == nil {
		t.Fatal("setupConflict: expected a conflict from Sync, got nil error")
	}
	if state != store.StateConflict {
		t.Fatalf("setupConflict: want StateConflict, got %v", state)
	}
	if !store.RebaseInProgress(cloneDir) {
		t.Fatal("setupConflict: expected a rebase to be in progress")
	}
	return cloneDir, "conflict.txt"
}

// setupModifyDelete creates a modify/delete rebase conflict: a shared base file
// is modified on the remote and deleted locally. store.Sync stalls the rebase
// with HEAD's (remote) modified version left in the tree and no markers. Returns
// the clone directory and the conflicted file name.
func setupModifyDelete(t *testing.T) (cloneDir, conflictFile string) {
	t.Helper()

	bareDir, cloneDir := setupBareWithClone(t)
	conflictFile = "md.txt"

	// Shared base containing the file.
	addCommitFile(t, cloneDir, conflictFile, "base\n")
	gitRun(t, cloneDir, "push", "origin", "HEAD:main")

	// Remote modifies the file.
	pusherDir := t.TempDir()
	if out, err := exec.Command("git", "clone", bareDir, pusherDir).CombinedOutput(); err != nil {
		t.Fatalf("git clone pusher: %v — %s", err, out)
	}
	gitRun(t, pusherDir, "config", "user.email", "test@test.com")
	gitRun(t, pusherDir, "config", "user.name", "test")
	addCommitFile(t, pusherDir, conflictFile, "remote modified\n")
	gitRun(t, pusherDir, "push", "origin", "HEAD:main")

	// Local deletes the file.
	gitRun(t, cloneDir, "rm", conflictFile)
	gitRun(t, cloneDir, "-c", "user.email=aimd-bot@cybersecauto-labs.org", "-c", "user.name=aimd-bot",
		"commit", "-m", "local deletes "+conflictFile)

	state, err := store.Sync(cloneDir)
	if err == nil || state != store.StateConflict {
		t.Fatalf("setupModifyDelete: want StateConflict, got state=%v err=%v", state, err)
	}
	return cloneDir, conflictFile
}

func TestUnmergedSides(t *testing.T) {
	// Content conflict: both sides present.
	contentClone, contentFile := setupConflict(t)
	ours, theirs, err := store.UnmergedSides(contentClone, contentFile)
	if err != nil {
		t.Fatalf("UnmergedSides (content): %v", err)
	}
	if !ours || !theirs {
		t.Errorf("content conflict should have both sides, got ours=%v theirs=%v", ours, theirs)
	}

	// Modify/delete: exactly one side (the modified remote = ours during rebase).
	mdClone, mdFile := setupModifyDelete(t)
	ours, theirs, err = store.UnmergedSides(mdClone, mdFile)
	if err != nil {
		t.Fatalf("UnmergedSides (modify/delete): %v", err)
	}
	if !ours || theirs {
		t.Errorf("modify/delete (remote modified) should have only ours, got ours=%v theirs=%v", ours, theirs)
	}

	// A clean, non-unmerged path: neither side.
	_, cleanClone := setupBareWithClone(t)
	ours, theirs, err = store.UnmergedSides(cleanClone, "init.txt")
	if err != nil {
		t.Fatalf("UnmergedSides (clean): %v", err)
	}
	if ours || theirs {
		t.Errorf("clean path should report no sides, got ours=%v theirs=%v", ours, theirs)
	}
}

func TestResolveTheirsRemovesDeletedSide(t *testing.T) {
	// Local deleted the file; --keep-local (theirs) must resolve by removing it,
	// not error because the theirs side has no content to check out.
	cloneDir, mdFile := setupModifyDelete(t)

	if err := store.ResolveTheirs(cloneDir, mdFile); err != nil {
		t.Fatalf("ResolveTheirs on modify/delete: %v", err)
	}
	state, err := store.ContinueRebase(cloneDir)
	if err != nil {
		t.Fatalf("ContinueRebase: %v", err)
	}
	if state != store.StateAhead {
		t.Errorf("want StateAhead after resolution, got %v", state)
	}
	if _, statErr := os.Stat(filepath.Join(cloneDir, mdFile)); !os.IsNotExist(statErr) {
		t.Errorf("file should be removed after taking the deleting side, stat err = %v", statErr)
	}
}

func TestResolveOursKeepsModifiedSide(t *testing.T) {
	// Remote modified the file; --keep-remote (ours) keeps that modified version.
	cloneDir, mdFile := setupModifyDelete(t)

	if err := store.ResolveOurs(cloneDir, mdFile); err != nil {
		t.Fatalf("ResolveOurs on modify/delete: %v", err)
	}
	if _, err := store.ContinueRebase(cloneDir); err != nil {
		t.Fatalf("ContinueRebase: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(cloneDir, mdFile))
	if err != nil {
		t.Fatalf("file should exist after keeping the modified side: %v", err)
	}
	if string(got) != "remote modified\n" {
		t.Errorf("want remote-modified content, got %q", string(got))
	}
}

func TestRebaseInProgress(t *testing.T) {
	_, cloneDir := setupBareWithClone(t)
	if store.RebaseInProgress(cloneDir) {
		t.Error("RebaseInProgress: want false for a clean clone")
	}

	conflictClone, _ := setupConflict(t)
	if !store.RebaseInProgress(conflictClone) {
		t.Error("RebaseInProgress: want true during a stalled rebase")
	}
}

func TestHasConflictMarkers(t *testing.T) {
	dir := t.TempDir()

	conflicted := filepath.Join(dir, "conflicted.md")
	body := "intro\n<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> abc123\nrest\n"
	if err := os.WriteFile(conflicted, []byte(body), 0o600); err != nil {
		t.Fatalf("write conflicted: %v", err)
	}
	has, err := store.HasConflictMarkers(conflicted)
	if err != nil {
		t.Fatalf("HasConflictMarkers: %v", err)
	}
	if !has {
		t.Error("want markers detected in conflicted file")
	}

	// A markdown setext heading uses a run of '=' but is NOT a conflict.
	clean := filepath.Join(dir, "clean.md")
	if err := os.WriteFile(clean, []byte("Title\n=======\n\nbody\n"), 0o600); err != nil {
		t.Fatalf("write clean: %v", err)
	}
	has, err = store.HasConflictMarkers(clean)
	if err != nil {
		t.Fatalf("HasConflictMarkers clean: %v", err)
	}
	if has {
		t.Error("setext heading must not be reported as a conflict marker")
	}
}

func TestHasConflictMarkersMissingFile(t *testing.T) {
	_, err := store.HasConflictMarkers(filepath.Join(t.TempDir(), "nope.md"))
	if err == nil {
		t.Fatal("HasConflictMarkers: want error for a missing file")
	}
}

func TestResolveOursContinuesAndCompletes(t *testing.T) {
	cloneDir, conflictFile := setupConflict(t)

	if err := store.ResolveOurs(cloneDir, conflictFile); err != nil {
		t.Fatalf("ResolveOurs: %v", err)
	}

	state, err := store.ContinueRebase(cloneDir)
	if err != nil {
		t.Fatalf("ContinueRebase: %v", err)
	}
	if state != store.StateAhead {
		t.Errorf("want StateAhead after clean continue, got %v", state)
	}
	if store.RebaseInProgress(cloneDir) {
		t.Error("rebase should be finished after a successful continue")
	}

	// "ours" during a rebase keeps the upstream (origin) version.
	got, err := os.ReadFile(filepath.Join(cloneDir, conflictFile))
	if err != nil {
		t.Fatalf("read resolved file: %v", err)
	}
	if string(got) != "remote version\n" {
		t.Errorf("ResolveOurs: want upstream content %q, got %q", "remote version\n", string(got))
	}
}

func TestResolveTheirsKeepsLocalVersion(t *testing.T) {
	cloneDir, conflictFile := setupConflict(t)

	if err := store.ResolveTheirs(cloneDir, conflictFile); err != nil {
		t.Fatalf("ResolveTheirs: %v", err)
	}
	if _, err := store.ContinueRebase(cloneDir); err != nil {
		t.Fatalf("ContinueRebase: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(cloneDir, conflictFile))
	if err != nil {
		t.Fatalf("read resolved file: %v", err)
	}
	// "theirs" during a rebase keeps the local commit being replayed.
	if string(got) != "local version\n" {
		t.Errorf("ResolveTheirs: want local content %q, got %q", "local version\n", string(got))
	}
}

func TestStageResolutionThenContinueWithManualEdit(t *testing.T) {
	cloneDir, conflictFile := setupConflict(t)

	// Simulate a hand-resolved file (markers removed).
	if err := os.WriteFile(filepath.Join(cloneDir, conflictFile), []byte("merged by hand\n"), 0o600); err != nil {
		t.Fatalf("manual edit: %v", err)
	}
	if err := store.StageResolution(cloneDir, conflictFile); err != nil {
		t.Fatalf("StageResolution: %v", err)
	}
	state, err := store.ContinueRebase(cloneDir)
	if err != nil {
		t.Fatalf("ContinueRebase: %v", err)
	}
	if state != store.StateAhead {
		t.Errorf("want StateAhead, got %v", state)
	}
}

func TestAbortRebaseRestoresLocalHead(t *testing.T) {
	cloneDir, _ := setupConflict(t)

	if err := store.AbortRebase(cloneDir); err != nil {
		t.Fatalf("AbortRebase: %v", err)
	}
	if store.RebaseInProgress(cloneDir) {
		t.Error("rebase should not be in progress after abort")
	}

	// The pre-rebase local commit (its conflicting content) is restored.
	got, err := os.ReadFile(filepath.Join(cloneDir, "conflict.txt"))
	if err != nil {
		t.Fatalf("read file after abort: %v", err)
	}
	if string(got) != "local version\n" {
		t.Errorf("after abort want local content restored, got %q", string(got))
	}
}

func TestContinueRebaseReportsFurtherConflict(t *testing.T) {
	bareDir, cloneDir := setupBareWithClone(t)

	// Remote adds two conflicting commits on two different files.
	pusherDir := t.TempDir()
	if out, err := exec.Command("git", "clone", bareDir, pusherDir).CombinedOutput(); err != nil {
		t.Fatalf("git clone pusher: %v — %s", err, out)
	}
	gitRun(t, pusherDir, "config", "user.email", "test@test.com")
	gitRun(t, pusherDir, "config", "user.name", "test")
	addCommitFile(t, pusherDir, "a.txt", "remote a\n")
	addCommitFile(t, pusherDir, "b.txt", "remote b\n")
	gitRun(t, pusherDir, "push", "origin", "HEAD:main")

	// Local creates the same two files with different content across two commits,
	// so the rebase stalls twice.
	addCommitFile(t, cloneDir, "a.txt", "local a\n")
	addCommitFile(t, cloneDir, "b.txt", "local b\n")

	state, err := store.Sync(cloneDir)
	if state != store.StateConflict || err == nil {
		t.Fatalf("want StateConflict from Sync, got state=%v err=%v", state, err)
	}

	// Resolve the first conflict, then continuing should surface the second.
	if rerr := store.ResolveOurs(cloneDir, "a.txt"); rerr != nil {
		t.Fatalf("ResolveOurs a.txt: %v", rerr)
	}
	state, err = store.ContinueRebase(cloneDir)
	if state != store.StateConflict {
		t.Fatalf("want StateConflict after continue surfaces second conflict, got %v", state)
	}
	var conflictErr *store.ConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("want *store.ConflictError, got %T: %v", err, err)
	}
	if !strings.Contains(strings.Join(conflictErr.Files, ","), "b.txt") {
		t.Errorf("expected b.txt in further-conflict list, got %v", conflictErr.Files)
	}
}

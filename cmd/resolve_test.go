package cmd_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/cmd"
)

// setupResolveConflict builds a store whose overlay was edited on both this
// machine and the remote, runs aimd sync to start (and stall) the rebase, and
// returns the store clone, the bare origin, and the conflicted store-relative
// path. The store is left with an in-progress conflicted rebase.
func setupResolveConflict(t *testing.T) (cloneDir, bareDir, conflictRel string) {
	t.Helper()

	bareDir, cloneDir = setupSyncBareWithClone(t)
	conflictRel = filepath.Join("repos", "test-proj", "CLAUDE.md")

	// Remote edits the overlay and pushes.
	pusherDir := t.TempDir()
	if out, err := exec.Command("git", "clone", bareDir, pusherDir).CombinedOutput(); err != nil {
		t.Fatalf("git clone pusher: %v — %s", err, out)
	}
	syncGitRun(t, pusherDir, "config", "user.email", "test@test.com")
	syncGitRun(t, pusherDir, "config", "user.name", "test")
	writeOverlay(t, pusherDir, conflictRel, "remote version\n")
	syncGitRun(t, pusherDir, "add", conflictRel)
	syncGitRun(t, pusherDir, "-c", "user.email=aimd@localhost", "-c", "user.name=aimd",
		"commit", "-m", "remote overlay")
	syncGitRun(t, pusherDir, "push", "origin", "HEAD:main")

	// Local edits the same overlay differently and commits.
	writeOverlay(t, cloneDir, conflictRel, "local version\n")
	syncGitRun(t, cloneDir, "add", conflictRel)
	syncGitRun(t, cloneDir, "-c", "user.email=aimd@localhost", "-c", "user.name=aimd",
		"commit", "-m", "local overlay")

	localPath := t.TempDir()
	seedRegistry(t, cloneDir, "test-proj", localPath, []string{"CLAUDE.md"})

	// Sync should diverge, rebase, and stall on the conflict.
	var out bytes.Buffer
	if err := cmd.RunSync(cloneDir, "test-machine", true, false, &out); err == nil {
		t.Fatalf("setupResolveConflict: expected a conflict from RunSync, got nil\n%s", out.String())
	}
	return cloneDir, bareDir, conflictRel
}

func writeOverlay(t *testing.T, dir, rel, content string) {
	t.Helper()
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir overlay dir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
		t.Fatalf("write overlay %s: %v", rel, err)
	}
}

func TestResolveNoRebaseInProgress(t *testing.T) {
	_, cloneDir := setupSyncBareWithClone(t)

	var out bytes.Buffer
	err := cmd.RunResolve(cloneDir, "repos/x/CLAUDE.md", false, false, false, false, &out)
	if err == nil {
		t.Fatal("want error when no rebase is in progress")
	}
	if !strings.Contains(err.Error(), "no rebase in progress") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveStoreMissing(t *testing.T) {
	var out bytes.Buffer
	err := cmd.RunResolve(filepath.Join(t.TempDir(), "nope"), "f", false, false, false, false, &out)
	if err == nil || !strings.Contains(err.Error(), "store not found") {
		t.Errorf("want store-not-found error, got %v", err)
	}
}

func TestResolveOursAndTheirsMutuallyExclusive(t *testing.T) {
	cloneDir, _, rel := setupResolveConflict(t)

	var out bytes.Buffer
	err := cmd.RunResolve(cloneDir, rel, true, true, false, false, &out)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("want mutually-exclusive error, got %v", err)
	}
}

func TestResolveOursCompletesAndPushes(t *testing.T) {
	cloneDir, bareDir, rel := setupResolveConflict(t)

	var out bytes.Buffer
	if err := cmd.RunResolve(cloneDir, rel, true, false, false, false, &out); err != nil {
		t.Fatalf("RunResolve --ours: %v", err)
	}
	if !strings.Contains(out.String(), "Resolved and synced") {
		t.Errorf("expected success message, got:\n%s", out.String())
	}

	// "ours" during a rebase keeps the upstream (origin) version. The replayed
	// local commit becomes empty (its content already matches origin) and is
	// dropped, leaving HEAD == origin/main — the push is a clean no-op.
	got, _ := os.ReadFile(filepath.Join(cloneDir, rel))
	if string(got) != "remote version\n" {
		t.Errorf("want upstream content, got %q", string(got))
	}

	// The store's overlay on the bare origin reflects the resolved (upstream) content.
	if show := syncGitRun(t, bareDir, "show", "main:"+filepath.ToSlash(rel)); show != "remote version\n" {
		t.Errorf("want origin overlay = upstream content, got %q", show)
	}
}

func TestResolveTheirsKeepsLocal(t *testing.T) {
	cloneDir, bareDir, rel := setupResolveConflict(t)

	var out bytes.Buffer
	if err := cmd.RunResolve(cloneDir, rel, false, true, false, false, &out); err != nil {
		t.Fatalf("RunResolve --theirs: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(cloneDir, rel))
	if string(got) != "local version\n" {
		t.Errorf("want local content, got %q", string(got))
	}

	// "theirs" keeps the local version, which differs from origin, so the replayed
	// commit is non-empty and is pushed to the bare origin.
	if log := syncGitRun(t, bareDir, "log", "--oneline", "main"); !strings.Contains(log, "local overlay") {
		t.Errorf("expected local commit replayed onto origin, got:\n%s", log)
	}
	if show := syncGitRun(t, bareDir, "show", "main:"+filepath.ToSlash(rel)); show != "local version\n" {
		t.Errorf("want origin overlay = local content, got %q", show)
	}
}

func TestResolveAbort(t *testing.T) {
	cloneDir, _, _ := setupResolveConflict(t)

	var out bytes.Buffer
	if err := cmd.RunResolve(cloneDir, "", false, false, true /* abort */, false, &out); err != nil {
		t.Fatalf("RunResolve --abort: %v", err)
	}
	if !strings.Contains(out.String(), "aborted") {
		t.Errorf("expected abort message, got:\n%s", out.String())
	}
	if rebaseInProgress(cloneDir) {
		t.Error("rebase should not be in progress after abort")
	}
}

func TestResolveEditorResolves(t *testing.T) {
	cloneDir, _, rel := setupResolveConflict(t)

	// An "editor" that rewrites the file with conflict-free content.
	editor := writeEditorScript(t, "printf 'merged\\n' > \"$1\"")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", editor)

	var out bytes.Buffer
	if err := cmd.RunResolve(cloneDir, rel, false, false, false, false, &out); err != nil {
		t.Fatalf("RunResolve editor: %v", err)
	}
	if !strings.Contains(out.String(), "Resolved and synced") {
		t.Errorf("expected success, got:\n%s", out.String())
	}
	got, _ := os.ReadFile(filepath.Join(cloneDir, rel))
	if string(got) != "merged\n" {
		t.Errorf("want merged content, got %q", string(got))
	}
}

func TestResolveEditorLeavesMarkers(t *testing.T) {
	cloneDir, _, rel := setupResolveConflict(t)

	// A no-op "editor" that leaves the conflict markers in place.
	editor := writeEditorScript(t, "true")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", editor)

	var out bytes.Buffer
	err := cmd.RunResolve(cloneDir, rel, false, false, false, false, &out)
	if err == nil || !strings.Contains(err.Error(), "conflict markers remain") {
		t.Errorf("want conflict-markers-remain error, got %v", err)
	}
}

func TestResolveNoEditorMarkersRemainPrintsInstructions(t *testing.T) {
	cloneDir, _, rel := setupResolveConflict(t)

	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")

	var out bytes.Buffer
	// No editor, markers still present → instructions, nil error, no continue.
	if err := cmd.RunResolve(cloneDir, rel, false, false, false, false, &out); err != nil {
		t.Fatalf("RunResolve (no editor): %v", err)
	}
	if !strings.Contains(out.String(), "No $EDITOR set") {
		t.Errorf("expected fallback instructions, got:\n%s", out.String())
	}
}

func TestResolveNoEditorAlreadyResolved(t *testing.T) {
	cloneDir, _, rel := setupResolveConflict(t)

	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")

	// Simulate a hand-resolved file (markers removed) before re-running resolve.
	if err := os.WriteFile(filepath.Join(cloneDir, rel), []byte("hand merged\n"), 0o600); err != nil {
		t.Fatalf("manual edit: %v", err)
	}

	var out bytes.Buffer
	if err := cmd.RunResolve(cloneDir, rel, false, false, false, false, &out); err != nil {
		t.Fatalf("RunResolve (pre-resolved): %v", err)
	}
	if !strings.Contains(out.String(), "Resolved and synced") {
		t.Errorf("expected success, got:\n%s", out.String())
	}
}

func TestResolveReportsFurtherConflicts(t *testing.T) {
	bareDir, cloneDir := setupSyncBareWithClone(t)
	relA := filepath.Join("repos", "test-proj", "CLAUDE.md")
	relB := filepath.Join("repos", "test-proj", "AGENTS.md")

	// Remote edits two overlays across two commits.
	pusherDir := t.TempDir()
	if out, err := exec.Command("git", "clone", bareDir, pusherDir).CombinedOutput(); err != nil {
		t.Fatalf("git clone pusher: %v — %s", err, out)
	}
	syncGitRun(t, pusherDir, "config", "user.email", "test@test.com")
	syncGitRun(t, pusherDir, "config", "user.name", "test")
	for _, c := range []struct{ rel, content, msg string }{
		{relA, "remote A\n", "remote CLAUDE"},
		{relB, "remote B\n", "remote AGENTS"},
	} {
		writeOverlay(t, pusherDir, c.rel, c.content)
		syncGitRun(t, pusherDir, "add", c.rel)
		syncGitRun(t, pusherDir, "-c", "user.email=aimd@localhost", "-c", "user.name=aimd", "commit", "-m", c.msg)
	}
	syncGitRun(t, pusherDir, "push", "origin", "HEAD:main")

	// Local edits the same two overlays differently across two commits.
	for _, c := range []struct{ rel, content, msg string }{
		{relA, "local A\n", "local CLAUDE"},
		{relB, "local B\n", "local AGENTS"},
	} {
		writeOverlay(t, cloneDir, c.rel, c.content)
		syncGitRun(t, cloneDir, "add", c.rel)
		syncGitRun(t, cloneDir, "-c", "user.email=aimd@localhost", "-c", "user.name=aimd", "commit", "-m", c.msg)
	}

	seedRegistry(t, cloneDir, "test-proj", t.TempDir(), []string{"CLAUDE.md", "AGENTS.md"})

	var syncOut bytes.Buffer
	if err := cmd.RunSync(cloneDir, "test-machine", true, false, &syncOut); err == nil {
		t.Fatalf("expected conflict from RunSync, got nil\n%s", syncOut.String())
	}

	// Resolve the first conflict; continuing must surface the second.
	var out bytes.Buffer
	err := cmd.RunResolve(cloneDir, relA, true /* ours */, false, false, false, &out)
	if err == nil {
		t.Fatalf("expected a further-conflict error, got nil\n%s", out.String())
	}
	if !strings.Contains(out.String(), "further conflicts") {
		t.Errorf("expected further-conflicts output, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), filepath.ToSlash(relB)) {
		t.Errorf("expected second conflicted path %s in output, got:\n%s", relB, out.String())
	}
	if !rebaseInProgress(cloneDir) {
		t.Error("rebase should still be in progress after a further conflict")
	}
}

func TestResolveMissingFileArg(t *testing.T) {
	cloneDir, _, _ := setupResolveConflict(t)

	var out bytes.Buffer
	err := cmd.RunResolve(cloneDir, "", false, false, false, false, &out)
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("want required-path error, got %v", err)
	}
}

func TestResolvePathEscapesStore(t *testing.T) {
	cloneDir, _, _ := setupResolveConflict(t)

	var out bytes.Buffer
	err := cmd.RunResolve(cloneDir, "../../etc/passwd", false, false, false, false, &out)
	if err == nil || !strings.Contains(err.Error(), "escapes the store") {
		t.Errorf("want path-escape error, got %v", err)
	}
}

func TestResolveDryRun(t *testing.T) {
	cloneDir, _, rel := setupResolveConflict(t)

	headBefore := strings.TrimSpace(syncGitRun(t, cloneDir, "rev-parse", "HEAD"))

	var out bytes.Buffer
	if err := cmd.RunResolve(cloneDir, rel, true /* ours */, false, false, true /* dryRun */, &out); err != nil {
		t.Fatalf("RunResolve dry-run: %v", err)
	}
	if !strings.Contains(out.String(), "dry-run") {
		t.Errorf("expected dry-run output, got:\n%s", out.String())
	}
	// Nothing changed: still mid-rebase, HEAD unchanged.
	headAfter := strings.TrimSpace(syncGitRun(t, cloneDir, "rev-parse", "HEAD"))
	if headBefore != headAfter {
		t.Error("dry-run must not change HEAD")
	}
}

func TestResolveAbortDryRun(t *testing.T) {
	cloneDir, _, _ := setupResolveConflict(t)

	var out bytes.Buffer
	if err := cmd.RunResolve(cloneDir, "", false, false, true, true /* dryRun */, &out); err != nil {
		t.Fatalf("RunResolve abort dry-run: %v", err)
	}
	if !strings.Contains(out.String(), "dry-run") {
		t.Errorf("expected dry-run output, got:\n%s", out.String())
	}
	if !rebaseInProgress(cloneDir) {
		t.Error("abort dry-run must keep the rebase in progress")
	}
}

// rebaseInProgress reports whether a rebase is in progress in dir (either the
// merge or apply backend).
func rebaseInProgress(dir string) bool {
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(dir, ".git", d)); err == nil {
			return true
		}
	}
	return false
}

// writeEditorScript writes an executable shell script that runs body with the
// edited file path as $1, and returns its path for use as $EDITOR.
func writeEditorScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "editor.sh")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write editor script: %v", err)
	}
	return path
}

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

// seedStoreContent gives the clone some aimd store content and history: a
// registry with a project, an overlay, and a metadata file, committed + pushed.
func seedStoreContent(t *testing.T, cloneDir string) {
	t.Helper()
	// Drop the generic seed file so the clone resembles a real aimd store
	// (only .aimd/, repos/, metadata/).
	gitRun(t, cloneDir, "rm", "-q", "init.txt")
	writeStoreFile(t, cloneDir, ".aimd/registry.json", `{"version":1,"projects":{"proj":{}}}`+"\n")
	writeStoreFile(t, cloneDir, "repos/proj/CLAUDE.md", "# ctx\n")
	writeStoreFile(t, cloneDir, "metadata/proj.json", "{}\n")
	gitRun(t, cloneDir, "add", ".")
	gitRun(t, cloneDir, "-c", "user.email=aimd-bot@cybersecauto-labs.org", "-c", "user.name=aimd-bot",
		"commit", "-m", "seed store content")
	gitRun(t, cloneDir, "push", "origin", "HEAD:main")
}

func writeStoreFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func TestResetHistory_WipesToEmptyRoot(t *testing.T) {
	bareDir, cloneDir := setupBareWithClone(t)
	seedStoreContent(t, cloneDir)

	// Precondition: more than one commit of history exists.
	if before := strings.TrimSpace(gitRun(t, cloneDir, "rev-list", "--count", "HEAD")); before == "1" {
		t.Fatalf("expected >1 commit before wipe, got %s", before)
	}

	if err := store.ResetHistory(cloneDir, "test-machine"); err != nil {
		t.Fatalf("ResetHistory: %v", err)
	}

	// Local main is a single fresh root commit.
	if count := strings.TrimSpace(gitRun(t, cloneDir, "rev-list", "--count", "HEAD")); count != "1" {
		t.Errorf("local history = %s commit(s), want 1", count)
	}
	// Registry is empty.
	data, err := os.ReadFile(filepath.Join(cloneDir, ".aimd", "registry.json"))
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if !strings.Contains(string(data), `"projects": {}`) {
		t.Errorf("registry not emptied:\n%s", data)
	}
	// Overlay + metadata content gone.
	if _, err := os.Stat(filepath.Join(cloneDir, "repos", "proj")); !os.IsNotExist(err) {
		t.Error("repos/proj should be gone after wipe")
	}
	if _, err := os.Stat(filepath.Join(cloneDir, "metadata", "proj.json")); !os.IsNotExist(err) {
		t.Error("metadata/proj.json should be gone after wipe")
	}
	// Worktree is clean (commit message subject is the wipe).
	if status := strings.TrimSpace(gitRun(t, cloneDir, "status", "--porcelain")); status != "" {
		t.Errorf("worktree not clean after wipe:\n%s", status)
	}
	if subject := gitRun(t, cloneDir, "log", "-1", "--format=%s"); !strings.Contains(subject, "reset: wipe aimd store") {
		t.Errorf("unexpected commit subject: %s", subject)
	}

	// Force-push replaces the remote history.
	if err := store.ForcePush(cloneDir); err != nil {
		t.Fatalf("ForcePush: %v", err)
	}
	if count := strings.TrimSpace(gitRun(t, bareDir, "rev-list", "--count", "main")); count != "1" {
		t.Errorf("remote history = %s commit(s), want 1", count)
	}

	// A fresh clone of the wiped remote is the empty store.
	fresh := t.TempDir()
	if out, err := exec.Command("git", "clone", bareDir, fresh).CombinedOutput(); err != nil {
		t.Fatalf("git clone wiped remote: %v — %s", err, out)
	}
	freshReg, err := os.ReadFile(filepath.Join(fresh, ".aimd", "registry.json"))
	if err != nil {
		t.Fatalf("read fresh registry: %v", err)
	}
	if !strings.Contains(string(freshReg), `"projects": {}`) {
		t.Errorf("fresh clone registry not empty:\n%s", freshReg)
	}
	if _, err := os.Stat(filepath.Join(fresh, "repos", "proj")); !os.IsNotExist(err) {
		t.Error("fresh clone should not contain repos/proj")
	}
}

func TestForcePush_ErrorOnUnreachableRemote(t *testing.T) {
	_, cloneDir := setupBareWithClone(t)
	seedStoreContent(t, cloneDir)
	if err := store.ResetHistory(cloneDir, "test-machine"); err != nil {
		t.Fatalf("ResetHistory: %v", err)
	}
	gitRun(t, cloneDir, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "does-not-exist"))

	err := store.ForcePush(cloneDir)
	if err == nil {
		t.Fatal("ForcePush to an unreachable remote should error")
	}
	var pe *store.PushError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *PushError, got %T: %v", err, err)
	}
}

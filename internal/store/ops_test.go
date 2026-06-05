package store_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/internal/store"
)

// setupStoreRepo creates a minimal store repo with registry, repos/<key>, and
// metadata/<key>.json already committed. Returns storeDir and paths to the
// key files so callers can modify them to produce a second commit.
func setupStoreRepo(t *testing.T, projectKey string) (storeDir string, registryFile string) {
	t.Helper()
	storeDir = t.TempDir()

	initGitRepo(t, storeDir, false)
	gitRun(t, storeDir, "config", "user.email", "test@test.com")
	gitRun(t, storeDir, "config", "user.name", "test")

	aimdDir := filepath.Join(storeDir, ".aimd")
	if err := os.MkdirAll(aimdDir, 0o755); err != nil {
		t.Fatalf("mkdir .aimd: %v", err)
	}
	registryFile = filepath.Join(aimdDir, "registry.json")
	if err := os.WriteFile(registryFile, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write registry.json: %v", err)
	}

	reposDir := filepath.Join(storeDir, "repos", projectKey)
	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		t.Fatalf("mkdir repos/%s: %v", projectKey, err)
	}
	reposFile := filepath.Join(reposDir, "file.txt")
	if err := os.WriteFile(reposFile, []byte("content"), 0o600); err != nil {
		t.Fatalf("write repos file: %v", err)
	}

	metaDir := filepath.Join(storeDir, "metadata")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir metadata: %v", err)
	}
	metaFile := filepath.Join(metaDir, projectKey+".json")
	if err := os.WriteFile(metaFile, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write metadata file: %v", err)
	}

	gitRun(t, storeDir, "add", ".")
	gitRun(t, storeDir, "-c", "user.email=aimd@localhost", "-c", "user.name=aimd",
		"commit", "-m", "initial")

	return storeDir, registryFile
}

func TestCommit(t *testing.T) {
	const projectKey = "mykey"
	storeDir, registryFile := setupStoreRepo(t, projectKey)

	// Modify registry.json so there is something new to commit.
	if err := os.WriteFile(registryFile, []byte(`{"updated":true}`), 0o600); err != nil {
		t.Fatalf("write registry.json: %v", err)
	}

	// Call Commit with no files list.
	projectRoot := "/home/user/myproject"
	machineName := "mymachine"
	if err := store.Commit(storeDir, projectKey, projectRoot, "track", machineName, nil); err != nil {
		t.Fatalf("store.Commit: %v", err)
	}

	// Verify the commit subject line.
	logOut := gitRun(t, storeDir, "log", "--format=%s", "-1")
	pattern := regexp.MustCompile(`^track: myproject \[mymachine \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z\]`)
	if !pattern.MatchString(logOut) {
		t.Errorf("unexpected commit message: %q (want match of %s)", logOut, pattern)
	}
}

func TestCommitWithFiles(t *testing.T) {
	const projectKey = "mykey"
	storeDir, registryFile := setupStoreRepo(t, projectKey)

	if err := os.WriteFile(registryFile, []byte(`{"updated":true}`), 0o600); err != nil {
		t.Fatalf("write registry.json: %v", err)
	}

	projectRoot := "/home/user/myproject"
	machineName := "mymachine"
	files := []string{"CLAUDE.md", "AGENTS.md"}

	if err := store.Commit(storeDir, projectKey, projectRoot, "track", machineName, files); err != nil {
		t.Fatalf("store.Commit: %v", err)
	}

	// Get full commit message (subject + body).
	logOut := gitRun(t, storeDir, "log", "--format=%B", "-1")

	if !strings.Contains(logOut, "Tracked files:") {
		t.Errorf("commit body missing 'Tracked files:'; got: %q", logOut)
	}
	if !strings.Contains(logOut, "  CLAUDE.md") {
		t.Errorf("commit body missing '  CLAUDE.md'; got: %q", logOut)
	}
	if !strings.Contains(logOut, "  AGENTS.md") {
		t.Errorf("commit body missing '  AGENTS.md'; got: %q", logOut)
	}
}

func TestCommitNoBody(t *testing.T) {
	const projectKey = "mykey"
	storeDir, registryFile := setupStoreRepo(t, projectKey)

	if err := os.WriteFile(registryFile, []byte(`{"v":2}`), 0o600); err != nil {
		t.Fatalf("write registry.json: %v", err)
	}

	if err := store.Commit(storeDir, projectKey, "/home/user/proj", "track", "machine", nil); err != nil {
		t.Fatalf("store.Commit: %v", err)
	}

	// Full commit message should be just the title — no blank line + body.
	logOut := strings.TrimSpace(gitRun(t, storeDir, "log", "--format=%B", "-1"))
	lines := strings.Split(logOut, "\n")
	if len(lines) > 1 {
		t.Errorf("expected single-line commit message, got %d lines: %q", len(lines), logOut)
	}
}

func TestPush(t *testing.T) {
	const projectKey = "testkey"

	// Create a bare "origin" repo.
	bareDir := t.TempDir()
	initGitRepo(t, bareDir, true)

	// Clone it into a working copy.
	cloneDir := t.TempDir()
	cloneOut, err := exec.Command("git", "clone", bareDir, cloneDir).CombinedOutput()
	if err != nil {
		t.Fatalf("git clone: %v — %s", err, cloneOut)
	}

	// Configure git identity in the clone.
	gitRun(t, cloneDir, "config", "user.email", "test@test.com")
	gitRun(t, cloneDir, "config", "user.name", "test")

	// Create the full directory structure that Commit expects to stage.
	aimdDir := filepath.Join(cloneDir, ".aimd")
	if err := os.MkdirAll(aimdDir, 0o755); err != nil {
		t.Fatalf("mkdir .aimd: %v", err)
	}
	registryFile := filepath.Join(aimdDir, "registry.json")
	if err := os.WriteFile(registryFile, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatalf("write registry.json: %v", err)
	}

	reposDir := filepath.Join(cloneDir, "repos", projectKey)
	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		t.Fatalf("mkdir repos/%s: %v", projectKey, err)
	}
	reposFile := filepath.Join(reposDir, "file.txt")
	if err := os.WriteFile(reposFile, []byte("content"), 0o600); err != nil {
		t.Fatalf("write repos file: %v", err)
	}

	metaDir := filepath.Join(cloneDir, "metadata")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir metadata: %v", err)
	}
	metaFile := filepath.Join(metaDir, projectKey+".json")
	if err := os.WriteFile(metaFile, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write metadata file: %v", err)
	}

	// Create an initial commit in the clone and push it to bare so origin/main exists.
	gitRun(t, cloneDir, "add", ".")
	gitRun(t, cloneDir, "-c", "user.email=aimd@localhost", "-c", "user.name=aimd",
		"commit", "-m", "initial")
	gitRun(t, cloneDir, "push", "origin", "HEAD:main")

	// Now make another change and call Commit + Push.
	if err := os.WriteFile(registryFile, []byte(`{"v":2}`), 0o600); err != nil {
		t.Fatalf("write registry.json v2: %v", err)
	}

	if err := store.Commit(cloneDir, projectKey, "/projects/testproject", "track", "mymachine", nil); err != nil {
		t.Fatalf("store.Commit: %v", err)
	}

	if err := store.Push(cloneDir); err != nil {
		t.Fatalf("store.Push: %v", err)
	}

	// Verify the bare repo received the commit on main.
	bareLog := gitRun(t, bareDir, "log", "main", "--format=%s", "-1")
	pattern := regexp.MustCompile(`^track: testproject \[mymachine `)
	if !pattern.MatchString(bareLog) {
		t.Errorf("bare repo commit message %q does not match expected pattern", bareLog)
	}
}

func TestPushMarkerWrittenOnFailure(t *testing.T) {
	const projectKey = "testkey"
	storeDir, registryFile := setupStoreRepo(t, projectKey)

	// Make a change and commit so there is something to push.
	if err := os.WriteFile(registryFile, []byte(`{"v":2}`), 0o600); err != nil {
		t.Fatalf("write registry.json: %v", err)
	}
	if err := store.Commit(storeDir, projectKey, "/projects/p", "track", "m", nil); err != nil {
		t.Fatalf("store.Commit: %v", err)
	}

	// Push with no remote — should fail and write the marker.
	pushErr := store.Push(storeDir)
	if pushErr == nil {
		t.Fatal("expected Push to fail with no remote, but it succeeded")
	}

	markerPath := filepath.Join(storeDir, ".aimd", "pending-push")
	data, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("pending-push marker not written: %v", err)
	}
	content := strings.TrimSpace(string(data))
	// Content should be a valid RFC3339 timestamp.
	tsPattern := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)
	if !tsPattern.MatchString(content) {
		t.Errorf("pending-push marker content %q is not a valid RFC3339 timestamp", content)
	}
}

func TestPushMarkerClearedOnSuccess(t *testing.T) {
	const projectKey = "testkey"

	// Create bare + clone so push succeeds.
	bareDir := t.TempDir()
	initGitRepo(t, bareDir, true)

	cloneDir := t.TempDir()
	cloneOut, err := exec.Command("git", "clone", bareDir, cloneDir).CombinedOutput()
	if err != nil {
		t.Fatalf("git clone: %v — %s", err, cloneOut)
	}
	gitRun(t, cloneDir, "config", "user.email", "test@test.com")
	gitRun(t, cloneDir, "config", "user.name", "test")

	aimdDir := filepath.Join(cloneDir, ".aimd")
	if err := os.MkdirAll(aimdDir, 0o755); err != nil {
		t.Fatalf("mkdir .aimd: %v", err)
	}
	registryFile := filepath.Join(aimdDir, "registry.json")
	if err := os.WriteFile(registryFile, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatalf("write registry.json: %v", err)
	}

	reposDir := filepath.Join(cloneDir, "repos", projectKey)
	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		t.Fatalf("mkdir repos: %v", err)
	}
	if err := os.WriteFile(filepath.Join(reposDir, "f.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatalf("write repos file: %v", err)
	}
	metaDir := filepath.Join(cloneDir, "metadata")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, projectKey+".json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write meta file: %v", err)
	}

	gitRun(t, cloneDir, "add", ".")
	gitRun(t, cloneDir, "-c", "user.email=aimd@localhost", "-c", "user.name=aimd",
		"commit", "-m", "initial")
	gitRun(t, cloneDir, "push", "origin", "HEAD:main")

	// Pre-create the pending-push marker.
	markerPath := filepath.Join(aimdDir, "pending-push")
	if err := os.WriteFile(markerPath, []byte("2024-01-01T00:00:00Z\n"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	// Make a change so there is something to push.
	if err := os.WriteFile(registryFile, []byte(`{"v":2}`), 0o600); err != nil {
		t.Fatalf("write registry.json v2: %v", err)
	}
	if err := store.Commit(cloneDir, projectKey, "/p", "track", "m", nil); err != nil {
		t.Fatalf("store.Commit: %v", err)
	}

	// Push — should succeed and clear the marker.
	if err := store.Push(cloneDir); err != nil {
		t.Fatalf("store.Push failed: %v", err)
	}

	if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
		t.Errorf("expected pending-push marker to be removed after successful push, but it still exists")
	}
}

func TestOverlayDirtyCleanWorktree(t *testing.T) {
	const projectKey = "mykey"
	storeDir, _ := setupStoreRepo(t, projectKey)

	dirty, err := store.OverlayDirty(storeDir, projectKey)
	if err != nil {
		t.Fatalf("store.OverlayDirty: %v", err)
	}
	if dirty {
		t.Error("OverlayDirty = true on a committed overlay, want false")
	}
}

func TestOverlayDirtyUncommittedChange(t *testing.T) {
	const projectKey = "mykey"
	storeDir, _ := setupStoreRepo(t, projectKey)

	// Modify a file under repos/<key> without committing it.
	overlayFile := filepath.Join(storeDir, "repos", projectKey, "file.txt")
	if err := os.WriteFile(overlayFile, []byte("changed"), 0o600); err != nil {
		t.Fatalf("write overlay file: %v", err)
	}

	dirty, err := store.OverlayDirty(storeDir, projectKey)
	if err != nil {
		t.Fatalf("store.OverlayDirty: %v", err)
	}
	if !dirty {
		t.Error("OverlayDirty = false on an uncommitted overlay change, want true")
	}
}

func TestOverlayDirtyMissingOverlay(t *testing.T) {
	const projectKey = "mykey"
	storeDir, _ := setupStoreRepo(t, projectKey)

	// A project key with no overlay directory yields an empty status.
	dirty, err := store.OverlayDirty(storeDir, "nonexistent")
	if err != nil {
		t.Fatalf("store.OverlayDirty: %v", err)
	}
	if dirty {
		t.Error("OverlayDirty = true for a missing overlay, want false")
	}
}

func TestPushErrorInterface(t *testing.T) {
	inner := fmt.Errorf("exit status 1")
	pe := &store.PushError{
		Transient: true,
		Output:    "some git output",
		Err:       inner,
	}

	// Error() should contain both the inner error and the output.
	errMsg := pe.Error()
	if !strings.Contains(errMsg, "exit status 1") {
		t.Errorf("PushError.Error() missing inner error text: %q", errMsg)
	}
	if !strings.Contains(errMsg, "some git output") {
		t.Errorf("PushError.Error() missing Output text: %q", errMsg)
	}

	// Unwrap() should let errors.Is work with the inner error.
	if !errors.Is(pe, inner) {
		t.Errorf("errors.Is(pe, inner) returned false; Unwrap may not be working")
	}
}

func TestCommitWithUnknownVerb(t *testing.T) {
	const projectKey = "unknownkey"
	storeDir, registryFile := setupStoreRepo(t, projectKey)

	if err := os.WriteFile(registryFile, []byte(`{"v":99}`), 0o600); err != nil {
		t.Fatalf("write registry.json: %v", err)
	}

	files := []string{"some-file.md"}
	// "import" is not a known verb — label should be "Import" (title-cased).
	if err := store.Commit(storeDir, projectKey, "/home/user/proj", "import", "m", files); err != nil {
		t.Fatalf("store.Commit: %v", err)
	}

	logOut := gitRun(t, storeDir, "log", "--format=%B", "-1")
	if !strings.Contains(logOut, "Import files:") {
		t.Errorf("commit body missing 'Import files:'; got: %q", logOut)
	}
}

// aimd's machine-generated store commits must not inherit the user's global
// commit-signing config: with commit.gpgsign=true and a broken signing program,
// a normal `git commit` would fail, but aimd commits must still succeed because
// they disable inherited signing.
func TestCommitSucceedsWithGlobalGPGSigningEnabled(t *testing.T) {
	const projectKey = "gpgkey"
	storeDir, registryFile := setupStoreRepo(t, projectKey)

	// Force-enable commit signing globally with a signing program that always
	// fails. Applied AFTER setup so only the aimd commit under test is affected.
	fakeGPG := filepath.Join(t.TempDir(), "fail-gpg")
	if err := os.WriteFile(fakeGPG, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write fake gpg program: %v", err)
	}
	globalCfg := filepath.Join(t.TempDir(), "gitconfig")
	cfg := "[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = " + fakeGPG + "\n"
	if err := os.WriteFile(globalCfg, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write global gitconfig: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalCfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	// Modify registry.json so there is something to commit.
	if err := os.WriteFile(registryFile, []byte(`{"updated":true}`), 0o600); err != nil {
		t.Fatalf("write registry.json: %v", err)
	}

	if err := store.Commit(storeDir, projectKey, "/home/user/proj", "track", "m", nil); err != nil {
		t.Fatalf("store.Commit must succeed despite global commit.gpgsign=true: %v", err)
	}
}

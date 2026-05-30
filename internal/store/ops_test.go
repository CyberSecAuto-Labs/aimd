package store_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/internal/store"
)

func TestCommit(t *testing.T) {
	storeDir := t.TempDir()
	const projectKey = "mykey"

	// Initialise a plain git repo.
	initGitRepo(t, storeDir, false)

	// Configure minimal git identity so the commit can be created.
	gitRun(t, storeDir, "config", "user.email", "test@test.com")
	gitRun(t, storeDir, "config", "user.name", "test")

	// Create the full directory structure that Commit expects to stage.
	aimdDir := filepath.Join(storeDir, ".aimd")
	if err := os.MkdirAll(aimdDir, 0o755); err != nil {
		t.Fatalf("mkdir .aimd: %v", err)
	}
	registryFile := filepath.Join(aimdDir, "registry.json")
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

	// Stage and commit the initial files so HEAD exists.
	gitRun(t, storeDir, "add", ".")
	gitRun(t, storeDir, "-c", "user.email=aimd@localhost", "-c", "user.name=aimd",
		"commit", "-m", "initial")

	// Modify registry.json so there is something new to commit.
	if err := os.WriteFile(registryFile, []byte(`{"updated":true}`), 0o600); err != nil {
		t.Fatalf("write registry.json: %v", err)
	}

	// Call Commit.
	projectRoot := "/home/user/myproject"
	machineName := "mymachine"
	if err := store.Commit(storeDir, projectKey, projectRoot, "track", machineName); err != nil {
		t.Fatalf("store.Commit: %v", err)
	}

	// Verify the commit message.
	logOut := gitRun(t, storeDir, "log", "--format=%s", "-1")
	pattern := regexp.MustCompile(`^track: myproject \[mymachine \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z\]`)
	if !pattern.MatchString(logOut) {
		t.Errorf("unexpected commit message: %q (want match of %s)", logOut, pattern)
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

	if err := store.Commit(cloneDir, projectKey, "/projects/testproject", "track", "mymachine"); err != nil {
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

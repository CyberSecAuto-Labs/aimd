package cmd_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CyberSecAuto-Labs/aimd/cmd"
)

// makeRestoreStore sets up a store repo that already has overlay files and a
// registry entry for projectKey. trackedFiles are relative paths (e.g.
// "CLAUDE.md"). The store is committed so that store.Commit() can add new
// commits on top.
func makeRestoreStore(t *testing.T, storeDir, projectKey, _ string, trackedFiles []string) {
	t.Helper()

	// Initialise the store git repo with identity.
	cmds := [][]string{
		{"git", "-C", storeDir, "init"},
		{"git", "-C", storeDir, "config", "user.email", "aimd@localhost"},
		{"git", "-C", storeDir, "config", "user.name", "aimd"},
	}
	for _, c := range cmds {
		if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", c, err, out)
		}
	}

	// Scaffold store layout.
	for _, sub := range []string{".aimd", "metadata"} {
		if err := os.MkdirAll(filepath.Join(storeDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	// Write overlay files into store/repos/<projectKey>/.
	reposDir := filepath.Join(storeDir, "repos", projectKey)
	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		t.Fatalf("mkdir repos: %v", err)
	}
	for _, f := range trackedFiles {
		overlayPath := filepath.Join(reposDir, f)
		if err := os.MkdirAll(filepath.Dir(overlayPath), 0o755); err != nil {
			t.Fatalf("mkdir overlay parent: %v", err)
		}
		if err := os.WriteFile(overlayPath, []byte("# overlay content for "+f+"\n"), 0o644); err != nil {
			t.Fatalf("write overlay %s: %v", f, err)
		}
	}

	// Build registry JSON with the project and tracked files.
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	var trackedJSON strings.Builder
	for i, f := range trackedFiles {
		if i > 0 {
			trackedJSON.WriteString(",")
		}
		_, _ = fmt.Fprintf(&trackedJSON, `{"path":%q,"addedAt":%q,"addedBy":"other-machine"}`, f, now)
	}

	regJSON := `{"version":1,"projects":{"` + projectKey + `":{"displayName":"myapp","remoteUrl":"git@github.com:test/myapp.git","machines":{},"tracked":[` + trackedJSON.String() + `]}}}` + "\n"
	if err := os.WriteFile(filepath.Join(storeDir, ".aimd", "registry.json"), []byte(regJSON), 0o600); err != nil {
		t.Fatalf("write registry.json: %v", err)
	}

	// Initial commit so store.Commit() can add on top.
	cmds2 := [][]string{
		{"git", "-C", storeDir, "add", "."},
		{"git", "-C", storeDir, "-c", "user.email=aimd@localhost", "-c", "user.name=aimd",
			"commit", "-m", "init: scaffold aimd store"},
	}
	for _, c := range cmds2 {
		if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", c, err, out)
		}
	}
}

// makeProjectWithRemote initialises a project git repo and adds a fake remote
// so that project.Detect() can derive a project key.
// The project key will be "github.com~test~myapp".
func makeProjectWithRemote(t *testing.T, dir string) {
	t.Helper()
	makeProjectRepo(t, dir)
	out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", "git@github.com:test/myapp.git").CombinedOutput()
	if err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
}

func TestRunRestore_CreatesSymlink(t *testing.T) {
	// Not parallel — uses os.Chdir which affects the whole process.
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	storeDir := filepath.Join(base, "store")

	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	makeProjectWithRemote(t, projectDir)
	makeRestoreStore(t, storeDir, "github.com~test~myapp", projectDir, []string{"CLAUDE.md"})

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	if err := cmd.RunRestore(storeDir, "new-machine", false, false, &bytes.Buffer{}); err != nil {
		t.Fatalf("RunRestore() error = %v", err)
	}

	// Assert: project_dst is now a symlink pointing to overlay_src.
	projectDst := filepath.Join(projectDir, "CLAUDE.md")
	fi, err := os.Lstat(projectDst)
	if err != nil {
		t.Fatalf("Lstat CLAUDE.md: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("expected CLAUDE.md to be a symlink")
	}

	target, err := os.Readlink(projectDst)
	if err != nil {
		t.Fatalf("Readlink CLAUDE.md: %v", err)
	}
	overlaySrc := filepath.Join(storeDir, "repos", "github.com~test~myapp", "CLAUDE.md")
	if target != overlaySrc {
		t.Errorf("symlink target = %q, want %q", target, overlaySrc)
	}
}

func TestRunRestore_Idempotent(t *testing.T) {
	// Not parallel — uses os.Chdir which affects the whole process.
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	storeDir := filepath.Join(base, "store")

	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	makeProjectWithRemote(t, projectDir)
	makeRestoreStore(t, storeDir, "github.com~test~myapp", projectDir, []string{"CLAUDE.md"})

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	var out1 bytes.Buffer
	if err := cmd.RunRestore(storeDir, "new-machine", false, false, &out1); err != nil {
		t.Fatalf("RunRestore() first run error = %v", err)
	}

	var out2 bytes.Buffer
	if err := cmd.RunRestore(storeDir, "new-machine", false, false, &out2); err != nil {
		t.Fatalf("RunRestore() second run error = %v", err)
	}

	// Second run should skip (symlink already correct).
	// Pull and push warnings are expected (no remote in tests), but no file-level warnings.
	output2 := out2.String()
	if strings.Contains(output2, "is a real file") || strings.Contains(output2, "not in store") {
		t.Errorf("second run produced unexpected file-level warnings: %s", output2)
	}
	// Should report 0 restored files (idempotent).
	if !strings.Contains(output2, "Restored 0") {
		t.Errorf("second run should report 0 restored files, got: %s", output2)
	}

	// Symlink must still be valid after two runs.
	projectDst := filepath.Join(projectDir, "CLAUDE.md")
	fi, err := os.Lstat(projectDst)
	if err != nil {
		t.Fatalf("Lstat CLAUDE.md: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("expected CLAUDE.md to remain a symlink after second run")
	}
}

func TestRunRestore_BrokenSymlink(t *testing.T) {
	// Not parallel — uses os.Chdir which affects the whole process.
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	storeDir := filepath.Join(base, "store")

	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	makeProjectWithRemote(t, projectDir)
	makeRestoreStore(t, storeDir, "github.com~test~myapp", projectDir, []string{"CLAUDE.md"})

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	// Plant a broken symlink at CLAUDE.md.
	projectDst := filepath.Join(projectDir, "CLAUDE.md")
	if err := os.Symlink("/nonexistent/path/CLAUDE.md", projectDst); err != nil {
		t.Fatalf("creating broken symlink: %v", err)
	}

	if err := cmd.RunRestore(storeDir, "new-machine", false, false, &bytes.Buffer{}); err != nil {
		t.Fatalf("RunRestore() error = %v", err)
	}

	// Assert: broken symlink was repaired — now points to overlay.
	fi, err := os.Lstat(projectDst)
	if err != nil {
		t.Fatalf("Lstat CLAUDE.md: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("expected CLAUDE.md to be a symlink")
	}

	target, err := os.Readlink(projectDst)
	if err != nil {
		t.Fatalf("Readlink CLAUDE.md: %v", err)
	}
	overlaySrc := filepath.Join(storeDir, "repos", "github.com~test~myapp", "CLAUDE.md")
	if target != overlaySrc {
		t.Errorf("symlink target = %q, want %q", target, overlaySrc)
	}
}

func TestRunRestore_MissingOverlay(t *testing.T) {
	// Not parallel — uses os.Chdir which affects the whole process.
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	storeDir := filepath.Join(base, "store")

	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	makeProjectWithRemote(t, projectDir)
	// Register CLAUDE.md in the registry but DON'T write the overlay file.
	// We do this by setting up the store WITHOUT trackedFiles, then writing
	// the registry manually to include CLAUDE.md.
	makeRestoreStore(t, storeDir, "github.com~test~myapp", projectDir, []string{})

	// Overwrite registry.json to include a tracked file with no overlay.
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	regJSON := `{"version":1,"projects":{"github.com~test~myapp":{"displayName":"myapp","remoteUrl":"git@github.com:test/myapp.git","machines":{},"tracked":[{"path":"CLAUDE.md","addedAt":"` + now + `","addedBy":"other-machine"}]}}}` + "\n"
	if err := os.WriteFile(filepath.Join(storeDir, ".aimd", "registry.json"), []byte(regJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	var out bytes.Buffer
	if err := cmd.RunRestore(storeDir, "new-machine", false, false, &out); err != nil {
		t.Fatalf("RunRestore() error = %v", err)
	}

	// Assert: warning was printed about missing overlay.
	output := out.String()
	if !strings.Contains(output, "not in store") {
		t.Errorf("expected 'not in store' warning, got: %s", output)
	}

	// Assert: CLAUDE.md was NOT created in the project.
	projectDst := filepath.Join(projectDir, "CLAUDE.md")
	if _, err := os.Lstat(projectDst); !os.IsNotExist(err) {
		t.Error("expected CLAUDE.md to not exist after missing overlay warning")
	}
}

func TestRunRestore_RealFile_NoForce(t *testing.T) {
	// Not parallel — uses os.Chdir which affects the whole process.
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	storeDir := filepath.Join(base, "store")

	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	makeProjectWithRemote(t, projectDir)
	makeRestoreStore(t, storeDir, "github.com~test~myapp", projectDir, []string{"CLAUDE.md"})

	// Plant a real file at CLAUDE.md.
	projectDst := filepath.Join(projectDir, "CLAUDE.md")
	if err := os.WriteFile(projectDst, []byte("local content"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	var out bytes.Buffer
	if err := cmd.RunRestore(storeDir, "new-machine", false, false, &out); err != nil {
		t.Fatalf("RunRestore() error = %v", err)
	}

	// Assert: warning printed.
	output := out.String()
	if !strings.Contains(output, "real file") {
		t.Errorf("expected 'real file' warning, got: %s", output)
	}

	// Assert: CLAUDE.md is still a real file (not replaced).
	fi, err := os.Lstat(projectDst)
	if err != nil {
		t.Fatalf("Lstat CLAUDE.md: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("expected CLAUDE.md to remain a real file without --force")
	}

	content, _ := os.ReadFile(projectDst)
	if string(content) != "local content" {
		t.Errorf("CLAUDE.md content was modified without --force")
	}
}

func TestRunRestore_RealFile_Force(t *testing.T) {
	// Not parallel — uses os.Chdir which affects the whole process.
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	storeDir := filepath.Join(base, "store")

	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	makeProjectWithRemote(t, projectDir)
	makeRestoreStore(t, storeDir, "github.com~test~myapp", projectDir, []string{"CLAUDE.md"})

	// Plant a real file at CLAUDE.md.
	projectDst := filepath.Join(projectDir, "CLAUDE.md")
	if err := os.WriteFile(projectDst, []byte("local content"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	if err := cmd.RunRestore(storeDir, "new-machine", true, false, &bytes.Buffer{}); err != nil {
		t.Fatalf("RunRestore(force) error = %v", err)
	}

	// Assert: CLAUDE.md is now a symlink pointing to overlay.
	fi, err := os.Lstat(projectDst)
	if err != nil {
		t.Fatalf("Lstat CLAUDE.md: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("expected CLAUDE.md to be a symlink after --force")
	}

	target, err := os.Readlink(projectDst)
	if err != nil {
		t.Fatalf("Readlink CLAUDE.md: %v", err)
	}
	overlaySrc := filepath.Join(storeDir, "repos", "github.com~test~myapp", "CLAUDE.md")
	if target != overlaySrc {
		t.Errorf("symlink target = %q, want %q", target, overlaySrc)
	}
}

func TestRunRestore_StoreNotInitialized(t *testing.T) {
	// Not parallel — uses os.Chdir.
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	storeDir := filepath.Join(base, "store-does-not-exist")

	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeProjectRepo(t, projectDir)

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	err = cmd.RunRestore(storeDir, "test-machine", false, false, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error when store does not exist, got nil")
	}
	if !strings.Contains(err.Error(), "aimd init") {
		t.Errorf("error should mention 'aimd init', got: %v", err)
	}
}

func TestRunRestore_DirectoryAtDestination(t *testing.T) {
	// Not parallel — uses os.Chdir.
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	storeDir := filepath.Join(base, "store")

	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	makeProjectRepo(t, projectDir)
	if err := exec.Command("git", "-C", projectDir, "remote", "add", "origin",
		"git@github.com:test/myapp.git").Run(); err != nil {
		t.Fatalf("git remote add: %v", err)
	}
	makeRestoreStore(t, storeDir, "github.com~test~myapp", projectDir, []string{"CLAUDE.md"})

	// Place a non-empty directory at the destination path.
	projectDst := filepath.Join(projectDir, "CLAUDE.md")
	if err := os.MkdirAll(projectDst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDst, "inner.txt"), []byte("contents"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	var buf bytes.Buffer
	if err := cmd.RunRestore(storeDir, "test-machine", false, false, &buf); err != nil {
		t.Fatalf("RunRestore() unexpected error: %v", err)
	}

	// Assert: directory is still intact (not removed).
	fi, statErr := os.Lstat(projectDst)
	if statErr != nil {
		t.Fatalf("Lstat CLAUDE.md: %v", statErr)
	}
	if !fi.IsDir() {
		t.Error("expected CLAUDE.md directory to be left in place")
	}

	// Assert: warning was printed.
	if !strings.Contains(buf.String(), "is a directory") {
		t.Errorf("expected 'is a directory' warning, got: %q", buf.String())
	}
}

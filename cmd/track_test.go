package cmd_test

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/cmd"
)

// makeProjectRepo initialises a plain git repo with user identity configured.
func makeProjectRepo(t *testing.T, dir string) {
	t.Helper()
	cmds := [][]string{
		{"git", "-C", dir, "init"},
		{"git", "-C", dir, "config", "user.email", "test@test.com"},
		{"git", "-C", dir, "config", "user.name", "Test"},
	}
	for _, c := range cmds {
		if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", c, err, out)
		}
	}
}

// makeStoreRepo initialises a plain git repo to act as the aimd store.
func makeStoreRepo(t *testing.T, storeDir string) {
	t.Helper()
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
	for _, sub := range []string{".aimd", "repos", "metadata"} {
		if err := os.MkdirAll(filepath.Join(storeDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	// Write minimal registry.json.
	reg := []byte(`{"version":1,"projects":{}}` + "\n")
	if err := os.WriteFile(filepath.Join(storeDir, ".aimd", "registry.json"), reg, 0o600); err != nil {
		t.Fatalf("write registry.json: %v", err)
	}

	// Initial commit so the store repo HEAD exists.
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

func TestRunTrack_HappyPath(t *testing.T) {
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

	makeProjectRepo(t, projectDir)
	makeStoreRepo(t, storeDir)

	// Write CLAUDE.md in the project.
	content := "# CLAUDE context\nhello from test\n"
	claudeMd := filepath.Join(projectDir, "CLAUDE.md")
	if err := os.WriteFile(claudeMd, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Switch CWD to the project repo so project.Detect() works.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	if err := cmd.RunTrack([]string{"CLAUDE.md"}, storeDir, "test-machine", false, io.Discard); err != nil {
		t.Fatalf("RunTrack() error = %v", err)
	}

	// Assert: CLAUDE.md is now a symlink.
	fi, err := os.Lstat(claudeMd)
	if err != nil {
		t.Fatalf("Lstat CLAUDE.md: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("expected CLAUDE.md to be a symlink")
	}

	// Assert: symlink target is inside store/repos/.
	target, err := os.Readlink(claudeMd)
	if err != nil {
		t.Fatalf("Readlink CLAUDE.md: %v", err)
	}
	reposDir := filepath.Join(storeDir, "repos")
	if !strings.HasPrefix(target, reposDir) {
		t.Errorf("symlink target %q does not start with %q", target, reposDir)
	}

	// Assert: overlay file exists with correct content.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading overlay file: %v", err)
	}
	if string(got) != content {
		t.Errorf("overlay content = %q, want %q", string(got), content)
	}

	// Assert: git status in project is clean (CLAUDE.md excluded).
	statusOut, statusErr := exec.Command("git", "-C", projectDir, "status", "--short").CombinedOutput()
	if statusErr != nil {
		t.Fatalf("git status: %v\n%s", statusErr, statusOut)
	}
	if len(statusOut) != 0 {
		t.Errorf("expected clean git status, got:\n%s", string(statusOut))
	}

	// Assert: registry has the tracked file.
	regPath := filepath.Join(storeDir, ".aimd", "registry.json")
	regData, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("reading registry: %v", err)
	}
	if !strings.Contains(string(regData), "CLAUDE.md") {
		t.Errorf("registry does not contain CLAUDE.md:\n%s", string(regData))
	}
}

func TestRunTrack_DryRun(t *testing.T) {
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
	makeStoreRepo(t, storeDir)

	claudeMd := filepath.Join(projectDir, "CLAUDE.md")
	if err := os.WriteFile(claudeMd, []byte("content"), 0o644); err != nil {
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

	if err := cmd.RunTrack([]string{"CLAUDE.md"}, storeDir, "test-machine", true, io.Discard); err != nil {
		t.Fatalf("RunTrack(dry-run) error = %v", err)
	}

	// In dry-run mode, CLAUDE.md must remain a regular file.
	fi, err := os.Lstat(claudeMd)
	if err != nil {
		t.Fatalf("Lstat CLAUDE.md: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("dry-run: CLAUDE.md should not have been converted to a symlink")
	}
}

func TestRunTrack_AlreadySymlink(t *testing.T) {
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
	makeStoreRepo(t, storeDir)

	// Create CLAUDE.md as a symlink pointing to a real file.
	targetFile := filepath.Join(base, "target.md")
	if err := os.WriteFile(targetFile, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	claudeMd := filepath.Join(projectDir, "CLAUDE.md")
	if err := os.Symlink(targetFile, claudeMd); err != nil {
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

	err = cmd.RunTrack([]string{"CLAUDE.md"}, storeDir, "test-machine", false, io.Discard)
	if err == nil {
		t.Fatal("expected error tracking an existing symlink, got nil")
	}
}

func TestRunTrack_NotExists(t *testing.T) {
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
	makeStoreRepo(t, storeDir)

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	err = cmd.RunTrack([]string{"nonexistent.md"}, storeDir, "test-machine", false, io.Discard)
	if err == nil {
		t.Fatal("expected error for non-existent file, got nil")
	}
}

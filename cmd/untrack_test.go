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

// setupTrackedFile is a test helper that creates a project with a tracked CLAUDE.md.
// It sets CWD to projectDir; callers must defer restoring the original CWD.
// Returns (projectDir, storeDir, overlayPath, original content).
func setupTrackedFile(t *testing.T) (projectDir, storeDir, overlayPath string, content []byte) {
	t.Helper()

	base := t.TempDir()
	projectDir = filepath.Join(base, "project")
	storeDir = filepath.Join(base, "store")

	for _, d := range []string{projectDir, storeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	makeProjectRepo(t, projectDir)
	makeStoreRepo(t, storeDir)

	content = []byte("# CLAUDE context\nhello from untrack test\n")
	claudeMd := filepath.Join(projectDir, "CLAUDE.md")
	if err := os.WriteFile(claudeMd, content, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}

	if err := cmd.RunTrack([]string{"CLAUDE.md"}, storeDir, "test-machine", false, io.Discard); err != nil {
		t.Fatalf("RunTrack setup: %v", err)
	}

	// Locate the overlay path via the symlink target.
	symlinkTarget, err := os.Readlink(claudeMd)
	if err != nil {
		t.Fatalf("Readlink CLAUDE.md: %v", err)
	}
	overlayPath = symlinkTarget
	return projectDir, storeDir, overlayPath, content
}

func TestRunUntrack_Restore_HappyPath(t *testing.T) {
	// Not parallel — uses os.Chdir which affects the whole process.

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	projectDir, storeDir, overlayPath, content := setupTrackedFile(t)
	defer func() { _ = os.Chdir(orig) }()

	claudeMd := filepath.Join(projectDir, "CLAUDE.md")

	// Run untrack --restore (default, deleteMode=false).
	if err := cmd.RunUntrack(
		[]string{"CLAUDE.md"}, storeDir, "test-machine",
		false, true, false,
		strings.NewReader(""), io.Discard,
	); err != nil {
		t.Fatalf("RunUntrack() error = %v", err)
	}

	// Assert: CLAUDE.md is now a real file, not a symlink.
	fi, err := os.Lstat(claudeMd)
	if err != nil {
		t.Fatalf("Lstat CLAUDE.md: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("expected CLAUDE.md to be a regular file after restore, still a symlink")
	}

	// Assert: content matches original.
	got, err := os.ReadFile(claudeMd)
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("restored content = %q, want %q", string(got), string(content))
	}

	// Assert: git status is clean (exclude entry removed, file is regular).
	statusOut, statusErr := exec.Command("git", "-C", projectDir, "status", "--short").CombinedOutput()
	if statusErr != nil {
		t.Fatalf("git status: %v\n%s", statusErr, statusOut)
	}
	// CLAUDE.md is now a regular file that has never been committed, so it may
	// show up as untracked "??" — that is expected and acceptable. What we care
	// about is that the symlink is gone and no aimd-managed exclude is hiding it.
	// We verify there are no changes to tracked files (no "M" lines).
	for _, line := range strings.Split(strings.TrimSpace(string(statusOut)), "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "??") {
			t.Errorf("unexpected git status line (expected only untracked): %q", line)
		}
	}

	// Assert: overlay file removed from store.
	if _, err := os.Stat(overlayPath); !os.IsNotExist(err) {
		t.Errorf("overlay file %s should have been removed from store", overlayPath)
	}

	// Assert: registry no longer lists CLAUDE.md.
	regPath := filepath.Join(storeDir, ".aimd", "registry.json")
	regData, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("reading registry: %v", err)
	}
	if strings.Contains(string(regData), "CLAUDE.md") {
		t.Errorf("registry still contains CLAUDE.md after untrack:\n%s", string(regData))
	}
}

func TestRunUntrack_Delete_HappyPath(t *testing.T) {
	// Not parallel — uses os.Chdir.

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	projectDir, storeDir, overlayPath, _ := setupTrackedFile(t)
	defer func() { _ = os.Chdir(orig) }()

	claudeMd := filepath.Join(projectDir, "CLAUDE.md")

	// Run untrack --delete.
	if err := cmd.RunUntrack(
		[]string{"CLAUDE.md"}, storeDir, "test-machine",
		true, true, false,
		strings.NewReader(""), io.Discard,
	); err != nil {
		t.Fatalf("RunUntrack(--delete) error = %v", err)
	}

	// Assert: CLAUDE.md removed from project.
	if _, err := os.Lstat(claudeMd); !os.IsNotExist(err) {
		t.Errorf("CLAUDE.md should have been removed from project after --delete")
	}

	// Assert: overlay removed from store.
	if _, err := os.Stat(overlayPath); !os.IsNotExist(err) {
		t.Errorf("overlay file %s should have been removed from store", overlayPath)
	}

	// Assert: registry clean.
	regPath := filepath.Join(storeDir, ".aimd", "registry.json")
	regData, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("reading registry: %v", err)
	}
	if strings.Contains(string(regData), "CLAUDE.md") {
		t.Errorf("registry still contains CLAUDE.md after --delete:\n%s", string(regData))
	}
}

func TestRunUntrack_NotTracked_Error(t *testing.T) {
	// Not parallel — uses os.Chdir.

	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	storeDir := filepath.Join(base, "store")

	for _, d := range []string{projectDir, storeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	makeProjectRepo(t, projectDir)
	makeStoreRepo(t, storeDir)

	// Write a plain regular file (not a symlink into the store).
	regularFile := filepath.Join(projectDir, "regular.md")
	if err := os.WriteFile(regularFile, []byte("content"), 0o644); err != nil {
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

	err = cmd.RunUntrack(
		[]string{"regular.md"}, storeDir, "test-machine",
		false, true, false,
		strings.NewReader(""), io.Discard,
	)
	if err == nil {
		t.Fatal("expected error untracking a regular (non-symlink) file, got nil")
	}
}

func TestRunUntrack_DryRun(t *testing.T) {
	// Not parallel — uses os.Chdir.

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	projectDir, storeDir, overlayPath, _ := setupTrackedFile(t)
	defer func() { _ = os.Chdir(orig) }()

	claudeMd := filepath.Join(projectDir, "CLAUDE.md")

	// Run untrack in dry-run mode.
	if err := cmd.RunUntrack(
		[]string{"CLAUDE.md"}, storeDir, "test-machine",
		false, true, true,
		strings.NewReader(""), io.Discard,
	); err != nil {
		t.Fatalf("RunUntrack(dry-run) error = %v", err)
	}

	// Assert: CLAUDE.md is still a symlink (dry-run made no changes).
	fi, err := os.Lstat(claudeMd)
	if err != nil {
		t.Fatalf("Lstat CLAUDE.md: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("dry-run: CLAUDE.md should still be a symlink after dry-run untrack")
	}

	// Assert: overlay still exists.
	if _, err := os.Stat(overlayPath); os.IsNotExist(err) {
		t.Errorf("dry-run: overlay file %s should not have been removed", overlayPath)
	}
}

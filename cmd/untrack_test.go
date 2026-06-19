package cmd_test

import (
	"bytes"
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

// When a surviving user pattern still ignores the restored file, untrack
// --restore must warn the user with the matching pattern.
func TestRunUntrack_Restore_WarnsWhenUserPatternShadows(t *testing.T) {
	// Not parallel — uses os.Chdir which affects the whole process.

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	projectDir, storeDir, _, _ := setupTrackedFile(t)
	defer func() { _ = os.Chdir(orig) }()

	// Add a bare user pattern outside aimd's managed block that also ignores the
	// file. After untrack removes the managed entry, this pattern still shadows it.
	excl := filepath.Join(projectDir, ".git", "info", "exclude")
	f, err := os.OpenFile(excl, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open exclude: %v", err)
	}
	if _, err := f.WriteString("\n*.md\n"); err != nil {
		t.Fatalf("write user pattern: %v", err)
	}
	_ = f.Close()

	var out bytes.Buffer
	if err := cmd.RunUntrack(
		[]string{"CLAUDE.md"}, storeDir, "test-machine",
		false, true, false,
		strings.NewReader(""), &out,
	); err != nil {
		t.Fatalf("RunUntrack() error = %v", err)
	}

	s := out.String()
	if !strings.Contains(s, "still ignored") {
		t.Errorf("expected a shadow warning; got output:\n%s", s)
	}
	if !strings.Contains(s, "*.md") {
		t.Errorf("expected the matching pattern in the warning; got output:\n%s", s)
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

// untrack --delete of multiple files where a later file fails must
// still persist the deletion of the files that already succeeded — otherwise the
// committed registry keeps listing a file whose content is already gone.
func TestRunUntrack_Delete_PartialFailurePersistsSucceeded(t *testing.T) {
	// Not parallel — uses os.Chdir.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	projectDir, storeDir, overlayPath, _ := setupTrackedFile(t)
	defer func() { _ = os.Chdir(orig) }()

	// Add a plain regular file that is NOT a tracked symlink — it will fail.
	if err := os.WriteFile(filepath.Join(projectDir, "regular.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// CLAUDE.md (tracked) succeeds; regular.md fails → overall error.
	err = cmd.RunUntrack(
		[]string{"CLAUDE.md", "regular.md"}, storeDir, "test-machine",
		true /* delete */, true /* yes */, false,
		strings.NewReader(""), io.Discard,
	)
	if err == nil {
		t.Fatal("expected an error from the failing second file, got nil")
	}

	// CLAUDE.md must be fully removed: project symlink gone, overlay gone.
	if _, statErr := os.Lstat(filepath.Join(projectDir, "CLAUDE.md")); !os.IsNotExist(statErr) {
		t.Error("CLAUDE.md should have been removed from the project")
	}
	if _, statErr := os.Stat(overlayPath); !os.IsNotExist(statErr) {
		t.Error("CLAUDE.md overlay should have been removed from the store")
	}

	// Registry must no longer list CLAUDE.md (the deletion was persisted).
	regData, err := os.ReadFile(filepath.Join(storeDir, ".aimd", "registry.json"))
	if err != nil {
		t.Fatalf("reading registry: %v", err)
	}
	if strings.Contains(string(regData), "CLAUDE.md") {
		t.Errorf("registry still lists CLAUDE.md after its content was deleted:\n%s", regData)
	}
}

// untrack must refuse to delete an overlay that belongs to a different
// project, even if the symlink points somewhere under repos/.
func TestRunUntrack_RefusesCrossProjectOverlay(t *testing.T) {
	// Not parallel — uses os.Chdir.
	base := t.TempDir()
	projectB := filepath.Join(base, "projectB")
	storeDir := filepath.Join(base, "store")

	for _, d := range []string{projectB, storeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	makeProjectRepo(t, projectB)
	makeStoreRepo(t, storeDir)

	// Create another project's overlay in the store and point a symlink in B at it.
	otherOverlay := filepath.Join(storeDir, "repos", "github.com~other~projectA", "secret.md")
	if err := os.MkdirAll(filepath.Dir(otherOverlay), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherOverlay, []byte("project A's only copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(otherOverlay, filepath.Join(projectB, "evil.md")); err != nil {
		t.Fatal(err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectB); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	err = cmd.RunUntrack(
		[]string{"evil.md"}, storeDir, "test-machine",
		true /* delete */, true /* yes */, false,
		strings.NewReader(""), io.Discard,
	)
	if err == nil {
		t.Fatal("expected untrack to refuse a cross-project overlay, got nil")
	}

	// Project A's overlay must NOT have been deleted.
	if _, statErr := os.Stat(otherOverlay); statErr != nil {
		t.Errorf("project A's overlay was destroyed by a cross-project untrack: %v", statErr)
	}
}

// when overlay removal fails during --delete, the project symlink must
// remain so the file stays re-untrackable (overlay removed before symlink).
func TestRunUntrack_Delete_OverlayFailureLeavesReUntrackable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions; cannot simulate overlay removal failure")
	}

	// Not parallel — uses os.Chdir.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	projectDir, storeDir, overlayPath, _ := setupTrackedFile(t)
	defer func() { _ = os.Chdir(orig) }()

	claudeMd := filepath.Join(projectDir, "CLAUDE.md")

	// Make the overlay's parent read-only so os.Remove(overlay) fails.
	overlayParent := filepath.Dir(overlayPath)
	if err := os.Chmod(overlayParent, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(overlayParent, 0o755) }()

	err = cmd.RunUntrack(
		[]string{"CLAUDE.md"}, storeDir, "test-machine",
		true /* delete */, true /* yes */, false,
		strings.NewReader(""), io.Discard,
	)
	if err == nil {
		t.Fatal("expected an error when overlay removal fails, got nil")
	}

	// The project symlink must still be present (not removed before the overlay).
	fi, lstatErr := os.Lstat(claudeMd)
	if lstatErr != nil {
		t.Fatalf("project symlink should still exist after overlay-removal failure: %v", lstatErr)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("expected CLAUDE.md to still be a symlink after the failed delete")
	}

	// Restore permissions and confirm a re-run now succeeds (re-untrackable).
	if err := os.Chmod(overlayParent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunUntrack(
		[]string{"CLAUDE.md"}, storeDir, "test-machine",
		true, true, false,
		strings.NewReader(""), io.Discard,
	); err != nil {
		t.Fatalf("re-running untrack after restoring permissions should succeed: %v", err)
	}
	if _, statErr := os.Lstat(claudeMd); !os.IsNotExist(statErr) {
		t.Error("CLAUDE.md should be gone after the successful re-run")
	}
}

// Declining the confirmation prompt (no --yes) must abort without changing the file.
func TestRunUntrack_DeclineConfirmation(t *testing.T) {
	// Not parallel — uses os.Chdir.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	projectDir, storeDir, overlayPath, _ := setupTrackedFile(t)
	defer func() { _ = os.Chdir(orig) }()

	claudeMd := filepath.Join(projectDir, "CLAUDE.md")

	// deleteMode, no --yes, answer "n" → aborted.
	var out bytes.Buffer
	if err := cmd.RunUntrack(
		[]string{"CLAUDE.md"}, storeDir, "test-machine",
		true /* delete */, false /* yes */, false,
		strings.NewReader("n\n"), &out,
	); err != nil {
		t.Fatalf("RunUntrack(decline) error = %v", err)
	}

	if !strings.Contains(out.String(), "Aborted") {
		t.Errorf("expected 'Aborted' in output, got: %s", out.String())
	}
	// File must still be a symlink and overlay must still exist.
	fi, lstatErr := os.Lstat(claudeMd)
	if lstatErr != nil {
		t.Fatalf("Lstat CLAUDE.md: %v", lstatErr)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("declined untrack should leave CLAUDE.md a symlink")
	}
	if _, statErr := os.Stat(overlayPath); statErr != nil {
		t.Errorf("declined untrack should leave the overlay intact: %v", statErr)
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

// setupTrackedTree creates a project tracking CLAUDE.md and docs/AGENTS.md, plus
// an untracked regular README.md. CWD is set to the project; callers must defer
// restoring the original CWD. Returns (projectDir, storeDir).
func setupTrackedTree(t *testing.T) (projectDir, storeDir string) {
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

	if err := os.MkdirAll(filepath.Join(projectDir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"CLAUDE.md":      "# claude\n",
		"docs/AGENTS.md": "# agents\n",
		"README.md":      "# readme (untracked)\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(projectDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunTrack(
		[]string{"CLAUDE.md", "docs/AGENTS.md"}, storeDir, "test-machine", false, io.Discard,
	); err != nil {
		t.Fatalf("RunTrack setup: %v", err)
	}
	return projectDir, storeDir
}

func isSymlink(t *testing.T, path string) bool {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat %s: %v", path, err)
	}
	return fi.Mode()&os.ModeSymlink != 0
}

// A directory argument untracks every tracked file beneath it and leaves
// untracked regular files alone.
func TestRunUntrack_Directory_RestoresAllTracked(t *testing.T) {
	// Not parallel — uses os.Chdir.

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	projectDir, storeDir := setupTrackedTree(t)
	defer func() { _ = os.Chdir(orig) }()

	// Precondition: tracked files are symlinks.
	if !isSymlink(t, filepath.Join(projectDir, "CLAUDE.md")) {
		t.Fatal("precondition: CLAUDE.md should be a symlink before untrack")
	}

	if err := cmd.RunUntrack(
		[]string{"."}, storeDir, "test-machine",
		false, true, false,
		strings.NewReader(""), io.Discard,
	); err != nil {
		t.Fatalf("RunUntrack(.) error = %v", err)
	}

	// Both tracked files restored to regular files.
	for _, name := range []string{"CLAUDE.md", filepath.Join("docs", "AGENTS.md")} {
		if isSymlink(t, filepath.Join(projectDir, name)) {
			t.Errorf("%s should be a regular file after `untrack .`, still a symlink", name)
		}
	}

	// The untracked regular file is left untouched (no error, not removed).
	if isSymlink(t, filepath.Join(projectDir, "README.md")) {
		t.Error("README.md should remain a regular file")
	}
}

// A directory with no tracked files is a no-op with a clear message, not an error.
func TestRunUntrack_Directory_NoTrackedFilesIsNoOp(t *testing.T) {
	// Not parallel — uses os.Chdir.

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	projectDir, storeDir := setupTrackedTree(t)
	defer func() { _ = os.Chdir(orig) }()

	plain := filepath.Join(projectDir, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plain, "notes.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cmd.RunUntrack(
		[]string{"plain"}, storeDir, "test-machine",
		false, true, false,
		strings.NewReader(""), &out,
	); err != nil {
		t.Fatalf("RunUntrack(plain) error = %v", err)
	}

	if !strings.Contains(out.String(), "No tracked files found") {
		t.Errorf("expected a no-op message, got: %q", out.String())
	}
	// Tracked files elsewhere remain untouched.
	if !isSymlink(t, filepath.Join(projectDir, "CLAUDE.md")) {
		t.Error("CLAUDE.md should still be a symlink after untracking an unrelated dir")
	}
}

// A directory target outside the project root is refused, leaving every tracked
// file untouched — the walk must not reach into the project from an ancestor dir.
func TestRunUntrack_Directory_OutsideProjectRootRefused(t *testing.T) {
	// Not parallel — uses os.Chdir.

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	projectDir, storeDir := setupTrackedTree(t)
	defer func() { _ = os.Chdir(orig) }()

	claudeMd := filepath.Join(projectDir, "CLAUDE.md")
	agentsMd := filepath.Join(projectDir, "docs", "AGENTS.md")

	err = cmd.RunUntrack(
		[]string{".."}, storeDir, "test-machine",
		false, true, false,
		strings.NewReader(""), io.Discard,
	)
	if err == nil {
		t.Fatal("expected an error untracking a directory outside the project root, got nil")
	}
	if !strings.Contains(err.Error(), "outside the project root") {
		t.Errorf("error = %q, want it to mention the project-root boundary", err.Error())
	}

	// Every tracked file must still be a symlink — nothing was untracked.
	if !isSymlink(t, claudeMd) {
		t.Error("CLAUDE.md should still be a symlink after a refused `untrack ..`")
	}
	if !isSymlink(t, agentsMd) {
		t.Error("docs/AGENTS.md should still be a symlink after a refused `untrack ..`")
	}
}

// A subdirectory argument untracks only the tracked files beneath it, leaving
// tracked files elsewhere in the project alone.
func TestRunUntrack_Directory_SubdirScope(t *testing.T) {
	// Not parallel — uses os.Chdir.

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	projectDir, storeDir := setupTrackedTree(t)
	defer func() { _ = os.Chdir(orig) }()

	if err := cmd.RunUntrack(
		[]string{"docs"}, storeDir, "test-machine",
		false, true, false,
		strings.NewReader(""), io.Discard,
	); err != nil {
		t.Fatalf("RunUntrack(docs) error = %v", err)
	}

	if isSymlink(t, filepath.Join(projectDir, "docs", "AGENTS.md")) {
		t.Error("docs/AGENTS.md should be restored (not a symlink)")
	}
	if !isSymlink(t, filepath.Join(projectDir, "CLAUDE.md")) {
		t.Error("CLAUDE.md should remain a symlink — only docs/ was untracked")
	}
}

package cmd_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/cmd"
)

// trackFileInNewProject creates a fresh project repo under base/<name>, writes
// <file>, tracks it on machine "test-machine", and returns the project dir and
// the tracked file's absolute path. It leaves CWD inside the new project; the
// caller is expected to defer restoring the original CWD.
func trackFileInNewProject(t *testing.T, storeDir, base, name, file, content string) (projDir, filePath string) {
	t.Helper()
	projDir = filepath.Join(base, name)
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeProjectRepo(t, projDir)

	filePath = filepath.Join(projDir, file)
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projDir); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunTrack([]string{file}, storeDir, "test-machine", false, io.Discard); err != nil {
		t.Fatalf("RunTrack in %s: %v", name, err)
	}
	return projDir, filePath
}

func newStore(t *testing.T) (base, storeDir string) {
	t.Helper()
	base = t.TempDir()
	storeDir = filepath.Join(base, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeStoreRepo(t, storeDir)
	return base, storeDir
}

// The roadmap integration anchor: files tracked across two projects, `reset`
// restores both working trees and empties the registry and store.
func TestRunReset_RestoresAllProjectsOnMachine(t *testing.T) {
	// Not parallel — uses os.Chdir.

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	base, storeDir := newStore(t)
	projA, claudeA := trackFileInNewProject(t, storeDir, base, "projA", "CLAUDE.md", "# A\n")
	_, claudeB := trackFileInNewProject(t, storeDir, base, "projB", "CLAUDE.md", "# B\n")

	if !isSymlink(t, claudeA) || !isSymlink(t, claudeB) {
		t.Fatal("precondition: both tracked files should be symlinks")
	}

	var out bytes.Buffer
	if err := cmd.RunReset(storeDir, "test-machine", true, false, strings.NewReader(""), &out); err != nil {
		t.Fatalf("RunReset error = %v", err)
	}

	// Both working trees hold real files again.
	for _, p := range []string{claudeA, claudeB} {
		if isSymlink(t, p) {
			t.Errorf("%s should be a real file after reset, still a symlink", p)
		}
		if _, statErr := os.Stat(p); statErr != nil {
			t.Errorf("restored file %s missing: %v", p, statErr)
		}
	}

	// Exclude entry stripped in projA.
	excludeA, _ := os.ReadFile(filepath.Join(projA, ".git", "info", "exclude"))
	if strings.Contains(string(excludeA), "CLAUDE.md") {
		t.Errorf("projA .git/info/exclude should no longer mention CLAUDE.md:\n%s", excludeA)
	}

	// Registry empty.
	reg := reloadRegistry(t, storeDir)
	if len(reg.Projects) != 0 {
		t.Errorf("registry should have no projects after reset, has %d", len(reg.Projects))
	}

	// Store overlays gone.
	entries, _ := os.ReadDir(filepath.Join(storeDir, "repos"))
	if len(entries) != 0 {
		t.Errorf("store repos/ should be empty after reset, has %d entries", len(entries))
	}
}

func TestRunReset_DryRunChangesNothing(t *testing.T) {
	// Not parallel — uses os.Chdir.

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	base, storeDir := newStore(t)
	_, claude := trackFileInNewProject(t, storeDir, base, "projA", "CLAUDE.md", "# A\n")

	var out bytes.Buffer
	if err := cmd.RunReset(storeDir, "test-machine", true, true, strings.NewReader(""), &out); err != nil {
		t.Fatalf("RunReset(dry-run) error = %v", err)
	}

	if !strings.Contains(out.String(), "dry-run") {
		t.Errorf("expected a dry-run message, got: %q", out.String())
	}
	if !isSymlink(t, claude) {
		t.Error("dry-run: file should still be a symlink")
	}
	if reg := reloadRegistry(t, storeDir); len(reg.Projects) != 1 {
		t.Errorf("dry-run: registry should be unchanged (1 project), has %d", len(reg.Projects))
	}
}

// Projects not checked out on the current machine are skipped and left intact.
func TestRunReset_SkipsProjectsNotOnThisMachine(t *testing.T) {
	// Not parallel — uses os.Chdir.

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	base, storeDir := newStore(t)
	_, claude := trackFileInNewProject(t, storeDir, base, "projA", "CLAUDE.md", "# A\n")

	// Reset as a different machine — the only project belongs to "test-machine".
	var out bytes.Buffer
	if err := cmd.RunReset(storeDir, "other-machine", true, false, strings.NewReader(""), &out); err != nil {
		t.Fatalf("RunReset error = %v", err)
	}

	if !strings.Contains(out.String(), "nothing to reset") {
		t.Errorf("expected a nothing-to-reset message, got: %q", out.String())
	}
	if !isSymlink(t, claude) {
		t.Error("file tracked on another machine should be left untouched")
	}
	if reg := reloadRegistry(t, storeDir); len(reg.Projects) != 1 {
		t.Errorf("registry should still hold the other machine's project, has %d", len(reg.Projects))
	}
}

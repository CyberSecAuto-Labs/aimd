package cmd_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/cmd"
	"github.com/CyberSecAuto-Labs/aimd/internal/exclude"
)

// excludeEntry adds relPath to a project's .git/info/exclude in the managed
// block, the same shape `aimd track` produces.
func excludeEntry(t *testing.T, projectDir, relPath string) {
	t.Helper()
	excludePath := filepath.Join(projectDir, ".git", "info", "exclude")
	if err := exclude.AppendEntry(excludePath, relPath); err != nil {
		t.Fatalf("exclude.AppendEntry: %v", err)
	}
}

// healthyProject builds a project + store where CLAUDE.md is fully wired:
// overlay in store, symlink in project, exclude entry present. Returns the
// project and store directories.
func healthyProject(t *testing.T) (projectDir, storeDir string) {
	t.Helper()
	base := t.TempDir()
	projectDir = filepath.Join(base, "project")
	storeDir = filepath.Join(base, "store")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	makeProjectWithRemote(t, projectDir)
	makeStatusStore(t, storeDir, []statusProject{{
		key: "github.com~test~myapp", display: "myapp", root: projectDir,
		tracked:  []string{"CLAUDE.md"},
		machines: map[string]string{"this-machine": projectDir},
	}})
	symlinkOverlay(t, storeDir, "github.com~test~myapp", projectDir, "CLAUDE.md")
	excludeEntry(t, projectDir, "CLAUDE.md")
	return projectDir, storeDir
}

func TestRunDoctor_Healthy(t *testing.T) {
	// Not parallel — uses os.Chdir.
	projectDir, storeDir := healthyProject(t)
	chdir(t, projectDir)

	var out bytes.Buffer
	if err := cmd.RunDoctor(storeDir, "this-machine", false, &out); err != nil {
		t.Fatalf("RunDoctor on a healthy project: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "✓ CLAUDE.md") {
		t.Errorf("expected a passing CLAUDE.md row, got:\n%s", got)
	}
	if !strings.Contains(got, "All checks passed") {
		t.Errorf("expected the all-clear summary, got:\n%s", got)
	}
}

func TestRunDoctor_BrokenSymlink(t *testing.T) {
	// Not parallel — uses os.Chdir.
	projectDir, storeDir := healthyProject(t)
	// Remove the symlink so the resolve check fails.
	if err := os.Remove(filepath.Join(projectDir, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	chdir(t, projectDir)

	var out bytes.Buffer
	err := cmd.RunDoctor(storeDir, "this-machine", false, &out)
	if err == nil {
		t.Fatal("RunDoctor with a broken symlink: want error, got nil")
	}
	got := out.String()
	if !strings.Contains(got, "✗ CLAUDE.md") || !strings.Contains(got, "symlink") {
		t.Errorf("expected a failing symlink row, got:\n%s", got)
	}
	if !strings.Contains(got, "aimd restore") {
		t.Errorf("expected the restore fix suggestion, got:\n%s", got)
	}
}

func TestRunDoctor_MissingExcludeEntry(t *testing.T) {
	// Not parallel — uses os.Chdir.
	projectDir, storeDir := healthyProject(t)
	// Wipe the exclude file so the entry check fails (symlink stays valid).
	if err := os.Remove(filepath.Join(projectDir, ".git", "info", "exclude")); err != nil {
		t.Fatal(err)
	}
	chdir(t, projectDir)

	var out bytes.Buffer
	err := cmd.RunDoctor(storeDir, "this-machine", false, &out)
	if err == nil {
		t.Fatal("RunDoctor with a missing exclude entry: want error, got nil")
	}
	got := out.String()
	if !strings.Contains(got, "✗ CLAUDE.md") || !strings.Contains(got, "exclude") {
		t.Errorf("expected a failing exclude row, got:\n%s", got)
	}
}

func TestRunDoctor_MissingOverlay(t *testing.T) {
	// Not parallel — uses os.Chdir.
	projectDir, storeDir := healthyProject(t)
	// Delete the overlay so registry and store disagree.
	if err := os.Remove(filepath.Join(storeDir, "repos", "github.com~test~myapp", "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	chdir(t, projectDir)

	var out bytes.Buffer
	err := cmd.RunDoctor(storeDir, "this-machine", false, &out)
	if err == nil {
		t.Fatal("RunDoctor with a missing overlay: want error, got nil")
	}
	got := out.String()
	if !strings.Contains(got, "✗ CLAUDE.md") || !strings.Contains(got, "missing from store") {
		t.Errorf("expected a store-consistency failure, got:\n%s", got)
	}
}

func TestRunDoctor_NoProjects(t *testing.T) {
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
	makeProjectWithRemote(t, projectDir)
	makeStatusStore(t, storeDir, nil)
	chdir(t, projectDir)

	var out bytes.Buffer
	if err := cmd.RunDoctor(storeDir, "this-machine", false, &out); err != nil {
		t.Fatalf("RunDoctor with no tracked projects: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "No projects tracked") {
		t.Errorf("expected the empty-state message, got:\n%s", got)
	}
}

// TestRunDoctor_All_NotCheckedOut proves --all reports a project that this
// machine has never checked out: the symlink/exclude checks are skipped, but
// store consistency still confirms the overlay is present.
func TestRunDoctor_All_NotCheckedOut(t *testing.T) {
	base := t.TempDir()
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Machine map deliberately omits "this-machine" so its root resolves empty.
	makeStatusStore(t, storeDir, []statusProject{{
		key: "github.com~test~other", display: "other", root: "",
		tracked:  []string{"CLAUDE.md"},
		machines: map[string]string{"other-machine": "/elsewhere/other"},
	}})

	var out bytes.Buffer
	if err := cmd.RunDoctor(storeDir, "this-machine", true, &out); err != nil {
		t.Fatalf("RunDoctor --all on a non-checked-out project: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "not checked out on this machine") {
		t.Errorf("expected the not-checked-out note, got:\n%s", got)
	}
	if !strings.Contains(got, "✓ CLAUDE.md") {
		t.Errorf("expected the overlay-present row, got:\n%s", got)
	}
}

// TestRunDoctor_RemoteReachable exercises the OK branch of the reachability
// check against a real bare-repo origin.
func TestRunDoctor_RemoteReachable(t *testing.T) {
	base := t.TempDir()
	bareDir := filepath.Join(base, "origin.git")
	storeDir := filepath.Join(base, "store")

	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "--bare", bareDir).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	makeStatusStore(t, storeDir, []statusProject{{
		key: "github.com~test~myapp", display: "myapp", root: "",
		tracked:  []string{"CLAUDE.md"},
		machines: map[string]string{"other-machine": "/elsewhere/myapp"},
	}})
	// Wire origin and publish main so `git fetch --dry-run origin main` succeeds.
	runGit(t, [][]string{
		{"git", "-C", storeDir, "remote", "add", "origin", bareDir},
		{"git", "-C", storeDir, "push", "origin", "HEAD:main"},
	})

	var out bytes.Buffer
	if err := cmd.RunDoctor(storeDir, "this-machine", true, &out); err != nil {
		t.Fatalf("RunDoctor with a reachable remote: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "✓ remote reachable") {
		t.Errorf("expected a passing reachability check, got:\n%s", got)
	}
}

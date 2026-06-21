package cmd_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/cmd"
)

// TestRunStatus_All_CompactRoster proves `status --all` defaults to a one-line
// per-project roster (worst-state glyph + file count) and hides per-file rows,
// while -v restores the full detail.
func TestRunStatus_All_CompactRoster(t *testing.T) {
	base := t.TempDir()
	storeDir := filepath.Join(base, "store")
	appA := filepath.Join(base, "appA") // synced
	appB := filepath.Join(base, "appB") // broken (no symlink created)
	for _, d := range []string{storeDir, appA, appB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	makeStatusStore(t, storeDir, []statusProject{
		{key: "github.com~test~appA", display: "appA", root: appA,
			tracked: []string{"CLAUDE.md"}, machines: map[string]string{"this-machine": appA}},
		{key: "github.com~test~appB", display: "appB", root: appB,
			tracked: []string{"AGENTS.md"}, machines: map[string]string{"this-machine": appB}},
	})
	symlinkOverlay(t, storeDir, "github.com~test~appA", appA, "CLAUDE.md")

	// Default --all: compact roster.
	var out bytes.Buffer
	if err := cmd.RunStatus(storeDir, "this-machine", true, false, false, false, &out); err != nil {
		t.Fatalf("RunStatus --all: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "✓ appA (1 file)") {
		t.Errorf("expected compact synced roster line for appA, got:\n%s", got)
	}
	if !strings.Contains(got, "✗ appB (1 file)") {
		t.Errorf("expected compact broken roster line for appB (worst-state glyph), got:\n%s", got)
	}
	if strings.Contains(got, "✓ CLAUDE.md") {
		t.Errorf("compact roster must not print per-file rows, got:\n%s", got)
	}
	if !strings.Contains(got, "watch: not running") {
		t.Errorf("expected watch-status line in the header, got:\n%s", got)
	}

	// -v restores the per-file detail.
	var vout bytes.Buffer
	if err := cmd.RunStatus(storeDir, "this-machine", true, false, false, true, &vout); err != nil {
		t.Fatalf("RunStatus --all -v: %v", err)
	}
	vgot := vout.String()
	if !strings.Contains(vgot, "✓ CLAUDE.md") {
		t.Errorf("verbose --all must show per-file rows, got:\n%s", vgot)
	}
	if strings.Contains(vgot, "✓ appA (1 file)") {
		t.Errorf("verbose --all must not use the compact roster, got:\n%s", vgot)
	}
}

// restoreAllProjects fixture: two projects checked out on this machine, each a
// real git repo (for the .git/info/exclude path) with an overlay in the store.
func makeRestoreAllFixture(t *testing.T) (storeDir, appA, appB string) {
	t.Helper()
	base := t.TempDir()
	storeDir = filepath.Join(base, "store")
	appA = filepath.Join(base, "appA")
	appB = filepath.Join(base, "appB")
	for _, d := range []string{storeDir, appA, appB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	makeProjectRepo(t, appA)
	makeProjectRepo(t, appB)
	makeStatusStore(t, storeDir, []statusProject{
		{key: "github.com~test~appA", display: "appA", root: appA,
			tracked: []string{"CLAUDE.md"}, machines: map[string]string{"this-machine": appA}},
		{key: "github.com~test~appB", display: "appB", root: appB,
			tracked: []string{"AGENTS.md"}, machines: map[string]string{"this-machine": appB}},
	})
	return storeDir, appA, appB
}

// TestRunRestore_All rehydrates every project checked out on this machine in one
// pass — the new-machine onboarding flow.
func TestRunRestore_All(t *testing.T) {
	storeDir, appA, appB := makeRestoreAllFixture(t)

	var out bytes.Buffer
	if err := cmd.RunRestore(storeDir, "this-machine", true, false, false, &out); err != nil {
		t.Fatalf("RunRestore --all: %v", err)
	}

	for _, p := range []struct{ dir, file string }{{appA, "CLAUDE.md"}, {appB, "AGENTS.md"}} {
		dst := filepath.Join(p.dir, p.file)
		fi, err := os.Lstat(dst)
		if err != nil {
			t.Fatalf("Lstat %s: %v", dst, err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("expected %s to be a symlink after restore --all", dst)
		}
	}
	if !strings.Contains(out.String(), "across 2 project(s)") {
		t.Errorf("expected the all-projects summary, got:\n%s", out.String())
	}
}

// TestRunRestore_All_DryRun previews the work without touching any working tree.
func TestRunRestore_All_DryRun(t *testing.T) {
	storeDir, appA, _ := makeRestoreAllFixture(t)

	var out bytes.Buffer
	if err := cmd.RunRestore(storeDir, "this-machine", true, false, true, &out); err != nil {
		t.Fatalf("RunRestore --all --dry-run: %v", err)
	}
	if !strings.Contains(out.String(), "dry-run: would restore 2 file(s) across 2 project(s)") {
		t.Errorf("expected dry-run summary, got:\n%s", out.String())
	}
	if _, err := os.Lstat(filepath.Join(appA, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Errorf("dry-run must not create symlinks, but appA/CLAUDE.md exists")
	}
}

// TestRunRestore_All_NothingCheckedOutHere covers a registry whose only project
// lives on another machine: restore --all has nothing to do here.
func TestRunRestore_All_NothingCheckedOutHere(t *testing.T) {
	base := t.TempDir()
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeStatusStore(t, storeDir, []statusProject{
		{key: "github.com~test~appA", display: "appA", root: "",
			tracked: []string{"CLAUDE.md"}, machines: map[string]string{"work-desktop": "/other/appA"}},
	})

	var out bytes.Buffer
	if err := cmd.RunRestore(storeDir, "this-machine", true, false, false, &out); err != nil {
		t.Fatalf("RunRestore --all: %v", err)
	}
	if !strings.Contains(out.String(), "nothing to restore") {
		t.Errorf("expected nothing-to-restore message, got:\n%s", out.String())
	}
}

// TestRunTrack_PrintsSyncHint proves tracking points the user at how edits
// propagate, so the command isn't a dead end.
func TestRunTrack_PrintsSyncHint(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(projectDir, "CLAUDE.md"), []byte("# ctx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, projectDir)

	var out bytes.Buffer
	if err := cmd.RunTrack([]string{"CLAUDE.md"}, storeDir, "test-machine", false, &out); err != nil {
		t.Fatalf("RunTrack: %v", err)
	}
	if !strings.Contains(out.String(), "aimd watch") {
		t.Errorf("expected the watch/sync hint after tracking, got:\n%s", out.String())
	}
}

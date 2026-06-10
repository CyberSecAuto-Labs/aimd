package cmd_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/cmd"
	"github.com/CyberSecAuto-Labs/aimd/internal/store"
)

// aimdCommitSeq makes each aimdCommit overlay write unique so git always has a
// change to stage, even for repeated identical verbs.
var aimdCommitSeq atomic.Int64

// aimdCommit produces a real trailer-bearing store commit for the given project
// by touching its overlay so there is something to stage, then calling
// store.Commit (the same path the live commands use).
func aimdCommit(t *testing.T, storeDir, key, root, verb, machine string, files []string) {
	t.Helper()
	overlay := filepath.Join(storeDir, "repos", key, "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(overlay), 0o755); err != nil {
		t.Fatalf("mkdir overlay: %v", err)
	}
	// Unique content each call so git always has a change to commit.
	content := fmt.Sprintf("# %s %s %d\n", verb, machine, aimdCommitSeq.Add(1))
	if err := os.WriteFile(overlay, []byte(content), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	// store.Commit stages metadata/<key>.json, so it must exist.
	meta := filepath.Join(storeDir, "metadata", key+".json")
	if err := os.MkdirAll(filepath.Dir(meta), 0o755); err != nil {
		t.Fatalf("mkdir metadata: %v", err)
	}
	if err := os.WriteFile(meta, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if err := store.Commit(storeDir, key, root, verb, machine, files); err != nil {
		t.Fatalf("store.Commit(%s): %v", verb, err)
	}
}

func TestRunLog_SingleProject(t *testing.T) {
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
	makeStatusStore(t, storeDir, []statusProject{{
		key: "github.com~test~myapp", display: "myapp", root: projectDir,
		tracked:  []string{"CLAUDE.md"},
		machines: map[string]string{"this-machine": projectDir},
	}})

	const key = "github.com~test~myapp"
	aimdCommit(t, storeDir, key, projectDir, "track", "laptop", []string{"CLAUDE.md", "AGENTS.md"})
	aimdCommit(t, storeDir, key, projectDir, "sync", "laptop", nil)

	chdir(t, projectDir)

	var out bytes.Buffer
	if err := cmd.RunLog(storeDir, false, 0, &out); err != nil {
		t.Fatalf("RunLog: %v", err)
	}
	got := out.String()

	for _, want := range []string{"aimd log • myapp", "tracked", "CLAUDE.md, AGENTS.md", "synced", "(laptop)"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n---\n%s", want, got)
		}
	}
}

func TestRunLog_All(t *testing.T) {
	base := t.TempDir()
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	makeStatusStore(t, storeDir, []statusProject{
		{key: "github.com~test~alpha", display: "alpha", root: "/p/alpha",
			tracked: []string{"CLAUDE.md"}, machines: map[string]string{"m": "/p/alpha"}},
		{key: "github.com~test~beta", display: "beta", root: "/p/beta",
			tracked: []string{"CLAUDE.md"}, machines: map[string]string{"m": "/p/beta"}},
	})

	aimdCommit(t, storeDir, "github.com~test~alpha", "/p/alpha", "track", "laptop", []string{"CLAUDE.md"})
	aimdCommit(t, storeDir, "github.com~test~beta", "/p/beta", "track", "desktop", []string{"CLAUDE.md"})

	var out bytes.Buffer
	if err := cmd.RunLog(storeDir, true, 0, &out); err != nil {
		t.Fatalf("RunLog: %v", err)
	}
	got := out.String()

	for _, want := range []string{"aimd log • all projects", "alpha", "beta", "(laptop)", "(desktop)"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n---\n%s", want, got)
		}
	}
}

func TestRunLog_Limit(t *testing.T) {
	base := t.TempDir()
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	const key = "github.com~test~myapp"
	makeStatusStore(t, storeDir, []statusProject{{
		key: key, display: "myapp", root: "/p/myapp",
		tracked: []string{"CLAUDE.md"}, machines: map[string]string{"m": "/p/myapp"},
	}})

	aimdCommit(t, storeDir, key, "/p/myapp", "track", "laptop", []string{"CLAUDE.md"})
	aimdCommit(t, storeDir, key, "/p/myapp", "sync", "laptop", nil)
	aimdCommit(t, storeDir, key, "/p/myapp", "sync", "laptop", nil)

	var out bytes.Buffer
	if err := cmd.RunLog(storeDir, true, 2, &out); err != nil {
		t.Fatalf("RunLog: %v", err)
	}

	// Count the indented entry lines (they start with two spaces).
	n := 0
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "  ") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("got %d entry lines, want 2 (limit)\n---\n%s", n, out.String())
	}
}

func TestRunLog_EmptyHistory(t *testing.T) {
	base := t.TempDir()
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	makeStatusStore(t, storeDir, []statusProject{{
		key: "github.com~test~myapp", display: "myapp", root: "/p/myapp",
		tracked: []string{"CLAUDE.md"}, machines: map[string]string{"m": "/p/myapp"},
	}})

	// No aimd overlay-change commits — only the scaffold commit exists.
	var out bytes.Buffer
	if err := cmd.RunLog(storeDir, true, 0, &out); err != nil {
		t.Fatalf("RunLog: %v", err)
	}
	if !strings.Contains(out.String(), "No history yet.") {
		t.Errorf("want 'No history yet.'\n---\n%s", out.String())
	}
}

func TestRunLog_LegacyCommit(t *testing.T) {
	base := t.TempDir()
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	const key = "github.com~test~myapp"
	makeStatusStore(t, storeDir, []statusProject{{
		key: key, display: "myapp", root: "/p/myapp",
		tracked: []string{"CLAUDE.md"}, machines: map[string]string{"m": "/p/myapp"},
	}})

	// A pre-trailer commit: aimd subject, no Aimd-* trailers.
	writeFile(t, filepath.Join(storeDir, "repos", key, "old.txt"), "old")
	runGit(t, [][]string{
		{"git", "-C", storeDir, "add", "."},
		{"git", "-C", storeDir, "-c", "user.email=aimd@localhost", "-c", "user.name=aimd",
			"commit", "-m", "untrack: myapp [oldbox 2026-01-01T00:00:00Z]"},
	})

	var out bytes.Buffer
	if err := cmd.RunLog(storeDir, true, 0, &out); err != nil {
		t.Fatalf("RunLog: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "untracked") {
		t.Errorf("legacy verb missing\n---\n%s", got)
	}
	if !strings.Contains(got, "(files not recorded)") {
		t.Errorf("legacy file fallback missing\n---\n%s", got)
	}
	if !strings.Contains(got, "(oldbox)") {
		t.Errorf("legacy machine missing\n---\n%s", got)
	}
}

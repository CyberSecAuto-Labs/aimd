package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/internal/store"
)

// bumpRegistry rewrites registry.json with new content so the next store.Commit
// has something staged to commit.
func bumpRegistry(t *testing.T, registryFile, content string) {
	t.Helper()
	if err := os.WriteFile(registryFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write registry.json: %v", err)
	}
}

// findEntry returns the first entry whose hash-independent verb+machine match,
// or fails the test.
func findEntry(t *testing.T, entries []store.LogEntry, verb, machine string) store.LogEntry {
	t.Helper()
	for _, e := range entries {
		if e.Verb == verb && e.Machine == machine {
			return e
		}
	}
	t.Fatalf("no log entry with verb=%q machine=%q in %+v", verb, machine, entries)
	return store.LogEntry{}
}

func TestLogTrailerRoundTrip(t *testing.T) {
	const projectKey = "github.com~org~myproject"
	storeDir, registryFile := setupStoreRepo(t, projectKey)

	bumpRegistry(t, registryFile, `{"v":1}`)
	files := []string{"CLAUDE.md", "AGENTS.md"}
	if err := store.Commit(storeDir, projectKey, "/home/user/myproject", "track", "laptop", files); err != nil {
		t.Fatalf("store.Commit: %v", err)
	}

	entries, err := store.Log(storeDir)
	if err != nil {
		t.Fatalf("store.Log: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 (the scaffold commit must be skipped): %+v", len(entries), entries)
	}

	e := entries[0]
	if e.Verb != "track" {
		t.Errorf("Verb = %q, want %q", e.Verb, "track")
	}
	if e.Machine != "laptop" {
		t.Errorf("Machine = %q, want %q", e.Machine, "laptop")
	}
	if e.ProjectKey != projectKey {
		t.Errorf("ProjectKey = %q, want %q", e.ProjectKey, projectKey)
	}
	if e.DisplayName != "myproject" {
		t.Errorf("DisplayName = %q, want %q", e.DisplayName, "myproject")
	}
	if e.Legacy {
		t.Error("Legacy = true, want false for a trailer-bearing commit")
	}
	if len(e.Files) != 2 || e.Files[0] != "CLAUDE.md" || e.Files[1] != "AGENTS.md" {
		t.Errorf("Files = %v, want [CLAUDE.md AGENTS.md]", e.Files)
	}
	if e.When.IsZero() {
		t.Error("When is zero, want the committer date")
	}
}

func TestLogNoFilesTrailerStillParses(t *testing.T) {
	const projectKey = "github.com~org~proj"
	storeDir, registryFile := setupStoreRepo(t, projectKey)

	bumpRegistry(t, registryFile, `{"v":2}`)
	if err := store.Commit(storeDir, projectKey, "/home/user/proj", "sync", "desktop", nil); err != nil {
		t.Fatalf("store.Commit: %v", err)
	}

	entries, err := store.Log(storeDir)
	if err != nil {
		t.Fatalf("store.Log: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(entries), entries)
	}
	e := entries[0]
	if e.Verb != "sync" || e.Machine != "desktop" || e.ProjectKey != projectKey {
		t.Errorf("unexpected entry: %+v", e)
	}
	if e.Legacy {
		t.Error("Legacy = true, want false")
	}
	if len(e.Files) != 0 {
		t.Errorf("Files = %v, want empty", e.Files)
	}
}

func TestLogMixedTrailerAndLegacy(t *testing.T) {
	const projectKey = "github.com~org~myproject"
	storeDir, registryFile := setupStoreRepo(t, projectKey)

	// A pre-trailer (legacy) commit: an aimd-style subject, no Aimd-* trailers.
	legacyFile := filepath.Join(storeDir, "repos", projectKey, "legacy.txt")
	if err := os.WriteFile(legacyFile, []byte("legacy"), 0o600); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}
	gitRun(t, storeDir, "add", ".")
	gitRun(t, storeDir, "-c", "user.email=aimd@localhost", "-c", "user.name=aimd",
		"commit", "-m", "untrack: myproject [oldmachine 2026-01-01T00:00:00Z]")

	// A modern trailer-bearing commit.
	bumpRegistry(t, registryFile, `{"v":3}`)
	if err := store.Commit(storeDir, projectKey, "/home/user/myproject", "track", "laptop", []string{"CLAUDE.md"}); err != nil {
		t.Fatalf("store.Commit: %v", err)
	}

	entries, err := store.Log(storeDir)
	if err != nil {
		t.Fatalf("store.Log: %v", err)
	}
	// Two aimd commits; the scaffold "initial" commit is skipped.
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}

	// Newest first: the trailer commit.
	if entries[0].Verb != "track" || entries[0].Legacy {
		t.Errorf("entries[0] = %+v, want non-legacy track", entries[0])
	}

	legacy := findEntry(t, entries, "untrack", "oldmachine")
	if !legacy.Legacy {
		t.Error("legacy entry Legacy = false, want true")
	}
	if legacy.ProjectKey != "" {
		t.Errorf("legacy ProjectKey = %q, want empty", legacy.ProjectKey)
	}
	if legacy.DisplayName != "myproject" {
		t.Errorf("legacy DisplayName = %q, want %q (parsed from subject)", legacy.DisplayName, "myproject")
	}
	if len(legacy.Files) != 0 {
		t.Errorf("legacy Files = %v, want empty (not recorded pre-trailer)", legacy.Files)
	}
}

func TestLogSkipsNonAimdCommits(t *testing.T) {
	const projectKey = "github.com~org~proj"
	storeDir, registryFile := setupStoreRepo(t, projectKey)

	// A hand-made commit that is not an aimd overlay change.
	other := filepath.Join(storeDir, "notes.txt")
	if err := os.WriteFile(other, []byte("hi"), 0o600); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	gitRun(t, storeDir, "add", ".")
	gitRun(t, storeDir, "-c", "user.email=a@b", "-c", "user.name=a",
		"commit", "-m", "random hand-made commit")

	bumpRegistry(t, registryFile, `{"v":9}`)
	if err := store.Commit(storeDir, projectKey, "/home/user/proj", "restore", "laptop", []string{"CLAUDE.md"}); err != nil {
		t.Fatalf("store.Commit: %v", err)
	}

	entries, err := store.Log(storeDir)
	if err != nil {
		t.Fatalf("store.Log: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 (scaffold + hand-made commit both skipped): %+v", len(entries), entries)
	}
	if entries[0].Verb != "restore" {
		t.Errorf("Verb = %q, want %q", entries[0].Verb, "restore")
	}
}

func TestLogEmptyStore(t *testing.T) {
	const projectKey = "github.com~org~proj"
	storeDir, _ := setupStoreRepo(t, projectKey)

	// Only the scaffold commit exists — no aimd overlay changes yet.
	entries, err := store.Log(storeDir)
	if err != nil {
		t.Fatalf("store.Log: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("got %d entries, want 0: %+v", len(entries), entries)
	}
}

package exclude_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/internal/exclude"
)

func excludePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), ".git", "info", "exclude")
}

// --- AppendEntry ---

func TestAppendEntry_CreatesFileAndDirs(t *testing.T) {
	path := excludePath(t)
	if err := exclude.AppendEntry(path, "CLAUDE.md"); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}
	ok, err := exclude.HasEntry(path, "CLAUDE.md")
	if err != nil {
		t.Fatalf("HasEntry: %v", err)
	}
	if !ok {
		t.Error("expected entry to be present after append")
	}
}

func TestAppendEntry_Idempotent(t *testing.T) {
	path := excludePath(t)
	for range 3 {
		if err := exclude.AppendEntry(path, "CLAUDE.md"); err != nil {
			t.Fatalf("AppendEntry: %v", err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	count := strings.Count(string(data), "CLAUDE.md")
	if count != 1 {
		t.Errorf("expected exactly 1 occurrence, got %d; content:\n%s", count, data)
	}
}

func TestAppendEntry_PreservesExistingEntries(t *testing.T) {
	path := excludePath(t)
	if err := exclude.AppendEntry(path, "first.md"); err != nil {
		t.Fatalf("first AppendEntry: %v", err)
	}
	if err := exclude.AppendEntry(path, "second.md"); err != nil {
		t.Fatalf("second AppendEntry: %v", err)
	}

	ok, err := exclude.HasEntry(path, "first.md")
	if err != nil || !ok {
		t.Errorf("expected first.md to remain; ok=%v err=%v", ok, err)
	}
	ok, err = exclude.HasEntry(path, "second.md")
	if err != nil || !ok {
		t.Errorf("expected second.md to be present; ok=%v err=%v", ok, err)
	}
}

func TestAppendEntry_NoTrailingNewlineSafe(t *testing.T) {
	// Write content without a trailing newline, then append.
	path := excludePath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("existing"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := exclude.AppendEntry(path, "CLAUDE.md"); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}
	ok, err := exclude.HasEntry(path, "CLAUDE.md")
	if err != nil || !ok {
		t.Errorf("expected CLAUDE.md to be present; ok=%v err=%v", ok, err)
	}
	// "existing" should still be there.
	ok, err = exclude.HasEntry(path, "existing")
	if err != nil || !ok {
		t.Errorf("expected existing to be preserved; ok=%v err=%v", ok, err)
	}
}

// --- HasEntry ---

func TestHasEntry_Present(t *testing.T) {
	path := excludePath(t)
	if err := exclude.AppendEntry(path, "CLAUDE.md"); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}
	ok, err := exclude.HasEntry(path, "CLAUDE.md")
	if err != nil {
		t.Fatalf("HasEntry: %v", err)
	}
	if !ok {
		t.Error("expected true, got false")
	}
}

func TestHasEntry_Absent(t *testing.T) {
	path := excludePath(t)
	if err := exclude.AppendEntry(path, "other.md"); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}
	ok, err := exclude.HasEntry(path, "CLAUDE.md")
	if err != nil {
		t.Fatalf("HasEntry: %v", err)
	}
	if ok {
		t.Error("expected false, got true")
	}
}

func TestHasEntry_FileNotExist(t *testing.T) {
	path := excludePath(t) // directory exists but file does not
	ok, err := exclude.HasEntry(path, "CLAUDE.md")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if ok {
		t.Error("expected false when file does not exist")
	}
}

// --- RemoveEntry ---

func TestRemoveEntry_EntryPresent(t *testing.T) {
	path := excludePath(t)
	if err := exclude.AppendEntry(path, "first.md"); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}
	if err := exclude.AppendEntry(path, "CLAUDE.md"); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}
	if err := exclude.AppendEntry(path, "last.md"); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}

	if err := exclude.RemoveEntry(path, "CLAUDE.md"); err != nil {
		t.Fatalf("RemoveEntry: %v", err)
	}

	ok, err := exclude.HasEntry(path, "CLAUDE.md")
	if err != nil || ok {
		t.Errorf("expected CLAUDE.md to be gone; ok=%v err=%v", ok, err)
	}
	// Other entries must remain.
	ok, err = exclude.HasEntry(path, "first.md")
	if err != nil || !ok {
		t.Errorf("expected first.md to remain; ok=%v err=%v", ok, err)
	}
	ok, err = exclude.HasEntry(path, "last.md")
	if err != nil || !ok {
		t.Errorf("expected last.md to remain; ok=%v err=%v", ok, err)
	}
}

func TestRemoveEntry_EntryAbsent(t *testing.T) {
	path := excludePath(t)
	if err := exclude.AppendEntry(path, "other.md"); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}
	if err := exclude.RemoveEntry(path, "CLAUDE.md"); err != nil {
		t.Fatalf("RemoveEntry on absent entry: %v", err)
	}
}

func TestRemoveEntry_FileNotExist(t *testing.T) {
	path := excludePath(t)
	if err := exclude.RemoveEntry(path, "CLAUDE.md"); err != nil {
		t.Fatalf("expected nil when file does not exist, got: %v", err)
	}
}

func TestRemoveEntry_MultipleOccurrences(t *testing.T) {
	path := excludePath(t)
	// Manually write a file with duplicate entries.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := "CLAUDE.md\nother.md\nCLAUDE.md\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := exclude.RemoveEntry(path, "CLAUDE.md"); err != nil {
		t.Fatalf("RemoveEntry: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(data), "CLAUDE.md") {
		t.Errorf("expected all CLAUDE.md occurrences removed; got:\n%s", data)
	}
	ok, err := exclude.HasEntry(path, "other.md")
	if err != nil || !ok {
		t.Errorf("expected other.md to remain; ok=%v err=%v", ok, err)
	}
}

// --- Multi-entry integration test ---

func TestMultiEntry_AppendHasRemove(t *testing.T) {
	path := excludePath(t)
	entries := []string{"CLAUDE.md", "context.md", "notes.md"}

	for _, e := range entries {
		if err := exclude.AppendEntry(path, e); err != nil {
			t.Fatalf("AppendEntry(%q): %v", e, err)
		}
	}

	// All must be present.
	for _, e := range entries {
		ok, err := exclude.HasEntry(path, e)
		if err != nil || !ok {
			t.Errorf("expected %q present; ok=%v err=%v", e, ok, err)
		}
	}

	// Remove the middle one.
	if err := exclude.RemoveEntry(path, "context.md"); err != nil {
		t.Fatalf("RemoveEntry: %v", err)
	}

	// Removed entry gone.
	ok, err := exclude.HasEntry(path, "context.md")
	if err != nil || ok {
		t.Errorf("expected context.md gone; ok=%v err=%v", ok, err)
	}

	// Others remain.
	for _, e := range []string{"CLAUDE.md", "notes.md"} {
		ok, err := exclude.HasEntry(path, e)
		if err != nil || !ok {
			t.Errorf("expected %q to remain; ok=%v err=%v", e, ok, err)
		}
	}
}

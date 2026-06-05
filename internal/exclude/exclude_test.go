package exclude_test

import (
	"os"
	"os/exec"
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
	// Write a bare user line without a trailing newline, then append. The managed
	// block must start on its own line and the user line must survive verbatim.
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

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// The user's bare line is preserved and not merged into the block marker.
	if !strings.HasPrefix(string(data), "existing\n") {
		t.Errorf("expected file to start with the preserved user line; got:\n%s", data)
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
	// Manually write a managed block that contains a duplicated entry — RemoveEntry
	// must strip every occurrence inside the block.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := "# >>> aimd managed block (do not edit by hand)\nCLAUDE.md\nother.md\nCLAUDE.md\n# <<< aimd managed block\n"
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

// RemoveEntry must write atomically (temp file + rename) and leave
// no leftover .tmp file behind.
func TestRemoveEntry_NoLeftoverTempFile(t *testing.T) {
	path := excludePath(t)
	if err := exclude.AppendEntry(path, "first.md"); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}
	if err := exclude.AppendEntry(path, "CLAUDE.md"); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}

	if err := exclude.RemoveEntry(path, "CLAUDE.md"); err != nil {
		t.Fatalf("RemoveEntry: %v", err)
	}

	// Content must be correct.
	ok, err := exclude.HasEntry(path, "CLAUDE.md")
	if err != nil || ok {
		t.Errorf("expected CLAUDE.md removed; ok=%v err=%v", ok, err)
	}
	ok, err = exclude.HasEntry(path, "first.md")
	if err != nil || !ok {
		t.Errorf("expected first.md preserved; ok=%v err=%v", ok, err)
	}

	// No leftover .tmp file in the directory.
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file present: %s", e.Name())
		}
	}
}

// file mode must be preserved as 0o644 after an atomic rewrite.
func TestRemoveEntry_PreservesMode(t *testing.T) {
	path := excludePath(t)
	if err := exclude.AppendEntry(path, "a.md"); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}
	if err := exclude.AppendEntry(path, "b.md"); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}

	if err := exclude.RemoveEntry(path, "a.md"); err != nil {
		t.Fatalf("RemoveEntry: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("file mode = %o, want 644", perm)
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

// --- Managed block ---

const (
	blockStart = "# >>> aimd managed block (do not edit by hand)"
	blockEnd   = "# <<< aimd managed block"
)

// AppendEntry must wrap entries in the delimited block and write the
// explanatory header on first creation.
func TestAppendEntry_WrapsInManagedBlock(t *testing.T) {
	path := excludePath(t)
	if err := exclude.AppendEntry(path, "CLAUDE.md"); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, blockStart) || !strings.Contains(s, blockEnd) {
		t.Errorf("expected managed-block delimiters; got:\n%s", s)
	}
	// A header line explaining the block must be present.
	if !strings.Contains(s, "managed by aimd") && !strings.Contains(s, "ABOVE or BELOW") {
		t.Errorf("expected explanatory header; got:\n%s", s)
	}
	// The entry sits between the markers.
	start := strings.Index(s, blockStart)
	end := strings.Index(s, blockEnd)
	if start < 0 || end < 0 || start > end {
		t.Fatalf("markers out of order; got:\n%s", s)
	}
	if !strings.Contains(s[start:end], "CLAUDE.md") {
		t.Errorf("expected CLAUDE.md inside the block; got:\n%s", s)
	}
}

// A user's hand-authored line outside the block must survive both append and
// removal of an aimd entry — this is the core shared-ownership safety property.
func TestManagedBlock_UserLinesUntouched(t *testing.T) {
	path := excludePath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Two user lines: one above where the block will be appended.
	if err := os.WriteFile(path, []byte("# my own notes\n.env.local\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := exclude.AppendEntry(path, "CLAUDE.md"); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}
	if err := exclude.RemoveEntry(path, "CLAUDE.md"); err != nil {
		t.Fatalf("RemoveEntry: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "# my own notes") || !strings.Contains(s, ".env.local") {
		t.Errorf("expected user lines to survive; got:\n%s", s)
	}
	// Block fully removed once emptied.
	if strings.Contains(s, blockStart) || strings.Contains(s, blockEnd) {
		t.Errorf("expected empty block to be removed; got:\n%s", s)
	}
}

// Removing the last entry removes the whole block (delimiters + header).
func TestRemoveEntry_RemovesEmptyBlock(t *testing.T) {
	path := excludePath(t)
	if err := exclude.AppendEntry(path, "only.md"); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}
	if err := exclude.RemoveEntry(path, "only.md"); err != nil {
		t.Fatalf("RemoveEntry: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(data), "aimd managed block") {
		t.Errorf("expected block markers gone; got:\n%s", data)
	}
}

// A bare line outside any managed block is the user's; RemoveEntry must not
// touch it, and HasEntry must not report it as an aimd entry.
func TestBareLine_NotManaged(t *testing.T) {
	path := excludePath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("CLAUDE.md\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ok, err := exclude.HasEntry(path, "CLAUDE.md")
	if err != nil {
		t.Fatalf("HasEntry: %v", err)
	}
	if ok {
		t.Error("expected bare line outside the block to NOT be reported as managed")
	}

	if err := exclude.RemoveEntry(path, "CLAUDE.md"); err != nil {
		t.Fatalf("RemoveEntry: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "CLAUDE.md") {
		t.Errorf("expected bare user line left untouched; got:\n%s", data)
	}
}

// --- CheckIgnore ---

func gitInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestCheckIgnore_IgnoredByUserPattern(t *testing.T) {
	dir := gitInit(t)
	excl := filepath.Join(dir, ".git", "info", "exclude")
	if err := os.WriteFile(excl, []byte(".context/\n"), 0o644); err != nil {
		t.Fatalf("WriteFile exclude: %v", err)
	}

	ignored, source, pattern, err := exclude.CheckIgnore(dir, ".context/notes.md")
	if err != nil {
		t.Fatalf("CheckIgnore: %v", err)
	}
	if !ignored {
		t.Fatal("expected path to be ignored by the user pattern")
	}
	if pattern != ".context/" {
		t.Errorf("pattern = %q, want %q", pattern, ".context/")
	}
	if !strings.Contains(source, "exclude") {
		t.Errorf("source = %q, want it to reference the exclude file", source)
	}
}

func TestCheckIgnore_NotIgnored(t *testing.T) {
	dir := gitInit(t)
	ignored, _, _, err := exclude.CheckIgnore(dir, "CLAUDE.md")
	if err != nil {
		t.Fatalf("CheckIgnore: %v", err)
	}
	if ignored {
		t.Error("expected path NOT to be ignored with an empty exclude file")
	}
}

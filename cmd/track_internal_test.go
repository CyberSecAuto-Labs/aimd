package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/internal/link"
	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
)

// expandTargets must never descend into a .git directory, otherwise
// `aimd track .` would relocate the repo's own git internals into the store.
func TestExpandTargets_SkipsVCSDirs(t *testing.T) {
	dir := t.TempDir()

	// A file we DO want to pick up.
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("ctx"), 0o644); err != nil {
		t.Fatal(err)
	}
	// .git internals we must NOT pick up.
	if err := os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"HEAD", "config", filepath.Join("objects", "abc")} {
		if err := os.WriteFile(filepath.Join(dir, ".git", f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	paths, err := expandTargets([]string{dir})
	if err != nil {
		t.Fatalf("expandTargets: %v", err)
	}

	gitSep := string(os.PathSeparator) + ".git" + string(os.PathSeparator)
	var foundClaude bool
	for _, p := range paths {
		if strings.Contains(p, gitSep) {
			t.Errorf("expandTargets returned a .git path: %s", p)
		}
		if strings.HasSuffix(p, string(os.PathSeparator)+"CLAUDE.md") {
			foundClaude = true
		}
	}
	if !foundClaude {
		t.Errorf("expandTargets dropped CLAUDE.md; got %v", paths)
	}
}

// an explicitly-named file inside a VCS directory must be rejected, otherwise
// `aimd track .git/config` would relocate the repo's git internals into the
// store and replace them with a symlink, corrupting the repository.
func TestExpandTargets_RejectsExplicitVCSTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitConfig := filepath.Join(dir, ".git", "config")
	if err := os.WriteFile(gitConfig, []byte("[core]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := expandTargets([]string{gitConfig}); err == nil {
		t.Fatal("expected expandTargets to reject a .git target, got nil error")
	}
}

// when CreateLink fails, trackFile must restore the original file from
// the in-memory copy rather than leaving the project with no file.
func TestTrackFile_RollbackRestoresOriginalOnLinkFailure(t *testing.T) {
	gitRoot := t.TempDir()
	storeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(storeDir, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}

	filePath := filepath.Join(gitRoot, "CLAUDE.md")
	content := []byte("important local context\n")
	if err := os.WriteFile(filePath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	proj := &registry.Project{Tracked: []registry.TrackedFile{}}

	// LinkModeCopy is unimplemented (CreateLink returns ErrNotImplemented), which
	// drives the rollback path.
	err := trackFile(filePath, gitRoot, "key", storeDir, "m", link.LinkModeCopy, proj, false, io.Discard)
	if err == nil {
		t.Fatal("expected an error when CreateLink fails, got nil")
	}

	// The original must have been restored.
	got, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatalf("original file lost after rollback: %v", readErr)
	}
	if string(got) != string(content) {
		t.Errorf("restored content = %q, want %q", got, content)
	}
	// And it must be a regular file again, not a broken symlink.
	fi, lstatErr := os.Lstat(filePath)
	if lstatErr != nil {
		t.Fatalf("Lstat: %v", lstatErr)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("expected a regular file after rollback, got a symlink")
	}
}

// an unknown or not-yet-implemented link mode must be rejected
// up front so callers fail fast before any destructive file operation.
func TestValidateLinkMode(t *testing.T) {
	if _, err := validateLinkMode(link.LinkModeSymlink); err != nil {
		t.Errorf("symlink should be valid, got %v", err)
	}
	for _, mode := range []link.LinkMode{link.LinkModeHardlink, link.LinkModeCopy, link.LinkMode("bogus")} {
		if _, err := validateLinkMode(mode); err == nil {
			t.Errorf("mode %q should be rejected, got nil error", mode)
		}
	}
}

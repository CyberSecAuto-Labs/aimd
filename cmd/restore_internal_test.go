package cmd

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/internal/link"
)

// restore --force must be copy-first — if CreateLink fails after the
// real file is removed, restoreFile must put the user's file back rather than
// leaving them with nothing.
func TestRestoreFile_ForceRollbackOnLinkFailure(t *testing.T) {
	dir := t.TempDir()

	overlaySrc := filepath.Join(dir, "overlay.md")
	if err := os.WriteFile(overlaySrc, []byte("overlay content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	projectDst := filepath.Join(dir, "CLAUDE.md")
	realContent := []byte("my unsaved local edits\n")
	if err := os.WriteFile(projectDst, realContent, 0o644); err != nil {
		t.Fatal(err)
	}

	// LinkModeCopy is unimplemented (CreateLink returns ErrNotImplemented) → the
	// force path removes the real file, fails to link, and must roll back.
	restored, err := restoreFile(overlaySrc, projectDst, "CLAUDE.md", link.LinkModeCopy, true, io.Discard)
	if err == nil {
		t.Fatal("expected an error when CreateLink fails under --force, got nil")
	}
	if restored {
		t.Error("restoreFile reported restored=true on failure")
	}

	got, readErr := os.ReadFile(projectDst)
	if readErr != nil {
		t.Fatalf("real file lost after rollback: %v", readErr)
	}
	if string(got) != string(realContent) {
		t.Errorf("rolled-back content = %q, want %q", got, realContent)
	}
	fi, lstatErr := os.Lstat(projectDst)
	if lstatErr != nil {
		t.Fatalf("Lstat: %v", lstatErr)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("expected a regular file after rollback, got a symlink")
	}
}

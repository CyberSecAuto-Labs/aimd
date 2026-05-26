package link_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/internal/link"
)

// helpers

// makeSrc creates a real file in dir and returns its path.
func makeSrc(t *testing.T, dir string) string {
	t.Helper()
	src := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(src, []byte("content"), 0o600); err != nil {
		t.Fatalf("creating source file: %v", err)
	}
	return src
}

// TestCreateLink_Symlink verifies that CreateLink creates a symlink that
// resolves to the source file.
func TestCreateLink_Symlink(t *testing.T) {
	dir := t.TempDir()
	src := makeSrc(t, dir)
	dest := filepath.Join(dir, "link")

	if err := link.CreateLink(src, dest, link.LinkModeSymlink); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	target, err := os.Readlink(dest)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if target != src {
		t.Errorf("symlink target = %q; want %q", target, src)
	}
}

// TestVerifyLink_Symlink_Valid verifies that a correct symlink is reported valid.
func TestVerifyLink_Symlink_Valid(t *testing.T) {
	dir := t.TempDir()
	src := makeSrc(t, dir)
	dest := filepath.Join(dir, "link")

	if err := link.CreateLink(src, dest, link.LinkModeSymlink); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	ok, err := link.VerifyLink(dest, src, link.LinkModeSymlink)
	if err != nil {
		t.Fatalf("VerifyLink: %v", err)
	}
	if !ok {
		t.Error("VerifyLink returned false for a valid symlink; want true")
	}
}

// TestVerifyLink_Symlink_Wrong verifies that a symlink pointing to the wrong
// target is reported invalid.
func TestVerifyLink_Symlink_Wrong(t *testing.T) {
	dir := t.TempDir()
	src := makeSrc(t, dir)
	other := filepath.Join(dir, "other.txt")
	if err := os.WriteFile(other, []byte("other"), 0o600); err != nil {
		t.Fatalf("creating other file: %v", err)
	}
	dest := filepath.Join(dir, "link")

	// Create symlink to src, but verify against other.
	if err := link.CreateLink(src, dest, link.LinkModeSymlink); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	ok, err := link.VerifyLink(dest, other, link.LinkModeSymlink)
	if err != nil {
		t.Fatalf("VerifyLink: %v", err)
	}
	if ok {
		t.Error("VerifyLink returned true for wrong target; want false")
	}
}

// TestVerifyLink_Symlink_Broken verifies that a symlink whose target has been
// deleted returns (false, nil) — a known-broken state, not an error.
func TestVerifyLink_Symlink_Broken(t *testing.T) {
	dir := t.TempDir()
	src := makeSrc(t, dir)
	dest := filepath.Join(dir, "link")

	if err := link.CreateLink(src, dest, link.LinkModeSymlink); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	// Delete the source to break the symlink.
	if err := os.Remove(src); err != nil {
		t.Fatalf("removing source: %v", err)
	}

	ok, err := link.VerifyLink(dest, src, link.LinkModeSymlink)
	if err != nil {
		t.Fatalf("VerifyLink on broken symlink returned error; want (false, nil): %v", err)
	}
	if ok {
		t.Error("VerifyLink returned true for broken symlink; want false")
	}
}

// TestVerifyLink_Symlink_Missing verifies that VerifyLink returns (false, nil)
// when the symlink does not exist at all.
func TestVerifyLink_Symlink_Missing(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "nonexistent")

	ok, err := link.VerifyLink(dest, "/some/src", link.LinkModeSymlink)
	if err != nil {
		t.Fatalf("VerifyLink on missing path returned error; want (false, nil): %v", err)
	}
	if ok {
		t.Error("VerifyLink returned true for nonexistent path; want false")
	}
}

// TestRemoveLink_Symlink verifies that RemoveLink deletes the symlink.
func TestRemoveLink_Symlink(t *testing.T) {
	dir := t.TempDir()
	src := makeSrc(t, dir)
	dest := filepath.Join(dir, "link")

	if err := link.CreateLink(src, dest, link.LinkModeSymlink); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	if err := link.RemoveLink(dest, link.LinkModeSymlink); err != nil {
		t.Fatalf("RemoveLink: %v", err)
	}

	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("symlink still exists after RemoveLink; Lstat err = %v", err)
	}
}

// TestCreateLink_Hardlink_NotImplemented verifies the hardlink stub.
func TestCreateLink_Hardlink_NotImplemented(t *testing.T) {
	err := link.CreateLink("/src", "/dest", link.LinkModeHardlink)
	if !errors.Is(err, link.ErrNotImplemented) {
		t.Errorf("CreateLink(hardlink) error = %v; want ErrNotImplemented", err)
	}
}

// TestCreateLink_Copy_NotImplemented verifies the copy stub.
func TestCreateLink_Copy_NotImplemented(t *testing.T) {
	err := link.CreateLink("/src", "/dest", link.LinkModeCopy)
	if !errors.Is(err, link.ErrNotImplemented) {
		t.Errorf("CreateLink(copy) error = %v; want ErrNotImplemented", err)
	}
}

// TestVerifyLink_Hardlink_NotImplemented verifies the hardlink stub.
func TestVerifyLink_Hardlink_NotImplemented(t *testing.T) {
	_, err := link.VerifyLink("/dest", "/src", link.LinkModeHardlink)
	if !errors.Is(err, link.ErrNotImplemented) {
		t.Errorf("VerifyLink(hardlink) error = %v; want ErrNotImplemented", err)
	}
}

// TestVerifyLink_Copy_NotImplemented verifies the copy stub.
func TestVerifyLink_Copy_NotImplemented(t *testing.T) {
	_, err := link.VerifyLink("/dest", "/src", link.LinkModeCopy)
	if !errors.Is(err, link.ErrNotImplemented) {
		t.Errorf("VerifyLink(copy) error = %v; want ErrNotImplemented", err)
	}
}

// TestRemoveLink_Hardlink_NotImplemented verifies the hardlink stub.
func TestRemoveLink_Hardlink_NotImplemented(t *testing.T) {
	err := link.RemoveLink("/dest", link.LinkModeHardlink)
	if !errors.Is(err, link.ErrNotImplemented) {
		t.Errorf("RemoveLink(hardlink) error = %v; want ErrNotImplemented", err)
	}
}

// TestRemoveLink_Copy_NotImplemented verifies the copy stub.
func TestRemoveLink_Copy_NotImplemented(t *testing.T) {
	err := link.RemoveLink("/dest", link.LinkModeCopy)
	if !errors.Is(err, link.ErrNotImplemented) {
		t.Errorf("RemoveLink(copy) error = %v; want ErrNotImplemented", err)
	}
}

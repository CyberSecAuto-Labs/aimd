package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/internal/config"
)

func TestSaveAndLoad_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")

	want := &config.Config{
		Remote:      "git@github.com:user/store.git",
		MachineName: "test-machine",
		LinkMode:    "symlink",
	}

	if err := config.Save(path, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got.Remote != want.Remote {
		t.Errorf("Remote = %q, want %q", got.Remote, want.Remote)
	}
	if got.MachineName != want.MachineName {
		t.Errorf("MachineName = %q, want %q", got.MachineName, want.MachineName)
	}
	if got.LinkMode != want.LinkMode {
		t.Errorf("LinkMode = %q, want %q", got.LinkMode, want.LinkMode)
	}
}

func TestLoad_NotFound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent-config")

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load() expected ErrNotFound, got nil")
	}
	if !errors.Is(err, config.ErrNotFound) {
		t.Errorf("Load() error = %v, want ErrNotFound", err)
	}
}

func TestSave_CreatesParentDirectories(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Nested path that doesn't exist yet.
	path := filepath.Join(dir, "nested", "dir", "config")

	cfg := &config.Config{
		Remote:      "git@github.com:user/store.git",
		MachineName: "machine",
		LinkMode:    "symlink",
	}

	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("config file not found after Save(): %v", err)
	}
}

func TestSave_AtomicWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")

	cfg := &config.Config{
		Remote:      "git@github.com:user/store.git",
		MachineName: "machine",
		LinkMode:    "symlink",
	}

	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify: final file exists, no temp files remain.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, e := range entries {
		if e.Name() != "config" {
			t.Errorf("unexpected file in dir after Save: %q (expected only %q)", e.Name(), "config")
		}
	}
}

func TestDefaultPath_ReturnsAbsolutePath(t *testing.T) {
	t.Parallel()
	path, err := config.DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath() error = %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("DefaultPath() = %q, want absolute path", path)
	}
	base := filepath.Base(path)
	if base != "config" {
		t.Errorf("DefaultPath() base = %q, want %q", base, "config")
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")

	if err := os.WriteFile(path, []byte("not valid json"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load() expected error for invalid JSON, got nil")
	}
	if errors.Is(err, config.ErrNotFound) {
		t.Error("Load() returned ErrNotFound for invalid JSON, want parse error")
	}
}

func TestSave_OverwritesExistingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")

	first := &config.Config{Remote: "git@github.com:first/store.git", MachineName: "m1", LinkMode: "symlink"}
	if err := config.Save(path, first); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}

	second := &config.Config{Remote: "git@github.com:second/store.git", MachineName: "m2", LinkMode: "symlink"}
	if err := config.Save(path, second); err != nil {
		t.Fatalf("second Save() error = %v", err)
	}

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Remote != second.Remote {
		t.Errorf("Remote = %q, want %q", got.Remote, second.Remote)
	}
}

func TestLoad_UnreadableFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping as root: read restrictions do not apply")
	}
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config")

	// Write a valid config file, then remove read permission.
	cfg := &config.Config{Remote: "git@github.com:u/s.git", MachineName: "m", LinkMode: "symlink"}
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load() expected error for unreadable file, got nil")
	}
	if errors.Is(err, config.ErrNotFound) {
		t.Error("Load() returned ErrNotFound for unreadable file, want permission error")
	}
}

func TestSave_DestinationIsDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Make the destination path an existing directory — os.Rename(file, dir) fails.
	configPath := filepath.Join(dir, "config")
	if err := os.Mkdir(configPath, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	cfg := &config.Config{Remote: "git@github.com:u/s.git", MachineName: "m", LinkMode: "symlink"}
	err := config.Save(configPath, cfg)
	if err == nil {
		t.Fatal("Save() expected error when destination is a directory, got nil")
	}
	// The temp file should have been cleaned up by the defer.
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("ReadDir() error = %v", readErr)
	}
	for _, e := range entries {
		if e.Name() != "config" {
			t.Errorf("unexpected leftover file after failed Save: %q", e.Name())
		}
	}
}

func TestSave_UnwritableDirectory(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping as root: write restrictions do not apply")
	}
	t.Parallel()

	// Create a read-only parent directory so os.MkdirAll fails.
	baseDir := t.TempDir()
	readOnlyDir := filepath.Join(baseDir, "readonly")
	if err := os.Mkdir(readOnlyDir, 0o500); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	t.Cleanup(func() {
		// Restore permissions for cleanup.
		_ = os.Chmod(readOnlyDir, 0o700)
	})

	// Try to save into a subdirectory of the read-only dir.
	path := filepath.Join(readOnlyDir, "subdir", "config")

	cfg := &config.Config{Remote: "git@github.com:user/store.git", MachineName: "m", LinkMode: "symlink"}
	err := config.Save(path, cfg)
	if err == nil {
		t.Fatal("Save() expected error for unwritable directory, got nil")
	}
}

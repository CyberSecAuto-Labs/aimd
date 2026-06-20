package cmd_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CyberSecAuto-Labs/aimd/cmd"
	"github.com/CyberSecAuto-Labs/aimd/internal/lock"
)

// setupTrackable wires up a project repo with a CLAUDE.md and a store repo,
// changes into the project dir, and returns the store dir and the overlay path.
func setupTrackable(t *testing.T) (storeDir, claudeMd string) {
	t.Helper()
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	storeDir = filepath.Join(base, "store")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeProjectRepo(t, projectDir)
	makeStoreRepo(t, storeDir)

	claudeMd = filepath.Join(projectDir, "CLAUDE.md")
	if err := os.WriteFile(claudeMd, []byte("# ctx\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	return storeDir, claudeMd
}

// TestMutatingCommand_WaitsForStoreLock proves a mutating command blocks while
// another process holds the exclusive store lock, then proceeds once it is
// released — i.e. two aimd processes cannot mutate the store concurrently.
func TestMutatingCommand_WaitsForStoreLock(t *testing.T) {
	storeDir, claudeMd := setupTrackable(t)

	// Hold the store lock as if from another aimd process.
	held, err := lock.Acquire(storeDir, lock.Exclusive)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.RunTrack([]string{"CLAUDE.md"}, storeDir, "test-machine", false, io.Discard)
	}()

	select {
	case <-done:
		_ = held.Release()
		t.Fatal("RunTrack completed while the store was locked by another holder")
	case <-time.After(300 * time.Millisecond):
		// Expected: blocked waiting on the lock.
	}

	if err := held.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunTrack after lock released: %v", err)
		}
	case <-time.After(9 * time.Second):
		t.Fatal("RunTrack did not complete after the lock was released")
	}

	fi, err := os.Lstat(claudeMd)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("expected CLAUDE.md to be a symlink after track completed")
	}
}

// TestMutatingCommand_ReleasesStoreLock proves a mutating command releases the
// store lock when it finishes, so the next acquirer is not blocked.
func TestMutatingCommand_ReleasesStoreLock(t *testing.T) {
	storeDir, _ := setupTrackable(t)

	if err := cmd.RunTrack([]string{"CLAUDE.md"}, storeDir, "test-machine", false, io.Discard); err != nil {
		t.Fatalf("RunTrack: %v", err)
	}

	// The lock must be free immediately after the command returns.
	h, err := lock.AcquireWithTimeout(storeDir, lock.Exclusive, 0)
	if err != nil {
		t.Fatalf("store still locked after RunTrack returned: %v", err)
	}
	_ = h.Release()
}

// TestDryRun_DoesNotTakeStoreLock proves a dry-run mutates nothing and so does
// not contend for the exclusive lock even while another holder has it.
func TestDryRun_DoesNotTakeStoreLock(t *testing.T) {
	storeDir, _ := setupTrackable(t)

	held, err := lock.Acquire(storeDir, lock.Exclusive)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = held.Release() })

	done := make(chan error, 1)
	go func() {
		done <- cmd.RunTrack([]string{"CLAUDE.md"}, storeDir, "test-machine", true, io.Discard)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("dry-run RunTrack: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dry-run blocked on the store lock; it should not acquire it")
	}
}

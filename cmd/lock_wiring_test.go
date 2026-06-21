package cmd_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// TestResolve_WaitsForStoreLock proves resolve takes the exclusive lock before
// touching the rebase state and blocks while another holder has it — so no other
// aimd process can disturb the store mid-resolution. Once the lock is free,
// resolve proceeds (here reaching the "no rebase in progress" guard, which is
// proof it got past the lock).
func TestResolve_WaitsForStoreLock(t *testing.T) {
	base := t.TempDir()
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeStoreRepo(t, storeDir)

	held, err := lock.Acquire(storeDir, lock.Exclusive)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.RunResolve(storeDir, "", false, false, false, false, io.Discard)
	}()

	select {
	case <-done:
		_ = held.Release()
		t.Fatal("RunResolve ran while the store was locked by another holder")
	case <-time.After(300 * time.Millisecond):
		// Expected: blocked waiting on the lock.
	}

	if err := held.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "no rebase in progress") {
			t.Fatalf("expected 'no rebase in progress' once lock released, got %v", err)
		}
	case <-time.After(9 * time.Second):
		t.Fatal("RunResolve did not complete after the lock was released")
	}
}

// TestReset_WaitsForStoreLock proves reset cannot tear down the store while
// another process (e.g. a watcher mid-sync) holds the exclusive lock; once the
// lock is free, reset proceeds.
func TestReset_WaitsForStoreLock(t *testing.T) {
	base := t.TempDir()
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeStoreRepo(t, storeDir)

	held, err := lock.Acquire(storeDir, lock.Exclusive)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.RunReset(storeDir, "test-machine", true, false, false, strings.NewReader(""), io.Discard)
	}()

	select {
	case <-done:
		_ = held.Release()
		t.Fatal("RunReset ran while the store was locked by another holder")
	case <-time.After(300 * time.Millisecond):
		// Expected: blocked waiting on the lock.
	}

	if err := held.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunReset after lock released: %v", err)
		}
	case <-time.After(9 * time.Second):
		t.Fatal("RunReset did not complete after the lock was released")
	}
}

// TestStatus_ReportsBusyUnderExclusiveLock proves a read command degrades to a
// "store busy" message instead of running git checks (and possibly erroring)
// while a mutating command holds the store exclusively.
func TestStatus_ReportsBusyUnderExclusiveLock(t *testing.T) {
	base := t.TempDir()
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeStoreRepo(t, storeDir)

	held, err := lock.Acquire(storeDir, lock.Exclusive)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = held.Release() })

	var out bytes.Buffer
	if err := cmd.RunStatus(storeDir, "test-machine", true, false, false, &out); err != nil {
		t.Fatalf("RunStatus under exclusive lock should not error, got: %v", err)
	}
	if !strings.Contains(out.String(), "store busy") {
		t.Fatalf("expected a 'store busy' message, got: %q", out.String())
	}
}

// TestDoctor_ReportsBusyUnderExclusiveLock is the doctor counterpart.
func TestDoctor_ReportsBusyUnderExclusiveLock(t *testing.T) {
	base := t.TempDir()
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeStoreRepo(t, storeDir)

	held, err := lock.Acquire(storeDir, lock.Exclusive)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = held.Release() })

	var out bytes.Buffer
	if err := cmd.RunDoctor(storeDir, "test-machine", true, &out); err != nil {
		t.Fatalf("RunDoctor under exclusive lock should not error, got: %v", err)
	}
	if !strings.Contains(out.String(), "store busy") {
		t.Fatalf("expected a 'store busy' message, got: %q", out.String())
	}
}

// TestStatus_SucceedsUnderSharedLock proves read commands coexist: a shared lock
// held elsewhere does not make status report busy.
func TestStatus_SucceedsUnderSharedLock(t *testing.T) {
	base := t.TempDir()
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeStoreRepo(t, storeDir)

	held, err := lock.Acquire(storeDir, lock.Shared)
	if err != nil {
		t.Fatalf("Acquire shared: %v", err)
	}
	t.Cleanup(func() { _ = held.Release() })

	var out bytes.Buffer
	if err := cmd.RunStatus(storeDir, "test-machine", true, false, false, &out); err != nil {
		t.Fatalf("RunStatus under a shared lock: %v", err)
	}
	if strings.Contains(out.String(), "store busy") {
		t.Fatalf("status should coexist with another reader, got busy: %q", out.String())
	}
}

// TestReset_RefusesWhileWatchPresent proves reset refuses whenever a watcher is
// running — even an idle one that holds no store lock at that instant.
func TestReset_RefusesWhileWatchPresent(t *testing.T) {
	base := t.TempDir()
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeStoreRepo(t, storeDir)

	// Simulate a running (idle) watcher: presence held, no store lock taken.
	presence, err := lock.AcquireWatchPresence(storeDir)
	if err != nil {
		t.Fatalf("AcquireWatchPresence: %v", err)
	}
	t.Cleanup(func() { _ = presence.Release() })

	err = cmd.RunReset(storeDir, "test-machine", true, false, false, strings.NewReader(""), io.Discard)
	if err == nil {
		t.Fatal("RunReset should refuse while a watcher is present")
	}
	if !strings.Contains(err.Error(), "aimd watch") {
		t.Fatalf("expected a 'stop aimd watch' message, got: %v", err)
	}

	// A dry-run only previews, so it is allowed alongside a watcher.
	if derr := cmd.RunReset(storeDir, "test-machine", true, true, false, strings.NewReader(""), io.Discard); derr != nil {
		t.Fatalf("dry-run reset should be allowed while a watcher is present, got: %v", derr)
	}

	// Once the watcher stops, reset proceeds.
	if rerr := presence.Release(); rerr != nil {
		t.Fatalf("Release presence: %v", rerr)
	}
	if rerr := cmd.RunReset(storeDir, "test-machine", true, false, false, strings.NewReader(""), io.Discard); rerr != nil {
		t.Fatalf("reset after watcher stopped: %v", rerr)
	}
}

// TestRunWatch_RegistersPresence proves a running watch registers presence so
// teardown commands can detect it.
func TestRunWatch_RegistersPresence(t *testing.T) {
	_, cloneDir := setupSyncBareWithClone(t)
	localPath := t.TempDir()
	seedRegistry(t, cloneDir, "test-proj", localPath, []string{"CLAUDE.md"})

	// The overlay dir must exist for the watcher to register the file.
	overlayDir := filepath.Join(cloneDir, "repos", "test-proj")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatalf("mkdir overlay: %v", err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "CLAUDE.md"), []byte("# ctx\n"), 0o600); err != nil {
		t.Fatalf("write overlay: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var out bytes.Buffer
	done := runWatchAsync(ctx, cloneDir, "test-machine", 600, true, &out, &mu)

	// Give the watcher time to register presence.
	time.Sleep(400 * time.Millisecond)
	running, err := lock.WatchRunning(cloneDir)
	if err != nil {
		t.Fatalf("WatchRunning: %v", err)
	}
	cancel()
	if werr := waitWatch(t, done); werr != nil {
		t.Fatalf("RunWatch returned error: %v", werr)
	}
	if !running {
		t.Fatal("expected WatchRunning = true while aimd watch is running")
	}
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

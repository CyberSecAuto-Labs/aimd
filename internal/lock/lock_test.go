//go:build unix

package lock_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/CyberSecAuto-Labs/aimd/internal/lock"
)

func TestAcquireRelease_Basic(t *testing.T) {
	dir := t.TempDir()
	h, err := lock.Acquire(dir, lock.Exclusive)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if h.Mode() != lock.Exclusive {
		t.Errorf("Mode = %v, want exclusive", h.Mode())
	}
	if err := h.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// After release the lock is free again.
	h2, err := lock.Acquire(dir, lock.Exclusive)
	if err != nil {
		t.Fatalf("re-Acquire after release: %v", err)
	}
	_ = h2.Release()
}

func TestAcquire_CreatesLockFileWithHolder(t *testing.T) {
	dir := t.TempDir()
	h, err := lock.Acquire(dir, lock.Exclusive)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = h.Release() })

	data, err := os.ReadFile(filepath.Join(dir, ".git", "aimd.lock"))
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected holder metadata to be written under exclusive lock")
	}
}

func TestSharedLocksCoexist(t *testing.T) {
	dir := t.TempDir()
	a, err := lock.Acquire(dir, lock.Shared)
	if err != nil {
		t.Fatalf("first shared Acquire: %v", err)
	}
	t.Cleanup(func() { _ = a.Release() })
	b, err := lock.AcquireWithTimeout(dir, lock.Shared, 0)
	if err != nil {
		t.Fatalf("second shared Acquire should coexist: %v", err)
	}
	t.Cleanup(func() { _ = b.Release() })
}

func TestExclusiveExcludesShared(t *testing.T) {
	dir := t.TempDir()
	h, err := lock.Acquire(dir, lock.Exclusive)
	if err != nil {
		t.Fatalf("Acquire exclusive: %v", err)
	}
	t.Cleanup(func() { _ = h.Release() })

	_, err = lock.AcquireWithTimeout(dir, lock.Shared, 0)
	if !lock.IsBusy(err) {
		t.Fatalf("shared acquire under exclusive lock: got %v, want BusyError", err)
	}
}

func TestSharedExcludesExclusive(t *testing.T) {
	dir := t.TempDir()
	h, err := lock.Acquire(dir, lock.Shared)
	if err != nil {
		t.Fatalf("Acquire shared: %v", err)
	}
	t.Cleanup(func() { _ = h.Release() })

	_, err = lock.AcquireWithTimeout(dir, lock.Exclusive, 0)
	if !lock.IsBusy(err) {
		t.Fatalf("exclusive acquire under shared lock: got %v, want BusyError", err)
	}
}

func TestExclusiveContended_Refuses(t *testing.T) {
	dir := t.TempDir()
	h, err := lock.Acquire(dir, lock.Exclusive)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = h.Release() })

	_, err = lock.AcquireWithTimeout(dir, lock.Exclusive, 0)
	if !lock.IsBusy(err) {
		t.Fatalf("contended exclusive acquire: got %v, want BusyError", err)
	}
	var be *lock.BusyError
	if !errors.As(err, &be) {
		t.Fatalf("error is not *BusyError: %v", err)
	}
	if be.Holder.PID != os.Getpid() {
		t.Errorf("BusyError holder PID = %d, want %d", be.Holder.PID, os.Getpid())
	}
}

func TestAcquireWaitsThenSucceeds(t *testing.T) {
	dir := t.TempDir()
	h, err := lock.Acquire(dir, lock.Exclusive)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	released := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = h.Release()
		close(released)
	}()

	start := time.Now()
	h2, err := lock.AcquireWithTimeout(dir, lock.Exclusive, 2*time.Second)
	if err != nil {
		t.Fatalf("Acquire should succeed after holder releases: %v", err)
	}
	t.Cleanup(func() { _ = h2.Release() })
	if time.Since(start) < 100*time.Millisecond {
		t.Errorf("acquire returned too fast (%s); expected to wait for release", time.Since(start))
	}
	<-released
}

func TestRelease_Idempotent(t *testing.T) {
	dir := t.TempDir()
	h, err := lock.Acquire(dir, lock.Exclusive)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("second Release should be a no-op, got: %v", err)
	}
}

func TestStaleLockFile_DoesNotBlock(t *testing.T) {
	// A lock file left behind by a dead process (metadata present, no flock
	// held) must not block a fresh acquirer.
	dir := t.TempDir()
	lockDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Dead-PID metadata with an old timestamp; no process holds the flock.
	if err := os.WriteFile(filepath.Join(lockDir, "aimd.lock"), []byte("999999 2020-01-01T00:00:00Z\n"), 0o644); err != nil {
		t.Fatalf("seed lock file: %v", err)
	}
	h, err := lock.AcquireWithTimeout(dir, lock.Exclusive, 0)
	if err != nil {
		t.Fatalf("acquire over stale lock file: %v", err)
	}
	_ = h.Release()
}

func TestSubprocess_DeathReleasesLock(t *testing.T) {
	dir := t.TempDir()
	cmd := startLockHolder(t, dir)
	waitReady(t, dir)

	// Killing the holder must free the lock (kernel-released on exit).
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill holder: %v", err)
	}
	_ = cmd.Wait()

	if !eventuallyAcquirable(dir) {
		t.Fatal("lock not reclaimable after holder was killed")
	}
}

func TestSubprocess_SignalReleasesLock(t *testing.T) {
	dir := t.TempDir()
	cmd := startLockHolder(t, dir)
	waitReady(t, dir)

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal holder: %v", err)
	}
	_ = cmd.Wait()

	if !eventuallyAcquirable(dir) {
		t.Fatal("lock not reclaimable after holder received SIGTERM")
	}
}

func TestWatchPresence(t *testing.T) {
	dir := t.TempDir()

	// No watcher yet.
	running, err := lock.WatchRunning(dir)
	if err != nil {
		t.Fatalf("WatchRunning: %v", err)
	}
	if running {
		t.Fatal("WatchRunning = true with no watcher present")
	}

	// One watcher registers presence.
	a, err := lock.AcquireWatchPresence(dir)
	if err != nil {
		t.Fatalf("AcquireWatchPresence: %v", err)
	}
	running, err = lock.WatchRunning(dir)
	if err != nil {
		t.Fatalf("WatchRunning: %v", err)
	}
	if !running {
		t.Fatal("WatchRunning = false while a watcher holds presence")
	}

	// A second watcher coexists (shared); presence still detected.
	b, err := lock.AcquireWatchPresence(dir)
	if err != nil {
		t.Fatalf("second AcquireWatchPresence should coexist: %v", err)
	}
	if running, _ := lock.WatchRunning(dir); !running {
		t.Fatal("WatchRunning = false with two watchers present")
	}

	// Releasing one leaves the other holding presence.
	if err := a.Release(); err != nil {
		t.Fatalf("Release a: %v", err)
	}
	if running, _ := lock.WatchRunning(dir); !running {
		t.Fatal("WatchRunning = false while one watcher still holds presence")
	}

	// Releasing the last clears presence.
	if err := b.Release(); err != nil {
		t.Fatalf("Release b: %v", err)
	}
	if running, _ := lock.WatchRunning(dir); running {
		t.Fatal("WatchRunning = true after all watchers released")
	}
}

// The watch-presence lock and the store lock are independent files: holding the
// store lock must not make WatchRunning report a watcher, and vice versa.
func TestWatchPresence_IndependentFromStoreLock(t *testing.T) {
	dir := t.TempDir()

	h, err := lock.Acquire(dir, lock.Exclusive) // store lock
	if err != nil {
		t.Fatalf("Acquire store lock: %v", err)
	}
	t.Cleanup(func() { _ = h.Release() })

	if running, _ := lock.WatchRunning(dir); running {
		t.Fatal("WatchRunning = true while only the store lock is held")
	}
}

// TestHelperProcess is re-executed as a subprocess by the death/signal tests.
// Guarded by AIMD_LOCK_HELPER so it is a no-op under normal `go test`.
func TestHelperProcess(_ *testing.T) {
	dir := os.Getenv("AIMD_LOCK_HELPER")
	if dir == "" {
		return
	}
	h, err := lock.Acquire(dir, lock.Exclusive)
	if err != nil {
		os.Exit(3)
	}
	// Signal readiness, then hold the lock until the parent terminates us.
	if err := os.WriteFile(filepath.Join(dir, "ready"), nil, 0o644); err != nil {
		os.Exit(4)
	}
	time.Sleep(30 * time.Second)
	_ = h.Release()
	os.Exit(0)
}

// --- helpers ---

func startLockHolder(t *testing.T, dir string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "-test.timeout=60s") //nolint:gosec // re-exec of this test binary with constant args
	cmd.Env = append(os.Environ(), "AIMD_LOCK_HELPER="+dir)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

func waitReady(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("helper did not signal readiness in time")
}

func eventuallyAcquirable(dir string) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h, err := lock.AcquireWithTimeout(dir, lock.Exclusive, 0)
		if err == nil {
			_ = h.Release()
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

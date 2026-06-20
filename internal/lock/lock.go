//go:build unix

package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Mode selects whether a lock is exclusive (single writer) or shared
// (multiple readers, no writer).
type Mode int

const (
	// Exclusive admits a single holder and excludes every other holder,
	// shared or exclusive. Mutating commands take this mode.
	Exclusive Mode = iota
	// Shared admits any number of concurrent shared holders but excludes
	// exclusive holders. Read commands take this mode for a consistent
	// snapshot while no mutation is in flight.
	Shared
)

func (m Mode) String() string {
	if m == Shared {
		return "shared"
	}
	return "exclusive"
}

const (
	// DefaultTimeout bounds how long Acquire waits for a contended lock before
	// giving up with a BusyError. Mutating commands are short, so a held lock
	// usually clears well within this window.
	DefaultTimeout = 10 * time.Second
	// pollInterval is how often a blocked Acquire retries the non-blocking
	// flock while waiting for the holder to release.
	pollInterval = 50 * time.Millisecond
)

// Holder describes the process recorded in the lock file. It is best-effort
// metadata for diagnostics only — the actual mutual exclusion is enforced by
// flock, not by these fields.
type Holder struct {
	PID  int
	Time time.Time
}

// BusyError is returned when the lock cannot be acquired within the timeout.
// Callers use it to print a "store busy" message instead of failing a raw git
// operation mid-mutation.
type BusyError struct {
	Mode   Mode
	Holder Holder
}

func (e *BusyError) Error() string {
	if e.Holder.PID > 0 {
		return fmt.Sprintf(
			"aimd store is busy (locked by aimd process %d since %s); stop the other aimd command or retry shortly",
			e.Holder.PID, e.Holder.Time.Format(time.RFC3339),
		)
	}
	return "aimd store is busy (locked by another aimd process); retry shortly"
}

// IsBusy reports whether err is (or wraps) a BusyError.
func IsBusy(err error) bool {
	var be *BusyError
	return errors.As(err, &be)
}

// Handle is a held lock. Release it (typically via defer) to free the store.
// Release is idempotent and the kernel also drops the lock if the process
// exits without calling it.
type Handle struct {
	file *os.File
	mode Mode
	path string

	mu       sync.Mutex
	released bool
}

// Mode reports the mode the handle was acquired in.
func (h *Handle) Mode() Mode { return h.mode }

// lockFilePath returns the lock file path for a store. The lock lives inside
// the store's local .git directory rather than the versioned .aimd tree: a lock
// is per-machine runtime state, so it must never appear in `git status`, never
// be committed, and never propagate to other clones — exactly the guarantees
// .git already provides for local repo state.
func lockFilePath(storeDir string) string {
	return filepath.Join(storeDir, ".git", "aimd.lock")
}

// Acquire takes the store lock in the given mode, waiting up to DefaultTimeout.
func Acquire(storeDir string, mode Mode) (*Handle, error) {
	return AcquireWithTimeout(storeDir, mode, DefaultTimeout)
}

// AcquireWithTimeout takes the store lock in the given mode, waiting up to
// timeout for a contended lock. A zero timeout tries once and returns a
// BusyError immediately if the lock is held. On success the returned Handle
// must be released.
func AcquireWithTimeout(storeDir string, mode Mode, timeout time.Duration) (*Handle, error) {
	lockPath := lockFilePath(storeDir)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("creating store lock directory: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening store lock: %w", err)
	}

	how := syscall.LOCK_EX
	if mode == Shared {
		how = syscall.LOCK_SH
	}

	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("locking store: %w", err)
		}
		if !time.Now().Before(deadline) {
			holder := readHolder(lockPath)
			_ = f.Close()
			return nil, &BusyError{Mode: mode, Holder: holder}
		}
		time.Sleep(pollInterval)
	}

	// Record holder metadata only under an exclusive lock, where we are the
	// sole writer. Shared holders skip the write to avoid clobbering each
	// other; the residual metadata is diagnostic only.
	if mode == Exclusive {
		writeHolder(f)
	}
	return &Handle{file: f, mode: mode, path: lockPath}, nil
}

// Release frees the lock. It is safe to call more than once.
func (h *Handle) Release() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.released {
		return nil
	}
	h.released = true
	// Closing the descriptor releases the flock; the explicit unlock makes the
	// intent obvious and frees it a moment earlier.
	_ = syscall.Flock(int(h.file.Fd()), syscall.LOCK_UN)
	if err := h.file.Close(); err != nil {
		return fmt.Errorf("releasing store lock: %w", err)
	}
	return nil
}

// writeHolder records "<pid> <ISO-timestamp>" in the lock file. Best-effort:
// failures are ignored because the metadata is only used for diagnostics.
func writeHolder(f *os.File) {
	line := fmt.Sprintf("%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	if err := f.Truncate(0); err != nil {
		return
	}
	if _, err := f.WriteAt([]byte(line), 0); err != nil {
		return
	}
}

// readHolder parses the holder metadata from the lock file. A missing or
// malformed file yields a zero Holder.
func readHolder(lockPath string) Holder {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return Holder{}
	}
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return Holder{}
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return Holder{}
	}
	h := Holder{PID: pid}
	if len(fields) >= 2 {
		if ts, err := time.Parse(time.RFC3339, fields[1]); err == nil {
			h.Time = ts
		}
	}
	return h
}

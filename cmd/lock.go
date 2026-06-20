package cmd

import (
	"fmt"
	"time"

	"github.com/CyberSecAuto-Labs/aimd/internal/lock"
)

// readLockTimeout bounds how long a read command waits for the shared lock
// before reporting the store busy. It is short: a read should briefly wait out
// an in-flight mutation, then tell the user to retry rather than hang.
const readLockTimeout = 2 * time.Second

// lockStoreExclusive takes the exclusive store lock for a mutating command and
// returns a release function the caller must defer. Every command that touches
// the registry, overlays, metadata, project links, or .git/info/exclude holds
// this for its whole mutation so two aimd processes never mutate the one shared
// store at once. A store already locked by another process surfaces as a clear
// "store is busy" error instead of a corrupted git operation.
func lockStoreExclusive(storeDir string) (release func(), err error) {
	h, lerr := lock.Acquire(storeDir, lock.Exclusive)
	if lerr != nil {
		return nil, fmt.Errorf("locking store: %w", lerr)
	}
	return func() { _ = h.Release() }, nil
}

// lockStoreShared takes the shared store lock for a read command (status,
// doctor). The shared lock coexists with other readers but is excluded while a
// mutating command holds the store exclusively, giving the reader a consistent
// snapshot. It returns busy=true (with a nil release) when a mutation is in
// progress, so the caller prints a "store busy" message instead of running git
// checks against a store mid-mutation.
func lockStoreShared(storeDir string) (release func(), busy bool, err error) {
	h, lerr := lock.AcquireWithTimeout(storeDir, lock.Shared, readLockTimeout)
	if lerr != nil {
		if lock.IsBusy(lerr) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("locking store: %w", lerr)
	}
	return func() { _ = h.Release() }, false, nil
}

package cmd

import (
	"fmt"

	"github.com/CyberSecAuto-Labs/aimd/internal/lock"
)

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

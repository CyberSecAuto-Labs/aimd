// Package lock provides a cross-process advisory lock over the aimd store.
//
// aimd keeps a single private store (git worktree + index, registry.json,
// overlays, symlinks, .git/info/exclude) that every mutating command touches.
// Two aimd processes running at once can corrupt that state or lose writes, so
// mutating commands serialize on one store-level lock; read commands take a
// shared lock or report the store busy.
//
// The lock is advisory (it only constrains processes that call Acquire) and is
// backed by flock(2) on the file <storeDir>/.git/aimd.lock. It lives inside the
// store's local .git directory because a lock is per-machine runtime state: it
// must never show up in `git status`, never be committed, and never propagate to
// other clones. flock associates the lock with the open file, so the kernel
// releases it automatically when the holder exits — whether cleanly, on a
// crash, or on a signal — which means a dead holder never blocks the next
// acquirer. The file also records the holder PID and an ISO timestamp so a
// blocked process can report who holds the store.
package lock

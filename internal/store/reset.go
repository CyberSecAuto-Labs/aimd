package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
)

// resetOrphanBranch is the temporary branch name used while building the wiped
// store's fresh root commit. It is renamed to main on success.
const resetOrphanBranch = "aimd-wipe"

// ResetHistory rewrites the store to a pristine empty state with a single fresh
// root commit, discarding all prior history. It empties the registry, clears
// every overlay (repos/) and metadata file, then builds an orphan commit that
// contains only the empty registry — the same content `init` scaffolds — and
// points local main at it. The caller force-pushes afterward to replace the
// remote's history.
//
// This is the irreversible core of `aimd reset --remote`; callers must gate it
// behind explicit confirmation and a completed local teardown.
func ResetHistory(storeDir, machineName string) error {
	// 1. Reset the worktree to an empty store layout.
	for _, sub := range []string{"repos", "metadata"} {
		p := filepath.Join(storeDir, sub)
		if err := os.RemoveAll(p); err != nil {
			return fmt.Errorf("clearing %s: %w", sub, err)
		}
		if err := os.MkdirAll(p, 0o700); err != nil {
			return fmt.Errorf("recreating %s: %w", sub, err)
		}
	}
	registryPath := filepath.Join(storeDir, ".aimd", "registry.json")
	if err := registry.Save(registryPath, registry.New()); err != nil {
		return fmt.Errorf("writing empty registry: %w", err)
	}

	// 2. Build a fresh root commit on an orphan branch. Delete any leftover
	//    orphan branch from a previous interrupted run first (ignore failure —
	//    it usually does not exist).
	_, _ = gitCmd("-C", storeDir, "branch", "-D", resetOrphanBranch).CombinedOutput()

	if out, err := gitCmd("-C", storeDir, "checkout", "--orphan", resetOrphanBranch).CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout --orphan: %w — %s", err, strings.TrimSpace(string(out)))
	}
	// Clear the index (checkout --orphan carries the old index over), then stage
	// only the empty registry.
	if out, err := gitCmd("-C", storeDir, "read-tree", "--empty").CombinedOutput(); err != nil {
		return fmt.Errorf("git read-tree --empty: %w — %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := gitCmd("-C", storeDir, "add", filepath.Join(".aimd", "registry.json")).CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %w — %s", err, strings.TrimSpace(string(out)))
	}

	title := fmt.Sprintf("reset: wipe aimd store [%s %s]",
		machineName, time.Now().UTC().Format(time.RFC3339))
	args := append([]string{"-C", storeDir}, CommitIdentityArgs...)
	args = append(args, "commit", "-m", title)
	if out, err := gitCmd(args...).CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w — %s", err, strings.TrimSpace(string(out)))
	}

	// 3. Replace main with the orphan commit (renames the current branch to main,
	//    discarding the old main and its history locally).
	if out, err := gitCmd("-C", storeDir, "branch", "-M", "main").CombinedOutput(); err != nil {
		return fmt.Errorf("git branch -M main: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RemoteURL returns the URL of the store's origin remote, or an error if no
// origin remote is configured.
func RemoteURL(storeDir string) (string, error) {
	out, err := gitCmd("-C", storeDir, "remote", "get-url", "origin").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("no store remote configured: %s", strings.TrimSpace(string(out)))
	}
	url := strings.TrimSpace(string(out))
	if url == "" {
		return "", fmt.Errorf("store origin remote has no URL")
	}
	return url, nil
}

// ForcePush force-pushes HEAD to origin/main, replacing the remote history. It
// is the remote half of a store wipe: after ResetHistory has rewritten local
// main, this overwrites the remote so every other clone sees the empty store.
// On success it clears any pending-push marker; on failure it returns a
// *PushError so callers can distinguish a transient network failure from a hard
// rejection.
func ForcePush(storeDir string) error {
	out, err := gitCmd("-C", storeDir, "push", "--force", "origin", "HEAD:main").CombinedOutput()
	outStr := strings.TrimSpace(string(out))
	if err != nil {
		return &PushError{
			Transient: !isPushHard(outStr),
			Output:    outStr,
			Err:       err,
		}
	}

	markerPath := pendingPushMarkerPath(storeDir)
	if _, statErr := os.Stat(markerPath); !errors.Is(statErr, os.ErrNotExist) {
		_ = os.Remove(markerPath)
	}
	return nil
}

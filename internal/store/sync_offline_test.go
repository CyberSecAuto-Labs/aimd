package store_test

import (
	"os/exec"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/internal/store"
)

// DetectStateOffline must classify the store WITHOUT contacting the remote: it
// reads only the already-fetched origin/main tracking ref. These tests mutate
// the bare origin *after* the clone has its tracking ref, then assert the
// offline check still reports the pre-mutation relationship (proving no fetch).

func TestDetectStateOffline_UpToDate(t *testing.T) {
	_, cloneDir := setupBareWithClone(t)

	state, err := store.DetectStateOffline(cloneDir)
	if err != nil {
		t.Fatalf("DetectStateOffline: %v", err)
	}
	if state != store.StateUpToDate {
		t.Errorf("want StateUpToDate, got %v", state)
	}
}

func TestDetectStateOffline_AheadFromLocalCommit(t *testing.T) {
	_, cloneDir := setupBareWithClone(t)

	// A local commit not on origin/main → AHEAD, detectable offline.
	addCommitFile(t, cloneDir, "local.txt", "local only")

	state, err := store.DetectStateOffline(cloneDir)
	if err != nil {
		t.Fatalf("DetectStateOffline: %v", err)
	}
	if state != store.StateAhead {
		t.Errorf("want StateAhead, got %v", state)
	}
}

func TestDetectStateOffline_DoesNotFetch(t *testing.T) {
	bareDir, cloneDir := setupBareWithClone(t)

	// Advance origin/main from another clone, but DO NOT fetch in cloneDir.
	pusherDir := t.TempDir()
	if out, err := exec.Command("git", "clone", bareDir, pusherDir).CombinedOutput(); err != nil {
		t.Fatalf("git clone pusher: %v — %s", err, out)
	}
	gitRun(t, pusherDir, "config", "user.email", "test@test.com")
	gitRun(t, pusherDir, "config", "user.name", "test")
	addCommitFile(t, pusherDir, "remote.txt", "from remote")
	gitRun(t, pusherDir, "push", "origin", "HEAD:main")

	// Offline: the stale tracking ref still equals local HEAD → UpToDate.
	// (A fetching variant would report Behind — that's the contrast that proves
	// no network access happened here.)
	state, err := store.DetectStateOffline(cloneDir)
	if err != nil {
		t.Fatalf("DetectStateOffline: %v", err)
	}
	if state != store.StateUpToDate {
		t.Errorf("offline check must ignore un-fetched remote changes; want StateUpToDate, got %v", state)
	}

	// Now fetch and confirm the offline check sees the new tracking ref → Behind.
	gitRun(t, cloneDir, "fetch", "origin", "main")
	state, err = store.DetectStateOffline(cloneDir)
	if err != nil {
		t.Fatalf("DetectStateOffline after fetch: %v", err)
	}
	if state != store.StateBehind {
		t.Errorf("after fetch, want StateBehind, got %v", state)
	}
}

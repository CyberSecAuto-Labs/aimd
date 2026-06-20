package cmd_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CyberSecAuto-Labs/aimd/cmd"
)

// runWatchAsync starts RunWatch in a goroutine and returns a channel that
// receives its error when it returns. The caller is responsible for cancelling
// ctx so the goroutine exits.
func runWatchAsync(ctx context.Context, storeDir, machine string, debounceSecs int, all bool, out *bytes.Buffer, mu *sync.Mutex) <-chan error {
	done := make(chan error, 1)
	go func() {
		// Guard the buffer: RunWatch writes from its own goroutine paths.
		w := lockedWriter{buf: out, mu: mu}
		done <- cmd.RunWatch(ctx, storeDir, machine, debounceSecs, all, w)
	}()
	return done
}

// lockedWriter serialises writes to a bytes.Buffer so the test goroutine can
// read it concurrently under -race.
type lockedWriter struct {
	buf *bytes.Buffer
	mu  *sync.Mutex
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if err != nil {
		return n, fmt.Errorf("locked write: %w", err)
	}
	return n, nil
}

// waitWatch waits for RunWatch to return or fails the test on timeout.
func waitWatch(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("RunWatch did not return within timeout")
		return nil
	}
}

// seedEmptyRegistry writes a registry.json with no projects into storeDir.
func seedEmptyRegistry(t *testing.T, storeDir string) {
	t.Helper()
	regDir := filepath.Join(storeDir, ".aimd")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatalf("mkdir .aimd: %v", err)
	}
	if err := os.WriteFile(filepath.Join(regDir, "registry.json"),
		[]byte(`{"version":1,"projects":{}}`+"\n"), 0o600); err != nil {
		t.Fatalf("write registry.json: %v", err)
	}
}

func TestRunWatch_EmptyState(t *testing.T) {
	_, cloneDir := setupSyncBareWithClone(t)

	// Empty registry → selectProjects(all) yields zero targets.
	seedEmptyRegistry(t, cloneDir)

	var out bytes.Buffer
	err := cmd.RunWatch(context.Background(), cloneDir, "test-machine", 300, true, &out)
	if err != nil {
		t.Fatalf("RunWatch empty: %v", err)
	}
	if !strings.Contains(out.String(), "No projects tracked") {
		t.Errorf("expected empty-state line, got:\n%s", out.String())
	}
}

func TestRunWatch_HeaderAndShutdown(t *testing.T) {
	_, cloneDir := setupSyncBareWithClone(t)

	localPath := t.TempDir()
	seedRegistry(t, cloneDir, "test-proj", localPath, []string{"CLAUDE.md"})

	// Create the overlay file (and its parent dir) so it is watchable.
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

	// Give the watcher a moment to print the header and enter Run.
	time.Sleep(200 * time.Millisecond)
	cancel()

	if err := waitWatch(t, done); err != nil {
		t.Fatalf("RunWatch returned error: %v", err)
	}

	mu.Lock()
	got := out.String()
	mu.Unlock()
	for _, want := range []string{"aimd watch •", "watching", "aimd watch stopped"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, got)
		}
	}
}

func TestRunWatch_FlushSyncsDirtyOverlayOnShutdown(t *testing.T) {
	bareDir, cloneDir := setupSyncBareWithClone(t)

	// Commit + push an initial overlay so HEAD == origin/main.
	overlayDir := filepath.Join(cloneDir, "repos", "test-proj")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatalf("mkdir overlay: %v", err)
	}
	overlayFile := filepath.Join(overlayDir, "CLAUDE.md")
	if err := os.WriteFile(overlayFile, []byte("# initial\n"), 0o600); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	syncGitRun(t, cloneDir, "add", filepath.Join("repos", "test-proj", "CLAUDE.md"))
	syncGitRun(t, cloneDir, "-c", "user.email=aimd-bot@cybersecauto-labs.org", "-c", "user.name=aimd-bot",
		"commit", "-m", "initial overlay")
	syncGitRun(t, cloneDir, "push", "origin", "HEAD:main")

	localPath := t.TempDir()
	seedRegistry(t, cloneDir, "test-proj", localPath, []string{"CLAUDE.md"})

	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var out bytes.Buffer
	// Large debounce so the timer never fires on its own — the flush on cancel
	// is what drives the sync, exercising the graceful-shutdown path.
	done := runWatchAsync(ctx, cloneDir, "test-machine", 600, true, &out, &mu)

	// Let the watcher register before modifying.
	time.Sleep(300 * time.Millisecond)

	// Make the overlay dirty so the flush has something to sync.
	if err := os.WriteFile(overlayFile, []byte("# updated\n"), 0o600); err != nil {
		t.Fatalf("update overlay: %v", err)
	}

	// Give fsnotify a moment to register the change (so a pending timer exists).
	time.Sleep(500 * time.Millisecond)
	cancel()

	if err := waitWatch(t, done); err != nil {
		t.Fatalf("RunWatch returned error: %v", err)
	}

	mu.Lock()
	got := out.String()
	mu.Unlock()

	if !strings.Contains(got, "↑ syncing") {
		t.Fatalf("expected a sync to be attempted on flush, got:\n%s", got)
	}
	if !strings.Contains(got, "Synced") {
		t.Errorf("expected '✓ Synced' after flushing dirty overlay, got:\n%s", got)
	}

	// The bare origin must contain the sync commit.
	bareLog := syncGitRun(t, bareDir, "log", "--oneline", "main")
	if !strings.Contains(bareLog, "sync:") {
		t.Errorf("expected 'sync:' commit pushed to origin, got:\n%s", bareLog)
	}
}

// A file written immediately before Ctrl-C can have its fsnotify event still
// queued when ctx cancellation wins Run's select, so the debounce flush sees no
// pending key. Graceful shutdown must still sync the dirty overlay. This test
// makes the overlay dirty BEFORE the watcher registers, so no fsnotify event is
// ever generated during the session — the only path that can sync it on exit is
// a command-level dirty sweep. Regression test for Finding 2.
func TestRunWatch_ShutdownSyncsDirtyOverlayWithNoPendingEvent(t *testing.T) {
	bareDir, cloneDir := setupSyncBareWithClone(t)

	// Commit + push an initial overlay so HEAD == origin/main.
	overlayDir := filepath.Join(cloneDir, "repos", "test-proj")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatalf("mkdir overlay: %v", err)
	}
	overlayFile := filepath.Join(overlayDir, "CLAUDE.md")
	if err := os.WriteFile(overlayFile, []byte("# initial\n"), 0o600); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	syncGitRun(t, cloneDir, "add", filepath.Join("repos", "test-proj", "CLAUDE.md"))
	syncGitRun(t, cloneDir, "-c", "user.email=aimd-bot@cybersecauto-labs.org", "-c", "user.name=aimd-bot",
		"commit", "-m", "initial overlay")
	syncGitRun(t, cloneDir, "push", "origin", "HEAD:main")

	localPath := t.TempDir()
	seedRegistry(t, cloneDir, "test-proj", localPath, []string{"CLAUDE.md"})

	// Dirty the overlay BEFORE starting watch: no fsnotify event is generated for
	// this write during the session, so the debouncer will have no pending key.
	if err := os.WriteFile(overlayFile, []byte("# updated before watch\n"), 0o600); err != nil {
		t.Fatalf("update overlay: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var out bytes.Buffer
	// Large debounce; no timer ever fires. The file is never touched again.
	done := runWatchAsync(ctx, cloneDir, "test-machine", 600, true, &out, &mu)

	// Let the watcher register and enter Run — but do NOT touch the file again.
	time.Sleep(300 * time.Millisecond)
	cancel()

	if err := waitWatch(t, done); err != nil {
		t.Fatalf("RunWatch returned error: %v", err)
	}

	mu.Lock()
	got := out.String()
	mu.Unlock()
	if !strings.Contains(got, "Synced") {
		t.Errorf("expected dirty overlay synced on shutdown, got:\n%s", got)
	}

	// The sync commit must reach the bare origin.
	bareLog := syncGitRun(t, bareDir, "log", "--oneline", "main")
	if !strings.Contains(bareLog, "sync:") {
		t.Errorf("expected 'sync:' commit pushed to origin on shutdown, got:\n%s", bareLog)
	}
}

// The shutdown dirty sweep must not sync a project whose overlay is clean. A
// clean shutdown should leave origin untouched and print no sync line.
func TestRunWatch_ShutdownDoesNotSyncCleanOverlay(t *testing.T) {
	bareDir, cloneDir := setupSyncBareWithClone(t)

	overlayDir := filepath.Join(cloneDir, "repos", "test-proj")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatalf("mkdir overlay: %v", err)
	}
	overlayFile := filepath.Join(overlayDir, "CLAUDE.md")
	if err := os.WriteFile(overlayFile, []byte("# initial\n"), 0o600); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	// Commit + push so the overlay is clean and HEAD == origin/main.
	syncGitRun(t, cloneDir, "add", filepath.Join("repos", "test-proj", "CLAUDE.md"))
	syncGitRun(t, cloneDir, "-c", "user.email=aimd-bot@cybersecauto-labs.org", "-c", "user.name=aimd-bot",
		"commit", "-m", "initial overlay")
	syncGitRun(t, cloneDir, "push", "origin", "HEAD:main")

	localPath := t.TempDir()
	seedRegistry(t, cloneDir, "test-proj", localPath, []string{"CLAUDE.md"})

	originBefore := strings.TrimSpace(syncGitRun(t, bareDir, "rev-parse", "main"))

	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var out bytes.Buffer
	done := runWatchAsync(ctx, cloneDir, "test-machine", 600, true, &out, &mu)

	time.Sleep(300 * time.Millisecond)
	cancel()

	if err := waitWatch(t, done); err != nil {
		t.Fatalf("RunWatch returned error: %v", err)
	}

	mu.Lock()
	got := out.String()
	mu.Unlock()
	if strings.Contains(got, "↑ syncing") {
		t.Errorf("clean overlay should not be synced on shutdown, got:\n%s", got)
	}

	originAfter := strings.TrimSpace(syncGitRun(t, bareDir, "rev-parse", "main"))
	if originBefore != originAfter {
		t.Errorf("origin advanced on clean shutdown: %s → %s", originBefore, originAfter)
	}
}

// When the shutdown dirty sweep cannot sync a dirty project, RunWatch must
// surface that as a non-nil command result rather than logging it and exiting 0
// — there is no retry path on exit. A DIVERGED store with a dirty worktree makes
// store.Sync's `git pull --rebase` refuse, so syncProject fails. Regression test
// for Finding 3.
func TestRunWatch_ShutdownSyncFailureReturnsError(t *testing.T) {
	bareDir, cloneDir := setupSyncBareWithClone(t)

	// Commit + push an initial overlay so HEAD == origin/main.
	overlayDir := filepath.Join(cloneDir, "repos", "test-proj")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatalf("mkdir overlay: %v", err)
	}
	overlayFile := filepath.Join(overlayDir, "CLAUDE.md")
	if err := os.WriteFile(overlayFile, []byte("# initial\n"), 0o600); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	syncGitRun(t, cloneDir, "add", filepath.Join("repos", "test-proj", "CLAUDE.md"))
	syncGitRun(t, cloneDir, "-c", "user.email=aimd-bot@cybersecauto-labs.org", "-c", "user.name=aimd-bot",
		"commit", "-m", "initial overlay")
	syncGitRun(t, cloneDir, "push", "origin", "HEAD:main")

	// Machine B pushes an unrelated commit so cloneDir is BEHIND by one.
	pusherDir := t.TempDir()
	if cloneOut, err := exec.Command("git", "clone", bareDir, pusherDir).CombinedOutput(); err != nil {
		t.Fatalf("git clone pusher: %v — %s", err, cloneOut)
	}
	syncGitRun(t, pusherDir, "config", "user.email", "test@test.com")
	syncGitRun(t, pusherDir, "config", "user.name", "test")
	syncAddCommitFile(t, pusherDir, "remote.txt", "from B")
	syncGitRun(t, pusherDir, "push", "origin", "HEAD:main")

	// cloneDir gets a local commit too → DIVERGED (1 ahead, 1 behind).
	syncAddCommitFile(t, cloneDir, "local.txt", "from A")

	localPath := t.TempDir()
	seedRegistry(t, cloneDir, "test-proj", localPath, []string{"CLAUDE.md"})

	// Dirty the overlay before the watcher registers (no event → only the sweep
	// can act). With a DIVERGED store, store.Sync's rebase refuses on the dirty
	// worktree, so the shutdown sync fails.
	if err := os.WriteFile(overlayFile, []byte("# updated before watch\n"), 0o600); err != nil {
		t.Fatalf("update overlay: %v", err)
	}

	originBefore := strings.TrimSpace(syncGitRun(t, bareDir, "rev-parse", "main"))

	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var out bytes.Buffer
	done := runWatchAsync(ctx, cloneDir, "test-machine", 600, true, &out, &mu)

	time.Sleep(300 * time.Millisecond)
	cancel()

	err := waitWatch(t, done)
	if err == nil {
		t.Fatalf("expected RunWatch to return an error when the shutdown sync fails, got nil")
	}

	// Origin must not have advanced — the failed sync pushed nothing.
	originAfter := strings.TrimSpace(syncGitRun(t, bareDir, "rev-parse", "main"))
	if originBefore != originAfter {
		t.Errorf("origin advanced despite failed shutdown sync: %s → %s", originBefore, originAfter)
	}
}

func TestFormatDebounce(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{300 * time.Second, "5m"},
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m30s"},
		{time.Minute, "1m"},
	}
	for _, c := range cases {
		if got := cmd.FormatDebounce(c.in); got != c.want {
			t.Errorf("FormatDebounce(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

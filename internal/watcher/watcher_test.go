package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestWatcher_ChangeTriggersOnChangeAndOnSync(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "overlay.md")
	writeFile(t, file, "initial")

	changes := make(chan Event, 16)
	syncs := make(chan string, 16)

	w, err := New(Config{
		Debounce: 50 * time.Millisecond,
		OnChange: func(e Event) { changes <- e },
		OnSync:   func(key string) { syncs <- key },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if err := w.Add(file, "myproj"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	writeFile(t, file, "changed")

	select {
	case e := <-changes:
		if e.ProjectKey != "myproj" {
			t.Fatalf("OnChange ProjectKey = %q, want myproj", e.ProjectKey)
		}
		if filepath.Clean(e.Path) != filepath.Clean(file) {
			t.Fatalf("OnChange Path = %q, want %q", e.Path, file)
		}
	case <-time.After(readTimeout):
		t.Fatal("OnChange never fired")
	}

	select {
	case key := <-syncs:
		if key != "myproj" {
			t.Fatalf("OnSync key = %q, want myproj", key)
		}
	case <-time.After(readTimeout):
		t.Fatal("OnSync never fired")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(readTimeout):
		t.Fatal("Run did not return after cancel")
	}
}

func TestWatcher_RunFlushesPendingOnCancel(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "overlay.md")
	writeFile(t, file, "initial")

	syncs := make(chan string, 16)

	// Long debounce: the timer will not fire on its own, so the only way OnSync
	// runs is via the flush on ctx cancel.
	w, err := New(Config{
		Debounce: time.Hour,
		OnSync:   func(key string) { syncs <- key },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if err := w.Add(file, "myproj"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	writeFile(t, file, "changed")

	// Wait until the change has been registered as pending, then cancel.
	deadline := time.After(readTimeout)
	for len(w.debouncer.Pending()) == 0 {
		select {
		case <-deadline:
			t.Fatal("change never became pending")
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()

	select {
	case key := <-syncs:
		if key != "myproj" {
			t.Fatalf("flushed OnSync key = %q, want myproj", key)
		}
	case <-time.After(readTimeout):
		t.Fatal("Run did not flush pending OnSync on cancel")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(readTimeout):
		t.Fatal("Run did not return after cancel")
	}
}

func TestNew_NilOnSyncErrors(t *testing.T) {
	t.Parallel()

	_, err := New(Config{Debounce: time.Second})
	if err == nil {
		t.Fatal("New with nil OnSync should error")
	}
}

func TestWatcher_AddMissingParentErrors(t *testing.T) {
	t.Parallel()

	w, err := New(Config{Debounce: time.Second, OnSync: func(string) {}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	missing := filepath.Join(t.TempDir(), "nope", "overlay.md")
	if err := w.Add(missing, "p"); err == nil {
		t.Fatal("Add with missing parent dir should error")
	}
}

func TestWatcher_AddDedupsParentDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	a := filepath.Join(dir, "a.md")
	b := filepath.Join(dir, "b.md")
	writeFile(t, a, "a")
	writeFile(t, b, "b")

	w, err := New(Config{Debounce: time.Second, OnSync: func(string) {}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if err := w.Add(a, "p"); err != nil {
		t.Fatalf("Add a: %v", err)
	}
	// Second file in the same dir must not error on the dedup path.
	if err := w.Add(b, "p"); err != nil {
		t.Fatalf("Add b: %v", err)
	}
}

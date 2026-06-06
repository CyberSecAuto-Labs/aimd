package watcher

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Event is one change notification surfaced to the caller's UI (the live log).
type Event struct {
	ProjectKey string
	Path       string // the overlay file that changed
	Time       time.Time
}

// Config configures a Watcher.
type Config struct {
	// Debounce is the quiet period each project waits, after its last change,
	// before OnSync runs. Each change resets the timer. The caller owns the
	// default (cmd/watch uses 300s); this package does not impose one.
	Debounce time.Duration
	// OnChange is called for every watched-file change (the live log). May be nil.
	OnChange func(Event)
	// OnSync is called when a project's debounce window elapses. Required.
	OnSync func(projectKey string)
}

// Watcher wraps fsnotify and routes file-change events into a per-project
// debouncer. To survive editors that save via atomic rename (which drops an
// fsnotify watch on the individual file), it watches the parent directory of
// each registered overlay file and filters events down to the registered set.
type Watcher struct {
	fsw       *fsnotify.Watcher
	debouncer *Debouncer
	onChange  func(Event)

	mu       sync.Mutex
	files    map[string]string // cleaned absolute path -> projectKey
	watchedD map[string]bool   // parent dirs already passed to fsnotify.Add
}

// New creates the fsnotify watcher and the per-project debouncer. cfg.OnSync is
// required; New returns an error if it is nil.
func New(cfg Config) (*Watcher, error) {
	if cfg.OnSync == nil {
		return nil, errors.New("watcher: Config.OnSync is required")
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}

	w := &Watcher{
		fsw:      fsw,
		onChange: cfg.OnChange,
		files:    make(map[string]string),
		watchedD: make(map[string]bool),
	}
	w.debouncer = NewDebouncer(cfg.Debounce, cfg.OnSync)
	return w, nil
}

// Add starts watching one overlay file under the given project key. It registers
// the file's parent directory with fsnotify (deduplicating so each directory is
// added at most once) and records the cleaned absolute path so the event loop
// can filter to registered files. The parent directory must exist.
func (w *Watcher) Add(path, projectKey string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve absolute path for %q: %w", path, err)
	}
	abs = filepath.Clean(abs)
	dir := filepath.Dir(abs)

	w.mu.Lock()
	defer w.mu.Unlock()

	w.files[abs] = projectKey

	if !w.watchedD[dir] {
		if err := w.fsw.Add(dir); err != nil {
			delete(w.files, abs)
			return fmt.Errorf("watch directory %q: %w", dir, err)
		}
		w.watchedD[dir] = true
	}
	return nil
}

// Run drives the event loop, blocking until ctx is cancelled. On every
// Write/Create/Rename/Remove event whose cleaned path is a registered overlay
// file, it emits OnChange and resets that project's debounce timer.
//
// When ctx is cancelled, Run FLUSHES the debouncer — running OnSync once for
// every project with a pending change — before returning. This is the
// "flush pending timers before exit" mechanism: cmd/watch (Step 16) derives ctx
// from signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM), so SIGINT/SIGTERM
// cancel ctx and thereby trigger the flush. This package stays signal-agnostic.
func (w *Watcher) Run(ctx context.Context) error {
	const relevant = fsnotify.Write | fsnotify.Create | fsnotify.Rename | fsnotify.Remove

	for {
		select {
		case <-ctx.Done():
			w.debouncer.Flush()
			return nil

		case event, ok := <-w.fsw.Events:
			if !ok {
				w.debouncer.Flush()
				return nil
			}
			if event.Op&relevant == 0 {
				continue
			}
			w.handleEvent(event.Name)

		case err, ok := <-w.fsw.Errors:
			if !ok {
				w.debouncer.Flush()
				return nil
			}
			if err != nil {
				w.debouncer.Flush()
				return fmt.Errorf("fsnotify error: %w", err)
			}
		}
	}
}

// handleEvent looks up the changed path in the registered set and, if it is a
// watched overlay file, emits OnChange and triggers the debouncer.
func (w *Watcher) handleEvent(name string) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return
	}
	abs = filepath.Clean(abs)

	w.mu.Lock()
	key, ok := w.files[abs]
	w.mu.Unlock()
	if !ok {
		return
	}

	if w.onChange != nil {
		w.onChange(Event{ProjectKey: key, Path: abs, Time: time.Now()})
	}
	w.debouncer.Trigger(key)
}

// Close stops the debouncer (dropping any unfired timers without running OnSync)
// and releases the fsnotify watcher's resources. Call Run with a cancellable ctx
// to flush pending syncs before Close.
func (w *Watcher) Close() error {
	w.debouncer.Stop()
	if err := w.fsw.Close(); err != nil {
		return fmt.Errorf("close fsnotify watcher: %w", err)
	}
	return nil
}

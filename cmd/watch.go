package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
	"github.com/CyberSecAuto-Labs/aimd/internal/store"
	"github.com/CyberSecAuto-Labs/aimd/internal/watcher"
)

var (
	watchAll      bool
	watchDebounce int
)

var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Watch tracked files and sync after a quiet period",
	Long: `Watch tracked AI context files and automatically sync each project to the
private store after a debounce window elapses.

By default watch follows only the current project (detected from the working
directory). Use --all to watch every registered project. Each change resets that
project's debounce timer; once the quiet period passes the project is synced.

Press Ctrl-C to stop — any project with pending changes is synced before exit.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return RunWatch(cmd.Context(), storePath, machine, watchDebounce, watchAll, cmd.OutOrStdout())
	},
}

// watchFile records the metadata needed to render a change for one watched
// overlay file.
type watchFile struct {
	key  string
	rel  string
	name string
}

// RunWatch is the testable core of the watch command.
//
// ctx is the parent context; cancelling it (or a SIGINT/SIGTERM delivered to the
// process) flushes any pending per-project syncs and returns.
// storeDir is the resolved path to ~/.aimd/store.
// machineName identifies the current machine.
// debounceSecs is the quiet period, in seconds, before syncing after a change.
// all watches every registered project instead of just the current one.
// out receives all user-facing output.
func RunWatch(ctx context.Context, storeDir, machineName string, debounceSecs int, all bool, out io.Writer) error {
	if err := verifyStore(storeDir); err != nil {
		return err
	}

	registryPath := filepath.Join(storeDir, ".aimd", "registry.json")
	reg, err := registry.LoadOrNew(registryPath)
	if err != nil {
		return fmt.Errorf("loading registry: %w", err)
	}

	targets, err := selectProjects(reg, machineName, all)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		_, _ = fmt.Fprintln(out, "No projects tracked. Run `aimd track <file>` to get started.")
		return nil
	}

	// Build the flat list of overlay files to watch, keyed by cleaned-abs path.
	overlays, byKey := collectOverlays(storeDir, reg, targets, out)

	debounce := time.Duration(debounceSecs) * time.Second

	// Serialize syncs: OnSync may fire concurrently from per-project timer
	// goroutines, but every project pushes to the single shared store, so a mutex
	// funnels concurrent commits/pushes into one at-a-time sequence.
	var syncMu sync.Mutex
	// syncOne runs one serialized project sync, prints the live-log lines, and
	// returns the error so callers that need it (the shutdown sweep) can surface a
	// failure as the command result. The live debounce path discards the error
	// after logging because the watcher stays alive and retries on the next event.
	syncOne := func(key string) error {
		syncMu.Lock()
		defer syncMu.Unlock()

		target, ok := byKey[key]
		if !ok {
			return nil
		}
		proj := reg.Projects[key]
		if proj == nil {
			return nil
		}
		name := proj.DisplayName
		if name == "" {
			name = filepath.Base(target.root)
		}
		_, _ = fmt.Fprintf(out, "[%s] ↑ syncing %s...\n", time.Now().Format("15:04:05"), name)
		if serr := syncProject(storeDir, key, name, proj, machineName, target.root, registryPath, dryRun, out); serr != nil {
			_, _ = fmt.Fprintf(out, "[%s] error syncing %s: %v\n", time.Now().Format("15:04:05"), name, serr)
			return serr
		}
		return nil
	}
	onSync := func(key string) {
		_ = syncOne(key)
	}

	onChange := func(e watcher.Event) {
		wf, ok := overlays[e.Path]
		if !ok {
			return
		}
		_, _ = fmt.Fprintf(out, "[%s] %s/%s modified — syncing in %s\n",
			e.Time.Format("15:04:05"), wf.name, wf.rel, formatDebounce(debounce))
	}

	w, err := watcher.New(watcher.Config{Debounce: debounce, OnChange: onChange, OnSync: onSync})
	if err != nil {
		return fmt.Errorf("creating watcher: %w", err)
	}

	contributing := make(map[string]struct{})
	for overlay, wf := range overlays {
		if aerr := w.Add(overlay, wf.key); aerr != nil {
			_, _ = fmt.Fprintf(out, "warning: not watching %s/%s: %v\n", wf.name, wf.rel, aerr)
			continue
		}
		contributing[wf.key] = struct{}{}
	}

	// Header.
	keys := make([]string, 0, len(targets))
	for _, t := range targets {
		keys = append(keys, t.key)
	}
	remote := resolveRemote(reg, keys)
	_, _ = fmt.Fprintf(out, "aimd watch • %s → %s\n", machineName, remote)
	watchedCount := len(overlays)
	projectCount := len(contributing)
	if projectCount == 0 {
		projectCount = len(targets)
	}
	_, _ = fmt.Fprintf(out, "watching %d files across %d projects  (debounce: %s)\n",
		watchedCount, projectCount, formatDebounce(debounce))

	// Derive a cancellable context from OS signals. Cancelling ctx (via signal or
	// the parent) makes Run flush pending timers before returning — the graceful
	// shutdown path.
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	runErr := w.Run(sigCtx)

	// Final dirty sweep on shutdown. w.Run's flush only covers projects with a
	// pending debounce timer; a file written just before Ctrl-C can have its
	// fsnotify event still queued (never debounced) when ctx cancellation wins
	// Run's select. Sweep every watched project so any dirty overlay is synced
	// before exit. Projects the flush already synced are clean here and skipped.
	//
	// Unlike the live path, shutdown has no retry: surface dirty-check and sync
	// failures as the command result so a non-zero exit honestly reflects that the
	// promised shutdown sync did not complete.
	var sweepErr error
	for key := range contributing {
		dirty, derr := store.OverlayProjectDirty(storeDir, key)
		if derr != nil {
			_, _ = fmt.Fprintf(out, "[%s] error checking %s: %v\n", time.Now().Format("15:04:05"), key, derr)
			sweepErr = errors.Join(sweepErr, derr)
			continue
		}
		if !dirty {
			continue
		}
		if serr := syncOne(key); serr != nil {
			sweepErr = errors.Join(sweepErr, serr)
		}
	}

	closeErr := w.Close()
	_, _ = fmt.Fprintln(out, "aimd watch stopped")
	return errors.Join(runErr, sweepErr, closeErr)
}

// collectOverlays builds the set of watchable overlay files keyed by their
// cleaned absolute path, plus a key→target index for OnSync lookups. Overlay
// files whose parent directory does not exist are skipped with a warning
// (watcher.Add needs the parent dir to exist).
func collectOverlays(storeDir string, reg *registry.Registry, targets []projectTarget, out io.Writer) (map[string]watchFile, map[string]projectTarget) {
	overlays := make(map[string]watchFile)
	byKey := make(map[string]projectTarget)
	for _, t := range targets {
		byKey[t.key] = t
		proj := reg.Projects[t.key]
		if proj == nil {
			continue
		}
		name := proj.DisplayName
		if name == "" {
			name = filepath.Base(t.root)
		}
		for _, tf := range proj.Tracked {
			overlay := filepath.Clean(filepath.Join(storeDir, "repos", t.key, tf.Path))
			if abs, aerr := filepath.Abs(overlay); aerr == nil {
				overlay = filepath.Clean(abs)
			}
			if _, serr := os.Stat(filepath.Dir(overlay)); serr != nil {
				_, _ = fmt.Fprintf(out, "warning: skipping %s/%s — overlay directory missing\n", name, tf.Path)
				continue
			}
			overlays[overlay] = watchFile{key: t.key, rel: tf.Path, name: name}
		}
	}
	return overlays, byKey
}

// formatDebounce renders a debounce duration compactly: whole minutes as "5m",
// whole seconds under a minute as "30s", otherwise time.Duration's own string.
func formatDebounce(d time.Duration) string {
	switch {
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d > 0 && d < time.Minute && d%time.Second == 0:
		return fmt.Sprintf("%ds", int(d/time.Second))
	default:
		return d.String()
	}
}

func init() {
	watchCmd.Flags().BoolVar(&watchAll, "all", false, "Watch all registered projects, not just the current one")
	watchCmd.Flags().IntVar(&watchDebounce, "debounce", 300, "Quiet period in seconds before syncing after a change")
	rootCmd.AddCommand(watchCmd)
}

package watcher

import (
	"sort"
	"sync"
	"time"
)

// Debouncer coalesces rapid Trigger calls per key into a single fire after a
// quiet period. Each Trigger resets that key's timer. fire is invoked from
// the Debouncer's own goroutine (a time.AfterFunc goroutine) and must be safe to
// call concurrently.
type Debouncer struct {
	delay time.Duration
	fire  func(key string)

	mu      sync.Mutex
	pending map[string]*entry
}

// entry tracks one key's live timer plus a generation counter. The generation
// lets a superseded or cancelled timer callback detect that it no longer owns
// the key and bail out, which keeps fire-once semantics correct even though the
// callback runs without holding the lock for the duration of fire.
type entry struct {
	timer *time.Timer
	gen   uint64
}

// NewDebouncer returns a Debouncer that calls fire(key) once delay elapses with
// no further Trigger(key). A delay <= 0 fires almost immediately (on the next
// scheduler tick) with no meaningful coalescing window.
func NewDebouncer(delay time.Duration, fire func(key string)) *Debouncer {
	return &Debouncer{
		delay:   delay,
		fire:    fire,
		pending: make(map[string]*entry),
	}
}

// Trigger (re)starts the quiet-period timer for key.
func (d *Debouncer) Trigger(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	e, ok := d.pending[key]
	if ok {
		// Stop the old timer and bump the generation so a callback that already
		// fired (and is racing for the lock) sees a stale generation and bails.
		e.timer.Stop()
		e.gen++
	} else {
		e = &entry{}
		d.pending[key] = e
	}

	gen := e.gen
	e.timer = time.AfterFunc(d.delay, func() {
		d.onTimer(key, gen)
	})
}

// onTimer runs when a key's timer elapses. It fires only if the entry still
// exists and its generation matches, guaranteeing fire-once even under races
// with Trigger/Flush/Stop. fire is called without holding the lock so it cannot
// deadlock against the Debouncer.
func (d *Debouncer) onTimer(key string, gen uint64) {
	d.mu.Lock()
	e, ok := d.pending[key]
	if !ok || e.gen != gen {
		d.mu.Unlock()
		return
	}
	delete(d.pending, key)
	d.mu.Unlock()

	d.fire(key)
}

// Pending returns the keys with an unfired timer, sorted for deterministic tests.
func (d *Debouncer) Pending() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	keys := make([]string, 0, len(d.pending))
	for k := range d.pending {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Flush stops all timers and fires each pending key once, synchronously, now.
// It fires keys in sorted order. fire is invoked without holding the lock so it
// cannot deadlock against an in-flight timer callback or the Debouncer itself.
func (d *Debouncer) Flush() {
	d.mu.Lock()
	keys := make([]string, 0, len(d.pending))
	for k, e := range d.pending {
		e.timer.Stop()
		e.gen++ // invalidate any already-fired callback racing for the lock
		keys = append(keys, k)
	}
	d.pending = make(map[string]*entry)
	d.mu.Unlock()

	sort.Strings(keys)
	for _, k := range keys {
		d.fire(k)
	}
}

// Stop stops all timers without firing, dropping every pending key.
func (d *Debouncer) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, e := range d.pending {
		e.timer.Stop()
		e.gen++
	}
	d.pending = make(map[string]*entry)
}

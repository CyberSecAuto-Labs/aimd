package watcher

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

// readTimeout is generous so tests don't flake under -race/-shuffle.
const readTimeout = 2 * time.Second

func TestDebouncer_FiresAfterQuietPeriod(t *testing.T) {
	t.Parallel()

	fired := make(chan string, 8)
	d := NewDebouncer(50*time.Millisecond, func(key string) { fired <- key })
	t.Cleanup(d.Stop)

	d.Trigger("proj")

	select {
	case got := <-fired:
		if got != "proj" {
			t.Fatalf("fired key = %q, want %q", got, "proj")
		}
	case <-time.After(readTimeout):
		t.Fatal("debouncer never fired")
	}

	// Confirm it fired exactly once.
	select {
	case got := <-fired:
		t.Fatalf("debouncer fired a second time with %q", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestDebouncer_ResetOnNewEvent(t *testing.T) {
	t.Parallel()

	const delay = 75 * time.Millisecond
	fired := make(chan string, 8)
	d := NewDebouncer(delay, func(key string) { fired <- key })
	t.Cleanup(d.Stop)

	d.Trigger("proj")

	// Well within the first window: must not have fired yet.
	time.Sleep(delay / 3)
	select {
	case got := <-fired:
		t.Fatalf("fired %q during the first quiet period; reset failed", got)
	default:
	}

	// Re-trigger, which resets the timer.
	d.Trigger("proj")

	// Shortly after the reset (but before the new window closes): still no fire.
	time.Sleep(delay / 3)
	select {
	case got := <-fired:
		t.Fatalf("fired %q before the reset window elapsed", got)
	default:
	}

	// After the final quiet period it fires exactly once.
	select {
	case got := <-fired:
		if got != "proj" {
			t.Fatalf("fired key = %q, want %q", got, "proj")
		}
	case <-time.After(readTimeout):
		t.Fatal("debouncer never fired after reset")
	}

	select {
	case got := <-fired:
		t.Fatalf("debouncer fired a second time with %q", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestDebouncer_PerKeyIsolation(t *testing.T) {
	t.Parallel()

	fired := make(chan string, 8)
	d := NewDebouncer(50*time.Millisecond, func(key string) { fired <- key })
	t.Cleanup(d.Stop)

	d.Trigger("a")

	select {
	case got := <-fired:
		if got != "a" {
			t.Fatalf("fired key = %q, want %q", got, "a")
		}
	case <-time.After(readTimeout):
		t.Fatal("key a never fired")
	}

	// b was never triggered; nothing else should fire.
	select {
	case got := <-fired:
		t.Fatalf("unexpected fire for %q", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestDebouncer_Flush(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var got []string
	// Long delay so timers never fire on their own during the test.
	d := NewDebouncer(time.Hour, func(key string) {
		mu.Lock()
		got = append(got, key)
		mu.Unlock()
	})
	t.Cleanup(d.Stop)

	d.Trigger("a")
	d.Trigger("b")

	if pending := d.Pending(); !reflect.DeepEqual(pending, []string{"a", "b"}) {
		t.Fatalf("Pending() = %v, want [a b]", pending)
	}

	d.Flush()

	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("Flush fired = %v, want [a b]", got)
	}
	if pending := d.Pending(); len(pending) != 0 {
		t.Fatalf("Pending() after Flush = %v, want empty", pending)
	}
}

func TestDebouncer_StopDropsPending(t *testing.T) {
	t.Parallel()

	fired := make(chan string, 8)
	d := NewDebouncer(time.Hour, func(key string) { fired <- key })

	d.Trigger("a")
	d.Trigger("b")
	d.Stop()

	if pending := d.Pending(); len(pending) != 0 {
		t.Fatalf("Pending() after Stop = %v, want empty", pending)
	}

	select {
	case got := <-fired:
		t.Fatalf("Stop fired %q; it must drop pending without firing", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestDebouncer_PendingSorted(t *testing.T) {
	t.Parallel()

	d := NewDebouncer(time.Hour, func(string) {})
	t.Cleanup(d.Stop)

	d.Trigger("c")
	d.Trigger("a")
	d.Trigger("b")

	if pending := d.Pending(); !reflect.DeepEqual(pending, []string{"a", "b", "c"}) {
		t.Fatalf("Pending() = %v, want sorted [a b c]", pending)
	}
}

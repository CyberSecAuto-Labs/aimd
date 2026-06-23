package output

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withMode sets the package mode for the duration of a test and restores it
// after, so the process-wide global doesn't leak between cases.
func withMode(t *testing.T, m Mode) {
	t.Helper()
	prev := mode
	mode = m
	t.Cleanup(func() { mode = prev })
}

func TestSetMode(t *testing.T) {
	t.Cleanup(func() { mode = ModeAuto })

	cases := map[string]Mode{
		"auto":   ModeAuto,
		"always": ModeAlways,
		"never":  ModeNever,
	}
	for value, want := range cases {
		if err := SetMode(value); err != nil {
			t.Fatalf("SetMode(%q) returned error: %v", value, err)
		}
		if mode != want {
			t.Errorf("SetMode(%q): mode = %d, want %d", value, mode, want)
		}
	}

	if err := SetMode("rainbow"); err == nil {
		t.Error("SetMode(\"rainbow\") = nil, want error for invalid value")
	}
}

func TestColorizeAlwaysWrapsRegardlessOfWriter(t *testing.T) {
	withMode(t, ModeAlways)

	var buf bytes.Buffer // not a TTY
	got := Colorize(&buf, Green, "ok")
	want := string(Green) + "ok" + reset
	if got != want {
		t.Errorf("Colorize always = %q, want %q", got, want)
	}
}

func TestColorizeNeverReturnsPlain(t *testing.T) {
	withMode(t, ModeNever)

	// Even to a real terminal, never mode must stay plain.
	tty := openCharDevice(t)
	if got := Colorize(tty, Red, "boom"); got != "boom" {
		t.Errorf("Colorize never = %q, want plain %q", got, "boom")
	}
}

func TestColorizeAutoPlainForNonTTY(t *testing.T) {
	withMode(t, ModeAuto)
	t.Setenv("NO_COLOR", "")

	var buf bytes.Buffer
	if got := Colorize(&buf, Yellow, "warn"); got != "warn" {
		t.Errorf("Colorize auto (buffer) = %q, want plain %q", got, "warn")
	}
}

func TestColorizeAutoHonorsNoColorEvenOnTTY(t *testing.T) {
	withMode(t, ModeAuto)
	t.Setenv("NO_COLOR", "1")

	tty := openCharDevice(t)
	if got := Colorize(tty, Green, "ok"); got != "ok" {
		t.Errorf("Colorize auto with NO_COLOR set = %q, want plain %q", got, "ok")
	}
}

func TestColorizeAutoColorsTTY(t *testing.T) {
	withMode(t, ModeAuto)
	t.Setenv("NO_COLOR", "")

	tty := openCharDevice(t)
	got := Colorize(tty, Green, "ok")
	if !strings.HasPrefix(got, string(Green)) || !strings.HasSuffix(got, reset) {
		t.Errorf("Colorize auto on TTY = %q, want it wrapped in green + reset", got)
	}
}

func TestIsTTYRejectsRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.txt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	if isTTY(f) {
		t.Error("isTTY(regular file) = true, want false")
	}
}

// openCharDevice opens /dev/null, a character device on the unix CI/dev targets
// aimd supports — enough to exercise the TTY-true branch without a real pty.
func openCharDevice(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("opening %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

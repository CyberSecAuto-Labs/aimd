// Package output centralizes terminal color for aimd's user-facing messages.
//
// Color reinforces the existing status glyphs (✓ ✎ ✗ ⚡) and result lines; it is
// never the sole carrier of meaning. ANSI escapes are emitted only when color is
// enabled for the target writer, so piped or redirected output stays plain.
package output

import (
	"fmt"
	"io"
	"os"
)

// Color is an ANSI SGR foreground color escape.
type Color string

// The small palette aimd uses. Reset closes a colored span.
const (
	Green  Color = "\x1b[32m"
	Yellow Color = "\x1b[33m"
	Red    Color = "\x1b[31m"
	reset        = "\x1b[0m"
)

// Mode controls when color is emitted.
type Mode int

const (
	// ModeAuto emits color only to a TTY and only when NO_COLOR is unset.
	ModeAuto Mode = iota
	// ModeAlways forces color regardless of the writer or NO_COLOR.
	ModeAlways
	// ModeNever disables color entirely.
	ModeNever
)

// mode is the process-wide color mode, set once from the --color flag. It
// defaults to auto so output is colored on a terminal and plain when piped.
var mode = ModeAuto

// SetMode parses a --color flag value ("auto", "always", or "never") and applies
// it. An unrecognized value is rejected so the flag fails fast.
func SetMode(value string) error {
	switch value {
	case "auto":
		mode = ModeAuto
	case "always":
		mode = ModeAlways
	case "never":
		mode = ModeNever
	default:
		return fmt.Errorf("invalid --color value %q (want auto, always, or never)", value)
	}
	return nil
}

// Colorize wraps s in the given color when color is enabled for w; otherwise it
// returns s unchanged. Callers pass the same io.Writer they write to, so the
// decision tracks that destination's TTY-ness.
func Colorize(w io.Writer, c Color, s string) string {
	if !enabled(w) {
		return s
	}
	return string(c) + s + reset
}

// enabled reports whether color should be emitted to w under the current mode.
func enabled(w io.Writer) bool {
	switch mode {
	case ModeNever:
		return false
	case ModeAlways:
		return true
	default:
		// auto: honor the NO_COLOR convention (https://no-color.org) and only
		// colorize a real terminal, never a pipe or file.
		return os.Getenv("NO_COLOR") == "" && isTTY(w)
	}
}

// isTTY reports whether w is a terminal. Only an *os.File backed by a character
// device qualifies; a bytes.Buffer (tests) or a pipe does not.
func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

package cmd

import "time"

// FormatDebounce exposes the unexported formatDebounce helper to tests in the
// cmd_test package.
func FormatDebounce(d time.Duration) string { return formatDebounce(d) }

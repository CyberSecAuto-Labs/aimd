package cmd

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CyberSecAuto-Labs/aimd/internal/project"
	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
	"github.com/CyberSecAuto-Labs/aimd/internal/store"
)

var (
	logAll   bool
	logLimit int
)

var logCmd = &cobra.Command{
	Use:   "log",
	Short: "Show the history of store changes for tracked files",
	Long: `List past store changes — what verb ran, which files it touched, on which
machine, and how long ago.

By default log reports the current project only. Use --all to report history
across every registered project, and --limit to cap the number of entries.

log is read-only and offline: it reads structured fields from git trailers in
the store's commit history, never from the human-readable subject line.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return RunLog(storePath, logAll, logLimit, cmd.OutOrStdout())
	},
}

// RunLog is the testable core of the log command.
//
// storeDir is the resolved path to ~/.aimd/store.
// all reports history across every registered project instead of just the
// current one. limit caps the number of entries shown (0 = no limit). out
// receives all user-facing output.
func RunLog(storeDir string, all bool, limit int, out io.Writer) error {
	if err := verifyStore(storeDir); err != nil {
		return err
	}

	registryPath := filepath.Join(storeDir, ".aimd", "registry.json")
	reg, err := registry.LoadOrNew(registryPath)
	if err != nil {
		return fmt.Errorf("loading registry: %w", err)
	}

	entries, err := store.Log(storeDir)
	if err != nil {
		return fmt.Errorf("reading store history: %w", err)
	}

	// Resolve scope: in single-project mode, identify the current project's key
	// (for trailer matching) and display name (for legacy-commit matching).
	var wantKey, wantName string
	if !all {
		proj, derr := project.Detect()
		if derr != nil {
			return fmt.Errorf("not inside a tracked project — cd into one or pass --all (%w)", derr)
		}
		wantKey = proj.Key
		if p, ok := registry.GetProject(reg, proj.Key); ok && p.DisplayName != "" {
			wantName = p.DisplayName
		} else {
			wantName = filepath.Base(proj.Root)
		}
	}

	scope := "all projects"
	if !all {
		scope = displayOr(wantName, wantKey)
	}
	_, _ = fmt.Fprintf(out, "aimd log • %s\n\n", scope)

	shown := 0
	for _, e := range entries {
		if !all && !entryMatchesProject(e, wantKey, wantName) {
			continue
		}
		printLogEntry(out, e, all, reg)
		shown++
		if limit > 0 && shown >= limit {
			break
		}
	}

	if shown == 0 {
		_, _ = fmt.Fprintf(out, "No history yet.\n")
	}

	return nil
}

// entryMatchesProject reports whether a log entry belongs to the project
// identified by key/name. Trailer-bearing entries match on the project key;
// legacy entries fall back to a best-effort match on the display name parsed
// from the commit subject.
func entryMatchesProject(e store.LogEntry, key, name string) bool {
	if e.ProjectKey != "" {
		return e.ProjectKey == key
	}
	return e.DisplayName != "" && e.DisplayName == name
}

// printLogEntry renders one history line. In --all mode it includes the project
// label so entries from different projects stay distinguishable.
func printLogEntry(out io.Writer, e store.LogEntry, all bool, reg *registry.Registry) {
	when := "unknown"
	if !e.When.IsZero() {
		when = relativeTime(e.When)
	}

	files := "—"
	if len(e.Files) > 0 {
		files = strings.Join(e.Files, ", ")
	} else if e.Legacy {
		files = "(files not recorded)"
	}

	machine := e.Machine
	if machine == "" {
		machine = "?"
	}

	if all {
		_, _ = fmt.Fprintf(out, "  %-10s %-9s %-18s %s  (%s)\n",
			when, logVerbLabel(e.Verb), projectLabel(e, reg), files, machine)
	} else {
		_, _ = fmt.Fprintf(out, "  %-10s %-9s %s  (%s)\n",
			when, logVerbLabel(e.Verb), files, machine)
	}
}

// logVerbLabel renders a verb as a lowercase past-tense word for the log line.
func logVerbLabel(verb string) string {
	switch verb {
	case "track":
		return "tracked"
	case "untrack":
		return "untracked"
	case "restore":
		return "restored"
	case "sync":
		return "synced"
	case "remove":
		return "removed"
	case "":
		return "changed"
	default:
		return verb
	}
}

// projectLabel resolves the human label for an entry's project, preferring the
// registry display name, then the subject-parsed name, then the raw key.
func projectLabel(e store.LogEntry, reg *registry.Registry) string {
	if e.ProjectKey != "" {
		if p, ok := reg.Projects[e.ProjectKey]; ok && p != nil && p.DisplayName != "" {
			return p.DisplayName
		}
		if e.DisplayName != "" {
			return e.DisplayName
		}
		return e.ProjectKey
	}
	if e.DisplayName != "" {
		return e.DisplayName
	}
	return "?"
}

// displayOr returns name when non-empty, else fallback.
func displayOr(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}

func init() {
	logCmd.Flags().BoolVar(&logAll, "all", false, "Show history across all registered projects")
	logCmd.Flags().IntVar(&logLimit, "limit", 20, "Maximum number of entries to show (0 = no limit)")
	rootCmd.AddCommand(logCmd)
}

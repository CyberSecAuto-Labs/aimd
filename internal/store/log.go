package store

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Control characters used to delimit the `git log` output. They cannot appear
// in commit subjects, trailer values, or file paths, so splitting on them is
// unambiguous: record (between commits), field (between columns), file (between
// repeated Aimd-File trailer values).
const (
	logRecordSep = "\x1e" // RS
	logFieldSep  = "\x1f" // US
	logFileSep   = "\x1d" // GS
)

// logFormat is the per-commit pretty-format. Structured fields come from the
// Aimd-* trailers (D22); the subject is carried only so pre-trailer commits can
// degrade to a coarse entry and so --all has a human project label.
var logFormat = strings.Join([]string{
	"%H",
	"%cI",
	"%s",
	"%(trailers:key=Aimd-Verb,valueonly)",
	"%(trailers:key=Aimd-Machine,valueonly)",
	"%(trailers:key=Aimd-Project,valueonly)",
	"%(trailers:key=Aimd-File,valueonly,separator=%x1d)",
}, logFieldSep)

// aimdSubjectRe matches an aimd store-commit subject "<verb>: <display> [<machine> <ISO>]".
// It derives coarse fields for pre-trailer (legacy) commits and distinguishes
// aimd overlay changes from the initial scaffold commit or merge commits, which
// it deliberately does not match.
var aimdSubjectRe = regexp.MustCompile(`^(\S+): (.+) \[(\S+) (\S+)\]$`)

// LogEntry is one store-history record for an aimd overlay change.
type LogEntry struct {
	Hash        string    // commit hash
	When        time.Time // committer date
	Verb        string    // resolved: Aimd-Verb trailer, else parsed from subject
	Machine     string    // resolved: Aimd-Machine trailer, else parsed from subject
	ProjectKey  string    // Aimd-Project trailer; "" for legacy commits
	DisplayName string    // human project name parsed from the subject
	Files       []string  // Aimd-File trailer values; nil for legacy commits
	Legacy      bool      // true when the commit carries no Aimd-* trailers
}

// Log returns the store's overlay-change history, most recent first. Structured
// fields are read from git trailers (D22), with a coarse subject parse as the
// fallback for pre-trailer commits. Non-aimd commits (the initial scaffold
// commit, merges, hand-made commits) are skipped.
func Log(storeDir string) ([]LogEntry, error) {
	out, err := gitCmd("-C", storeDir, "log", "--no-color",
		"--format="+logRecordSep+logFormat).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git log: %w — %s", err, strings.TrimSpace(string(out)))
	}

	var entries []LogEntry
	for _, rec := range strings.Split(string(out), logRecordSep) {
		if strings.TrimSpace(rec) == "" {
			continue
		}
		if entry, ok := parseLogRecord(rec); ok {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// parseLogRecord parses one logFormat record into a LogEntry. The second return
// is false when the record is not an aimd overlay change and should be skipped.
func parseLogRecord(rec string) (LogEntry, bool) {
	fields := strings.Split(rec, logFieldSep)
	if len(fields) < 7 {
		return LogEntry{}, false
	}

	verb := strings.TrimSpace(fields[3])
	machine := strings.TrimSpace(fields[4])
	projectKey := strings.TrimSpace(fields[5])

	var files []string
	for _, f := range strings.Split(fields[6], logFileSep) {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}

	when, _ := time.Parse(time.RFC3339, strings.TrimSpace(fields[1]))

	entry := LogEntry{
		Hash:       strings.TrimSpace(fields[0]),
		When:       when,
		Verb:       verb,
		Machine:    machine,
		ProjectKey: projectKey,
		Files:      files,
	}

	hasTrailers := verb != "" || projectKey != "" || len(files) > 0
	m := aimdSubjectRe.FindStringSubmatch(strings.TrimSpace(fields[2]))
	switch {
	case m != nil:
		// Always keep the human display name; for a legacy commit also recover
		// verb and machine from the subject.
		entry.DisplayName = m[2]
		if !hasTrailers {
			entry.Legacy = true
			entry.Verb = m[1]
			entry.Machine = m[3]
		}
	case !hasTrailers:
		// No trailers and no recognisable aimd subject → not an overlay change.
		return LogEntry{}, false
	}

	return entry, true
}

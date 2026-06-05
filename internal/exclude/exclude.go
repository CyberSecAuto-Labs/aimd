package exclude

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

const (
	// blockStart and blockEnd delimit the region of .git/info/exclude that aimd
	// owns. Everything between them is managed by aimd; user-authored lines live
	// outside the markers and are never touched.
	blockStart = "# >>> aimd managed block (do not edit by hand)"
	blockEnd   = "# <<< aimd managed block"
)

// blockHeader is written just under blockStart whenever the block is rendered.
// It explains the co-ownership contract to anyone reading the file by hand.
var blockHeader = []string{
	"# aimd keeps the files it manages hidden from git here, so they never show",
	"# up in git status. Add your own ignore patterns ABOVE or BELOW this block —",
	"# any line inside it may be rewritten by aimd.",
}

// parsed splits an exclude file into the lines before the managed block, the
// entry lines inside it, and the lines after it. found reports whether a
// managed block was present. Comment and blank lines inside the block (the
// header) are not returned as entries — only meaningful ignore patterns are.
type parsed struct {
	before  []string
	entries []string
	after   []string
	found   bool
}

func parse(data string) parsed {
	var p parsed
	if data == "" {
		return p
	}
	lines := strings.Split(data, "\n")
	// A trailing newline yields a final empty element; drop it so we don't
	// accumulate a spurious blank line on every rewrite.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	const (
		stateBefore = iota
		stateInside
		stateAfter
	)
	state := stateBefore
	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		switch state {
		case stateBefore:
			if trimmed == blockStart {
				p.found = true
				state = stateInside
				continue
			}
			p.before = append(p.before, line)
		case stateInside:
			if trimmed == blockEnd {
				state = stateAfter
				continue
			}
			// Skip comments and blank lines inside the block (the header).
			if trimmed == "" || strings.HasPrefix(strings.TrimSpace(trimmed), "#") {
				continue
			}
			p.entries = append(p.entries, trimmed)
		case stateAfter:
			p.after = append(p.after, line)
		}
	}
	return p
}

// render rebuilds the file contents from the segments around the block and the
// current entry list. When entries is empty the block is omitted entirely, so
// removing the last tracked file also strips the delimiters and header.
func render(before, entries, after []string) string {
	out := make([]string, 0, len(before)+len(blockHeader)+len(entries)+len(after)+2)
	out = append(out, before...)
	if len(entries) > 0 {
		out = append(out, blockStart)
		out = append(out, blockHeader...)
		out = append(out, entries...)
		out = append(out, blockEnd)
	}
	out = append(out, after...)
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}

// readExclude reads the exclude file, returning ("", 0o644, nil) when it does
// not yet exist. The returned mode is the existing file's permission bits so a
// rewrite preserves them.
func readExclude(path string) (string, os.FileMode, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", 0o644, nil
		}
		return "", 0, fmt.Errorf("reading exclude file: %w", err)
	}
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	return string(data), mode, nil
}

// writeExclude writes content to path atomically (temp file + rename) so a
// crash or ENOSPC mid-write cannot truncate the file and un-hide every tracked
// entry at once. Mirrors registry.Save / writeProjectMetadata.
func writeExclude(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating exclude directory: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return fmt.Errorf("writing exclude temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming exclude file into place: %w", err)
	}
	return nil
}

// AppendEntry adds entry to aimd's managed block in the .git/info/exclude file
// at excludePath, creating the block (and the file, including missing parent
// directories) lazily on first use. It is idempotent: if entry is already in
// the block, nothing is written. User-authored lines outside the block are
// preserved untouched.
func AppendEntry(excludePath, entry string) error {
	data, mode, err := readExclude(excludePath)
	if err != nil {
		return err
	}
	p := parse(data)
	if slices.Contains(p.entries, entry) {
		return nil
	}
	p.entries = append(p.entries, entry)
	return writeExclude(excludePath, render(p.before, p.entries, p.after), mode)
}

// HasEntry reports whether aimd's managed block in excludePath contains entry.
// It deliberately does not match bare lines outside the block: those belong to
// the user. Returns false, nil when the file does not exist.
func HasEntry(excludePath, entry string) (bool, error) {
	data, _, err := readExclude(excludePath)
	if err != nil {
		return false, err
	}
	return slices.Contains(parse(data).entries, entry), nil
}

// RemoveEntry removes entry from aimd's managed block in excludePath. When the
// block becomes empty as a result, the delimiters and header are removed too.
// Lines outside the block — and the whole file when no managed block exists —
// are left untouched. Returns nil when the file does not exist or entry is
// absent.
func RemoveEntry(excludePath, entry string) error {
	data, mode, err := readExclude(excludePath)
	if err != nil {
		return err
	}
	p := parse(data)
	if !p.found {
		// No managed block: nothing of ours to remove. Pre-existing bare lines
		// stay exactly as the user wrote them.
		return nil
	}

	kept := p.entries[:0]
	removed := false
	for _, e := range p.entries {
		if e == entry {
			removed = true
			continue
		}
		kept = append(kept, e)
	}
	if !removed {
		return nil
	}
	return writeExclude(excludePath, render(p.before, kept, p.after), mode)
}

// CheckIgnore reports whether git, run inside gitRoot, would ignore relPath.
// When the path is ignored it returns the matching pattern together with its
// source location (e.g. ".git/info/exclude:7") so the caller can tell the user
// exactly what still shadows the file. A non-ignored path returns
// (false, "", "", nil).
//
// It is used after untrack --restore to warn when a surviving user pattern (a
// line outside aimd's managed block, or a broader glob like ".context/") keeps
// the freshly restored file invisible to git status.
func CheckIgnore(gitRoot, relPath string) (ignored bool, source, pattern string, err error) {
	// -v prints the matching source:line:pattern; --no-index ignores whether the
	// path happens to be tracked, so we get a verdict purely from ignore rules.
	out, runErr := exec.Command("git", "-C", gitRoot, "check-ignore", "-v", "--no-index", relPath).Output()
	if runErr != nil {
		var exitErr *exec.ExitError
		// Exit status 1 means "no match" — the path is not ignored.
		if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1 {
			return false, "", "", nil
		}
		return false, "", "", fmt.Errorf("running git check-ignore: %w", runErr)
	}

	// Format: <source>:<linenum>:<pattern>\t<pathname>
	line := strings.TrimRight(string(out), "\n")
	if tab := strings.IndexByte(line, '\t'); tab >= 0 {
		line = line[:tab]
	}
	if parts := strings.SplitN(line, ":", 3); len(parts) == 3 {
		source = parts[0] + ":" + parts[1]
		pattern = parts[2]
	}
	return true, source, pattern, nil
}

package exclude

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AppendEntry appends entry to the .git/info/exclude file at excludePath.
// It is idempotent: if a line exactly equal to entry is already present, nothing is written.
// If the file does not exist, it is created (including any missing parent directories).
func AppendEntry(excludePath, entry string) error {
	present, err := HasEntry(excludePath, entry)
	if err != nil {
		return err
	}
	if present {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return fmt.Errorf("creating exclude directory: %w", err)
	}

	f, err := os.OpenFile(excludePath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("opening exclude file: %w", err)
	}

	writeErr := appendEntry(f, entry)
	if err := f.Close(); err != nil && writeErr == nil {
		return fmt.Errorf("closing exclude file: %w", err)
	}
	return writeErr
}

// appendEntry writes entry to an already-open file, ensuring a leading newline
// if the file already has content but does not end with one.
func appendEntry(f *os.File, entry string) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat exclude file: %w", err)
	}

	if info.Size() > 0 {
		buf := make([]byte, 1)
		if _, err := f.ReadAt(buf, info.Size()-1); err != nil {
			return fmt.Errorf("reading last byte of exclude file: %w", err)
		}
		if buf[0] != '\n' {
			if _, err := f.WriteString("\n"); err != nil {
				return fmt.Errorf("writing newline to exclude file: %w", err)
			}
		}
	}

	if _, err := f.WriteString(entry + "\n"); err != nil {
		return fmt.Errorf("writing entry to exclude file: %w", err)
	}
	return nil
}

// HasEntry reports whether excludePath contains a line that exactly equals entry.
// Returns false, nil when the file does not exist.
func HasEntry(excludePath, entry string) (bool, error) {
	f, err := os.Open(excludePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("opening exclude file: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == entry {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("reading exclude file: %w", err)
	}
	return false, nil
}

// RemoveEntry removes all lines that exactly equal entry from the file at excludePath.
// If the file does not exist or entry is not present, it returns nil.
func RemoveEntry(excludePath, entry string) error {
	data, err := os.ReadFile(excludePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading exclude file: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	filtered := lines[:0]
	for _, line := range lines {
		if strings.TrimRight(line, "\r") != entry {
			filtered = append(filtered, line)
		}
	}

	// Write atomically: write to a temp file then rename over the original, so a
	// crash/ENOSPC mid-write cannot truncate the file and un-hide every other
	// tracked entry at once. Mirrors registry.Save / writeProjectMetadata.
	tmp := excludePath + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(filtered, "\n")), 0o644); err != nil {
		return fmt.Errorf("writing exclude temp file: %w", err)
	}
	if err := os.Rename(tmp, excludePath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming exclude file into place: %w", err)
	}
	return nil
}

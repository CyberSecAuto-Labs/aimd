package link

import (
	"errors"
	"fmt"
	"os"
)

// createSymlink creates an OS symbolic link at dest pointing to src.
func createSymlink(src, dest string) error {
	if err := os.Symlink(src, dest); err != nil {
		return fmt.Errorf("creating symlink %s -> %s: %w", dest, src, err)
	}
	return nil
}

// verifySymlink checks whether dest is a symlink that resolves to expectedSrc.
// A broken symlink (exists but target is gone) returns (false, nil) — it is a
// detectable state, not an unexpected error.
func verifySymlink(dest, expectedSrc string) (bool, error) {
	target, err := os.Readlink(dest)
	if err != nil {
		// Symlink does not exist or is not a symlink at all.
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("reading symlink %s: %w", dest, err)
	}

	if target != expectedSrc {
		return false, nil
	}

	// Confirm the target actually exists (broken symlink check).
	if _, err := os.Stat(dest); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Symlink exists but its target is gone — broken symlink.
			return false, nil
		}
		return false, fmt.Errorf("stat symlink target %s: %w", dest, err)
	}

	return true, nil
}

// removeSymlink removes the symlink at dest.
func removeSymlink(dest string) error {
	if err := os.Remove(dest); err != nil {
		return fmt.Errorf("removing symlink %s: %w", dest, err)
	}
	return nil
}

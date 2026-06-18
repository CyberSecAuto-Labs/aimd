// Package link creates and removes symlinks between the store and project directories.
package link

import (
	"errors"
	"fmt"
)

// LinkMode identifies the strategy used to link context files into projects.
type LinkMode string //nolint:revive // "link.LinkMode" is the intentional public API name

const (
	// LinkModeSymlink uses OS symbolic links — the default for v1.
	LinkModeSymlink LinkMode = "symlink"
	// LinkModeHardlink uses hard links — not yet implemented.
	LinkModeHardlink LinkMode = "hardlink"
	// LinkModeCopy copies files — not yet implemented (containers, Codespaces).
	LinkModeCopy LinkMode = "copy"
)

// ErrNotImplemented is returned by link modes that are not yet implemented.
var ErrNotImplemented = errors.New("link mode not implemented")

// CreateLink creates a link at dest pointing to src using the given mode.
func CreateLink(src, dest string, mode LinkMode) error {
	switch mode {
	case LinkModeSymlink:
		return createSymlink(src, dest)
	case LinkModeHardlink:
		return fmt.Errorf("hardlink: %w", ErrNotImplemented)
	case LinkModeCopy:
		return fmt.Errorf("copy: %w", ErrNotImplemented)
	}
	return fmt.Errorf("unknown link mode %q: %w", mode, ErrNotImplemented)
}

// VerifyLink checks whether dest is a valid link pointing to expectedSrc.
// Returns (true, nil) when the link is correct, (false, nil) when the link is
// missing, broken, or points elsewhere, and (false, err) on unexpected errors.
func VerifyLink(dest, expectedSrc string, mode LinkMode) (bool, error) {
	switch mode {
	case LinkModeSymlink:
		return verifySymlink(dest, expectedSrc)
	case LinkModeHardlink:
		return false, fmt.Errorf("hardlink: %w", ErrNotImplemented)
	case LinkModeCopy:
		return false, fmt.Errorf("copy: %w", ErrNotImplemented)
	}
	return false, fmt.Errorf("unknown link mode %q: %w", mode, ErrNotImplemented)
}

// RemoveLink removes the link at dest using the given mode.
func RemoveLink(dest string, mode LinkMode) error {
	switch mode {
	case LinkModeSymlink:
		return removeSymlink(dest)
	case LinkModeHardlink:
		return fmt.Errorf("hardlink: %w", ErrNotImplemented)
	case LinkModeCopy:
		return fmt.Errorf("copy: %w", ErrNotImplemented)
	}
	return fmt.Errorf("unknown link mode %q: %w", mode, ErrNotImplemented)
}

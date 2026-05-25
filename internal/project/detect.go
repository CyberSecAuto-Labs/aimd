package project

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// DetectRoot runs `git rev-parse --show-toplevel` and returns the absolute
// path to the git repository root. It returns a user-friendly error when
// the current directory is not inside a git repository.
func DetectRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository: run this command from inside a git project")
	}
	return strings.TrimSpace(string(out)), nil
}

// FetchRemoteURL runs `git remote get-url origin` and returns the URL of the
// "origin" remote. It returns ErrNoRemote when no remote named "origin" is
// configured.
func FetchRemoteURL() (string, error) {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return "", ErrNoRemote
	}
	url := strings.TrimSpace(string(out))
	if url == "" {
		return "", ErrNoRemote
	}
	return url, nil
}

// Detect composes DetectRoot, FetchRemoteURL, and key derivation into a single
// call that returns a fully populated *Info. When the repository has no remote,
// the key falls back to DeriveKeyFromPath and RemoteURL is left empty.
func Detect() (*Info, error) {
	root, err := DetectRoot()
	if err != nil {
		return nil, err
	}
	return detectWithRoot(root)
}

// detectWithRoot is the inner implementation of Detect, separated so the
// nilerr linter does not flag the intentional ErrNoRemote handling.
func detectWithRoot(root string) (*Info, error) {
	remoteURL, err := FetchRemoteURL()
	if errors.Is(err, ErrNoRemote) {
		// No remote is expected — fall back to a path-derived key.
		key := DeriveKeyFromPath(root)
		return &Info{Root: root, Key: key}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fetching remote URL: %w", err)
	}

	key, err := DeriveKey(remoteURL)
	if err != nil {
		return nil, fmt.Errorf("deriving project key: %w", err)
	}
	return &Info{Root: root, RemoteURL: remoteURL, Key: key}, nil
}

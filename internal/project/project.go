package project

import "errors"

// Info holds the detected git project information.
type Info struct {
	// Root is the absolute path to the git repository root.
	Root string
	// RemoteURL is the URL of the "origin" remote, or empty if no remote exists.
	RemoteURL string
	// Key is the filesystem-safe identifier derived from RemoteURL or Root.
	Key string
}

// ErrNoRemote is returned by FetchRemoteURL when the repository has no "origin" remote.
var ErrNoRemote = errors.New("no remote named 'origin'")

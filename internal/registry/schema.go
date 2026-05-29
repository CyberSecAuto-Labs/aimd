package registry

import "time"

// Registry is the root structure persisted to registry.json.
type Registry struct {
	Version  int                 `json:"version"`
	Projects map[string]*Project `json:"projects"`
}

// Project holds metadata about a single managed project.
type Project struct {
	DisplayName string              `json:"displayName"`
	RemoteURL   string              `json:"remoteUrl"`
	Machines    map[string]*Machine `json:"machines"`
	Tracked     []TrackedFile       `json:"tracked"`
}

// Machine records where a project is checked out on a specific host.
type Machine struct {
	LocalPath string    `json:"localPath"`
	LastSeen  time.Time `json:"lastSeen"`
}

// TrackedFile describes a context file that is managed by aimd for a project.
type TrackedFile struct {
	Path    string    `json:"path"`
	AddedAt time.Time `json:"addedAt"`
	AddedBy string    `json:"addedBy"`
}

package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNotFound is returned by Load when the registry file does not exist.
var ErrNotFound = errors.New("registry file not found")

// Exists reports whether the registry file at path is present.
func Exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("checking registry existence: %w", err)
}

// Load reads and JSON-decodes the registry from the given path.
// Returns ErrNotFound if the file does not exist; callers that want an empty
// registry on first run should check Exists first and initialise with New.
func Load(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("reading registry: %w", err)
	}

	var r Registry
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parsing registry: %w", err)
	}
	return &r, nil
}

// New returns an initialised empty Registry at version 1.
func New() *Registry {
	return &Registry{
		Version:  1,
		Projects: map[string]*Project{},
	}
}

// LoadOrNew reads the registry from path; if the file does not exist it returns
// a new empty Registry. All other errors are returned to the caller.
func LoadOrNew(path string) (*Registry, error) {
	ok, err := Exists(path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return New(), nil
	}
	return Load(path)
}

// Save writes the registry to the given path atomically.
// It marshals to indented JSON, writes to path+".tmp", then renames into place.
// Parent directories are created if they do not exist.
func Save(path string, r *Registry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating registry directory: %w", err)
	}

	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding registry: %w", err)
	}
	data = append(data, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing registry temp file: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming registry into place: %w", err)
	}
	return nil
}

// UpsertProject sets or replaces the project at key in the registry.
// It initialises r.Projects if nil.
func UpsertProject(r *Registry, key string, proj *Project) {
	if r.Projects == nil {
		r.Projects = make(map[string]*Project)
	}
	r.Projects[key] = proj
}

// GetProject returns the project for the given key and whether it was found.
func GetProject(r *Registry, key string) (*Project, bool) {
	p, ok := r.Projects[key]
	return p, ok
}

// UpsertMachine sets or replaces the machine entry in the project.
// It initialises proj.Machines if nil.
func UpsertMachine(proj *Project, machineName string, m *Machine) {
	if proj.Machines == nil {
		proj.Machines = make(map[string]*Machine)
	}
	proj.Machines[machineName] = m
}

// AddTrackedFile appends tf to proj.Tracked if no entry with the same Path exists.
// Returns true if added, false if already present.
func AddTrackedFile(proj *Project, tf TrackedFile) bool {
	for _, existing := range proj.Tracked {
		if existing.Path == tf.Path {
			return false
		}
	}
	proj.Tracked = append(proj.Tracked, tf)
	return true
}

// RemoveTrackedFile removes the entry with the matching Path from proj.Tracked.
// Returns true if removed, false if not found.
func RemoveTrackedFile(proj *Project, path string) bool {
	for i, tf := range proj.Tracked {
		if tf.Path == path {
			proj.Tracked = append(proj.Tracked[:i], proj.Tracked[i+1:]...)
			return true
		}
	}
	return false
}

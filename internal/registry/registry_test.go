package registry_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
)

// ------------------------------------------------------------------
// Helpers
// ------------------------------------------------------------------

func newProject(displayName, remoteURL string) *registry.Project {
	return &registry.Project{
		DisplayName: displayName,
		RemoteURL:   remoteURL,
		Machines:    map[string]*registry.Machine{},
		Tracked:     []registry.TrackedFile{},
	}
}

func newMachine(localPath string) *registry.Machine {
	return &registry.Machine{
		LocalPath: localPath,
		LastSeen:  time.Now().UTC().Truncate(time.Second),
	}
}

// ------------------------------------------------------------------
// Exists
// ------------------------------------------------------------------

func TestExists_FilePresent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")

	r := registry.New()
	if err := registry.Save(path, r); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	ok, err := registry.Exists(path)
	if err != nil {
		t.Fatalf("Exists() error = %v", err)
	}
	if !ok {
		t.Error("Exists() = false, want true for existing file")
	}
}

func TestExists_FileMissing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")

	ok, err := registry.Exists(path)
	if err != nil {
		t.Fatalf("Exists() error = %v", err)
	}
	if ok {
		t.Error("Exists() = true, want false for missing file")
	}
}

// ------------------------------------------------------------------
// Load
// ------------------------------------------------------------------

func TestLoad_NonExistentFile_ReturnsErrNotFound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")

	_, err := registry.Load(path)
	if err == nil {
		t.Fatal("Load() expected ErrNotFound, got nil")
	}
	if !errors.Is(err, registry.ErrNotFound) {
		t.Errorf("Load() error = %v, want ErrNotFound", err)
	}
}

func TestLoad_InvalidJSON_ReturnsParseError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")

	if err := os.WriteFile(path, []byte("not valid json"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := registry.Load(path)
	if err == nil {
		t.Fatal("Load() expected parse error, got nil")
	}
	if errors.Is(err, registry.ErrNotFound) {
		t.Error("Load() returned ErrNotFound for invalid JSON, want parse error")
	}
}

func TestLoad_NewerVersion_ReturnsErrUnsupportedVersion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")

	if err := os.WriteFile(path, []byte(`{"version":2,"projects":{}}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := registry.Load(path)
	if err == nil {
		t.Fatal("Load() expected ErrUnsupportedVersion, got nil")
	}
	if !errors.Is(err, registry.ErrUnsupportedVersion) {
		t.Errorf("Load() error = %v, want ErrUnsupportedVersion", err)
	}
}

// ------------------------------------------------------------------
// New
// ------------------------------------------------------------------

func TestNew_ReturnsEmptyRegistryAtVersion1(t *testing.T) {
	t.Parallel()
	r := registry.New()
	if r == nil {
		t.Fatal("New() returned nil")
	}
	if r.Version != 1 {
		t.Errorf("Version = %d, want 1", r.Version)
	}
	if r.Projects == nil {
		t.Error("Projects map is nil, want empty map")
	}
	if len(r.Projects) != 0 {
		t.Errorf("Projects len = %d, want 0", len(r.Projects))
	}
}

// ------------------------------------------------------------------
// LoadOrNew
// ------------------------------------------------------------------

func TestLoadOrNew_NonExistentFile_ReturnsEmptyRegistry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")

	r, err := registry.LoadOrNew(path)
	if err != nil {
		t.Fatalf("LoadOrNew() unexpected error = %v", err)
	}
	if r == nil {
		t.Fatal("LoadOrNew() returned nil registry")
	}
	if r.Version != 1 {
		t.Errorf("Version = %d, want 1", r.Version)
	}
	if r.Projects == nil {
		t.Error("Projects map is nil, want empty map")
	}
	if len(r.Projects) != 0 {
		t.Errorf("Projects len = %d, want 0", len(r.Projects))
	}
}

func TestLoadOrNew_ExistingFile_LoadsRegistry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")

	original := registry.New()
	registry.UpsertProject(original, "key", newProject("app", "url"))
	if err := registry.Save(path, original); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := registry.LoadOrNew(path)
	if err != nil {
		t.Fatalf("LoadOrNew() error = %v", err)
	}
	if _, ok := registry.GetProject(got, "key"); !ok {
		t.Error("LoadOrNew() did not load existing project")
	}
}

// ------------------------------------------------------------------
// Save + Load round-trip
// ------------------------------------------------------------------

func TestSaveAndLoad_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")

	addedAt := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	lastSeen := time.Date(2026, 5, 24, 10, 30, 0, 0, time.UTC)

	original := &registry.Registry{
		Version: 1,
		Projects: map[string]*registry.Project{
			"github.com~org~my-app": {
				DisplayName: "my-app",
				RemoteURL:   "git@github.com:org/my-app.git",
				Machines: map[string]*registry.Machine{
					"macbook-pro": {
						LocalPath: "/Users/dev/code/my-app",
						LastSeen:  lastSeen,
					},
				},
				Tracked: []registry.TrackedFile{
					{
						Path:    "CLAUDE.md",
						AddedAt: addedAt,
						AddedBy: "macbook-pro",
					},
				},
			},
		},
	}

	if err := registry.Save(path, original); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := registry.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got.Version != original.Version {
		t.Errorf("Version = %d, want %d", got.Version, original.Version)
	}

	proj, ok := got.Projects["github.com~org~my-app"]
	if !ok {
		t.Fatal("project 'github.com~org~my-app' not found after round-trip")
	}
	if proj.DisplayName != "my-app" {
		t.Errorf("DisplayName = %q, want %q", proj.DisplayName, "my-app")
	}
	if proj.RemoteURL != "git@github.com:org/my-app.git" {
		t.Errorf("RemoteURL = %q, want %q", proj.RemoteURL, "git@github.com:org/my-app.git")
	}

	m, ok := proj.Machines["macbook-pro"]
	if !ok {
		t.Fatal("machine 'macbook-pro' not found after round-trip")
	}
	if m.LocalPath != "/Users/dev/code/my-app" {
		t.Errorf("LocalPath = %q, want %q", m.LocalPath, "/Users/dev/code/my-app")
	}
	if !m.LastSeen.Equal(lastSeen) {
		t.Errorf("LastSeen = %v, want %v", m.LastSeen, lastSeen)
	}

	if len(proj.Tracked) != 1 {
		t.Fatalf("Tracked len = %d, want 1", len(proj.Tracked))
	}
	tf := proj.Tracked[0]
	if tf.Path != "CLAUDE.md" {
		t.Errorf("Tracked[0].Path = %q, want %q", tf.Path, "CLAUDE.md")
	}
	if !tf.AddedAt.Equal(addedAt) {
		t.Errorf("Tracked[0].AddedAt = %v, want %v", tf.AddedAt, addedAt)
	}
	if tf.AddedBy != "macbook-pro" {
		t.Errorf("Tracked[0].AddedBy = %q, want %q", tf.AddedBy, "macbook-pro")
	}
}

// ------------------------------------------------------------------
// Atomic write
// ------------------------------------------------------------------

func TestSave_AtomicWrite_NoTmpFileAfterSave(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")

	r := registry.New()
	if err := registry.Save(path, r); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// The .tmp file must not exist after a successful save.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("expected .tmp file to be gone after Save, but it exists")
	}

	// The final file must exist.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("registry file missing after Save: %v", err)
	}
}

// ------------------------------------------------------------------
// UpsertProject / GetProject
// ------------------------------------------------------------------

func TestUpsertProject_NewKey(t *testing.T) {
	t.Parallel()
	r := registry.New()
	proj := newProject("my-app", "git@github.com:org/my-app.git")
	registry.UpsertProject(r, "github.com~org~my-app", proj)

	got, ok := r.Projects["github.com~org~my-app"]
	if !ok {
		t.Fatal("project not found after UpsertProject")
	}
	if got.DisplayName != "my-app" {
		t.Errorf("DisplayName = %q, want %q", got.DisplayName, "my-app")
	}
}

func TestUpsertProject_ReplacesExistingKey(t *testing.T) {
	t.Parallel()
	r := registry.New()
	registry.UpsertProject(r, "k", newProject("first", "url1"))
	registry.UpsertProject(r, "k", newProject("second", "url2"))

	got, ok := r.Projects["k"]
	if !ok {
		t.Fatal("project not found after second UpsertProject")
	}
	if got.DisplayName != "second" {
		t.Errorf("DisplayName = %q, want %q", got.DisplayName, "second")
	}
}

func TestUpsertProject_InitialisesNilMap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")

	// A registry whose JSON omits "projects" leaves the map nil after Load.
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	r, err := registry.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if r.Projects != nil {
		t.Fatal("expected nil Projects map after loading registry without projects")
	}

	registry.UpsertProject(r, "key", newProject("app", "url"))

	got, ok := r.Projects["key"]
	if !ok {
		t.Fatal("project not found after UpsertProject on nil map")
	}
	if got.DisplayName != "app" {
		t.Errorf("DisplayName = %q, want %q", got.DisplayName, "app")
	}
}

func TestGetProject_ExistingKey(t *testing.T) {
	t.Parallel()
	r := registry.New()
	proj := newProject("app", "url")
	registry.UpsertProject(r, "key", proj)

	got, ok := registry.GetProject(r, "key")
	if !ok {
		t.Fatal("GetProject() ok = false, want true")
	}
	if got == nil {
		t.Fatal("GetProject() returned nil project")
	}
	if got.DisplayName != "app" {
		t.Errorf("DisplayName = %q, want %q", got.DisplayName, "app")
	}
}

func TestGetProject_MissingKey(t *testing.T) {
	t.Parallel()
	r := registry.New()

	got, ok := registry.GetProject(r, "nonexistent")
	if ok {
		t.Fatal("GetProject() ok = true, want false for missing key")
	}
	if got != nil {
		t.Errorf("GetProject() returned non-nil for missing key")
	}
}

// ------------------------------------------------------------------
// UpsertMachine
// ------------------------------------------------------------------

func TestUpsertMachine_InitialisesNilMap(t *testing.T) {
	t.Parallel()
	proj := &registry.Project{DisplayName: "app", RemoteURL: "url"}
	// Machines is nil by default.
	if proj.Machines != nil {
		t.Fatal("expected nil Machines map in fresh Project")
	}

	m := newMachine("/home/dev/app")
	registry.UpsertMachine(proj, "laptop", m)

	if proj.Machines == nil {
		t.Fatal("Machines map still nil after UpsertMachine")
	}
	got, ok := proj.Machines["laptop"]
	if !ok {
		t.Fatal("machine 'laptop' not found after UpsertMachine")
	}
	if got.LocalPath != "/home/dev/app" {
		t.Errorf("LocalPath = %q, want %q", got.LocalPath, "/home/dev/app")
	}
}

func TestUpsertMachine_ReplacesExistingEntry(t *testing.T) {
	t.Parallel()
	proj := newProject("app", "url")
	registry.UpsertMachine(proj, "host", newMachine("/old/path"))
	registry.UpsertMachine(proj, "host", newMachine("/new/path"))

	got, ok := proj.Machines["host"]
	if !ok {
		t.Fatal("machine 'host' not found after second UpsertMachine")
	}
	if got.LocalPath != "/new/path" {
		t.Errorf("LocalPath = %q, want %q", got.LocalPath, "/new/path")
	}
}

// ------------------------------------------------------------------
// AddTrackedFile
// ------------------------------------------------------------------

func TestAddTrackedFile_AddsNewEntry(t *testing.T) {
	t.Parallel()
	proj := newProject("app", "url")
	tf := registry.TrackedFile{Path: "CLAUDE.md", AddedAt: time.Now().UTC(), AddedBy: "host"}

	added := registry.AddTrackedFile(proj, tf)
	if !added {
		t.Error("AddTrackedFile() = false, want true for new entry")
	}
	if len(proj.Tracked) != 1 {
		t.Errorf("Tracked len = %d, want 1", len(proj.Tracked))
	}
}

func TestAddTrackedFile_IdempotentOnDuplicatePath(t *testing.T) {
	t.Parallel()
	proj := newProject("app", "url")
	tf := registry.TrackedFile{Path: "CLAUDE.md", AddedAt: time.Now().UTC(), AddedBy: "host"}

	registry.AddTrackedFile(proj, tf)
	added := registry.AddTrackedFile(proj, tf)
	if added {
		t.Error("AddTrackedFile() = true on duplicate, want false")
	}
	if len(proj.Tracked) != 1 {
		t.Errorf("Tracked len = %d, want 1 (idempotent)", len(proj.Tracked))
	}
}

// ------------------------------------------------------------------
// RemoveTrackedFile
// ------------------------------------------------------------------

func TestRemoveTrackedFile_RemovesExistingEntry(t *testing.T) {
	t.Parallel()
	proj := newProject("app", "url")
	registry.AddTrackedFile(proj, registry.TrackedFile{Path: "CLAUDE.md", AddedAt: time.Now().UTC(), AddedBy: "host"})
	registry.AddTrackedFile(proj, registry.TrackedFile{Path: ".cursor/rules.md", AddedAt: time.Now().UTC(), AddedBy: "host"})

	removed := registry.RemoveTrackedFile(proj, "CLAUDE.md")
	if !removed {
		t.Error("RemoveTrackedFile() = false, want true for existing path")
	}
	if len(proj.Tracked) != 1 {
		t.Errorf("Tracked len = %d, want 1 after removal", len(proj.Tracked))
	}
	if proj.Tracked[0].Path != ".cursor/rules.md" {
		t.Errorf("remaining entry = %q, want %q", proj.Tracked[0].Path, ".cursor/rules.md")
	}
}

func TestRemoveTrackedFile_ReturnsFalseForMissingPath(t *testing.T) {
	t.Parallel()
	proj := newProject("app", "url")

	removed := registry.RemoveTrackedFile(proj, "nonexistent.md")
	if removed {
		t.Error("RemoveTrackedFile() = true for missing path, want false")
	}
}

// ------------------------------------------------------------------
// Concurrent write safety
// ------------------------------------------------------------------

func TestSave_ConcurrentWrites_NoCorruption(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")

	const workers = 10
	var wg sync.WaitGroup
	wg.Add(workers)
	successCount := make(chan int, workers)

	for i := range workers {
		go func(_ int) {
			defer wg.Done()
			r := &registry.Registry{
				Version: 1,
				Projects: map[string]*registry.Project{
					"proj": {
						DisplayName: "app",
						RemoteURL:   "url",
						Machines:    map[string]*registry.Machine{},
						Tracked:     []registry.TrackedFile{},
					},
				},
			}
			if registry.Save(path, r) == nil {
				successCount <- 1
			} else {
				successCount <- 0
			}
		}(i)
	}
	wg.Wait()
	close(successCount)

	// At least one write must have succeeded.
	total := 0
	for v := range successCount {
		total += v
	}
	if total == 0 {
		t.Fatal("all concurrent Save() calls failed; expected at least one success")
	}

	// Final file must be loadable with valid JSON — no corruption.
	got, err := registry.Load(path)
	if err != nil {
		t.Fatalf("Load() after concurrent writes error = %v", err)
	}
	if got.Version != 1 {
		t.Errorf("Version after concurrent writes = %d, want 1", got.Version)
	}
}

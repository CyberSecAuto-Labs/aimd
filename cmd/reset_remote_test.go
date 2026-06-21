package cmd_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/cmd"
	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
)

// newStoreWithRemote builds a store git repo wired to a fresh bare origin, so
// reset --remote's force-push has somewhere to go.
func newStoreWithRemote(t *testing.T) (base, storeDir, bareDir string) {
	t.Helper()
	base = t.TempDir()
	storeDir = filepath.Join(base, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeStoreRepo(t, storeDir)
	bareDir = filepath.Join(base, "remote.git")
	if out, err := exec.Command("git", "init", "-b", "main", "--bare", bareDir).CombinedOutput(); err != nil {
		t.Fatalf("git init bare: %v — %s", err, out)
	}
	syncGitRun(t, storeDir, "remote", "add", "origin", bareDir)
	syncGitRun(t, storeDir, "push", "origin", "HEAD:main")
	return base, storeDir, bareDir
}

// seedOffMachineProject adds a project to the store that is checked out only on
// another machine, so planReset skips it locally but the remote wipe still
// removes it everywhere.
func seedOffMachineProject(t *testing.T, storeDir, key string) {
	t.Helper()
	registryPath := filepath.Join(storeDir, ".aimd", "registry.json")
	reg, err := registry.LoadOrNew(registryPath)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	reg.Projects[key] = &registry.Project{
		DisplayName: key,
		Machines:    map[string]*registry.Machine{"other-machine": {LocalPath: "/elsewhere/" + key}},
		Tracked:     []registry.TrackedFile{{Path: "CLAUDE.md"}},
	}
	if err := registry.Save(registryPath, reg); err != nil {
		t.Fatalf("save registry: %v", err)
	}
	writeNested(t, filepath.Join(storeDir, "repos", key, "CLAUDE.md"), "# off-machine\n")
	writeNested(t, filepath.Join(storeDir, "metadata", key+".json"), "{}\n")
	syncGitRun(t, storeDir, "add", ".")
	syncGitRun(t, storeDir, "-c", "user.email=aimd-bot@cybersecauto-labs.org", "-c", "user.name=aimd-bot",
		"commit", "-m", "seed off-machine "+key)
	syncGitRun(t, storeDir, "push", "origin", "HEAD:main")
}

func writeNested(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestRunReset_RemoteWipesEverywhere(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	base, storeDir, bareDir := newStoreWithRemote(t)
	_, claude := trackFileInNewProject(t, storeDir, base, "projA", "CLAUDE.md", "# A\n")
	seedOffMachineProject(t, storeDir, "off-proj")

	// Precondition: the remote has accumulated history.
	if n := strings.TrimSpace(syncGitRun(t, bareDir, "rev-list", "--count", "main")); n == "1" {
		t.Fatalf("expected >1 remote commit before wipe, got %s", n)
	}

	var out bytes.Buffer
	in := strings.NewReader(bareDir + "\n") // type the remote URL to confirm
	if err := cmd.RunReset(storeDir, "test-machine", false, false, true, in, &out); err != nil {
		t.Fatalf("RunReset --remote: %v", err)
	}

	got := out.String()
	// The off-machine project must be named in the warning.
	if !strings.Contains(got, "off-proj") {
		t.Errorf("expected warning naming the off-machine project, got:\n%s", got)
	}
	if !strings.Contains(got, "Remote store wiped") {
		t.Errorf("expected remote-wiped confirmation, got:\n%s", got)
	}

	// This machine's file is restored to a real file.
	if isSymlink(t, claude) {
		t.Error("tracked file should be a real file after reset --remote")
	}

	// Local registry empty, local overlays gone.
	if reg := reloadRegistry(t, storeDir); len(reg.Projects) != 0 {
		t.Errorf("local registry should be empty, has %d projects", len(reg.Projects))
	}
	if entries, _ := os.ReadDir(filepath.Join(storeDir, "repos")); len(entries) != 0 {
		t.Errorf("local repos/ should be empty, has %d entries", len(entries))
	}

	// Remote history replaced with a single empty-store commit.
	if n := strings.TrimSpace(syncGitRun(t, bareDir, "rev-list", "--count", "main")); n != "1" {
		t.Errorf("remote history = %s commit(s), want 1", n)
	}

	// A fresh clone of the wiped remote is the empty store.
	fresh := t.TempDir()
	if out, err := exec.Command("git", "clone", bareDir, fresh).CombinedOutput(); err != nil {
		t.Fatalf("clone wiped remote: %v — %s", err, out)
	}
	freshReg, err := os.ReadFile(filepath.Join(fresh, ".aimd", "registry.json"))
	if err != nil {
		t.Fatalf("read fresh registry: %v", err)
	}
	if !strings.Contains(string(freshReg), `"projects": {}`) {
		t.Errorf("fresh clone registry not empty:\n%s", freshReg)
	}
	for _, sub := range []string{"repos/projA", "repos/off-proj"} {
		if _, err := os.Stat(filepath.Join(fresh, sub)); !os.IsNotExist(err) {
			t.Errorf("fresh clone should not contain %s", sub)
		}
	}
}

func TestRunReset_RemoteURLMismatchAborts(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	base, storeDir, bareDir := newStoreWithRemote(t)
	_, claude := trackFileInNewProject(t, storeDir, base, "projA", "CLAUDE.md", "# A\n")
	before := strings.TrimSpace(syncGitRun(t, bareDir, "rev-list", "--count", "main"))

	var out bytes.Buffer
	// --yes is set but must NOT bypass the typed-URL gate; the wrong input aborts.
	if err := cmd.RunReset(storeDir, "test-machine", true, false, true, strings.NewReader("not-the-url\n"), &out); err != nil {
		t.Fatalf("RunReset --remote with bad confirmation should not error, got: %v", err)
	}
	if !strings.Contains(out.String(), "Confirmation did not match") {
		t.Errorf("expected mismatch message, got:\n%s", out.String())
	}
	// Nothing changed: file still a symlink, remote history intact.
	if !isSymlink(t, claude) {
		t.Error("file should still be a symlink after an aborted wipe")
	}
	if after := strings.TrimSpace(syncGitRun(t, bareDir, "rev-list", "--count", "main")); after != before {
		t.Errorf("remote history changed on aborted wipe: before=%s after=%s", before, after)
	}
}

func TestRunReset_RemoteNoRemoteConfigured(t *testing.T) {
	// A store with no origin remote cannot be wiped remotely.
	base := t.TempDir()
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeStoreRepo(t, storeDir)

	var out bytes.Buffer
	err := cmd.RunReset(storeDir, "test-machine", false, false, true, strings.NewReader("anything\n"), &out)
	if err == nil {
		t.Fatal("reset --remote without a configured remote should error")
	}
	if !strings.Contains(err.Error(), "remote") {
		t.Errorf("expected a remote-related error, got: %v", err)
	}
}

func TestRunReset_RemoteDryRunChangesNothing(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	base, storeDir, bareDir := newStoreWithRemote(t)
	_, claude := trackFileInNewProject(t, storeDir, base, "projA", "CLAUDE.md", "# A\n")
	before := strings.TrimSpace(syncGitRun(t, bareDir, "rev-list", "--count", "main"))

	var out bytes.Buffer
	if err := cmd.RunReset(storeDir, "test-machine", false, true, true, strings.NewReader(""), &out); err != nil {
		t.Fatalf("RunReset --remote --dry-run: %v", err)
	}
	if !strings.Contains(out.String(), "WIPE the remote store") {
		t.Errorf("expected dry-run to mention the remote wipe, got:\n%s", out.String())
	}
	if !isSymlink(t, claude) {
		t.Error("dry-run must not restore the file")
	}
	if after := strings.TrimSpace(syncGitRun(t, bareDir, "rev-list", "--count", "main")); after != before {
		t.Errorf("dry-run changed remote history: before=%s after=%s", before, after)
	}
}

func TestRunReset_RemoteAbortsWhenLocalRestoreFails(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	base, storeDir, bareDir := newStoreWithRemote(t)
	_, claude := trackFileInNewProject(t, storeDir, base, "projA", "CLAUDE.md", "# A\n")

	// Break local restore by removing the overlay the restore reads from. The
	// project key is derived (not the dir name), so find it from the registry.
	reg := reloadRegistry(t, storeDir)
	var key string
	for k := range reg.Projects {
		key = k
	}
	if key == "" {
		t.Fatal("expected one tracked project")
	}
	if err := os.Remove(filepath.Join(storeDir, "repos", key, "CLAUDE.md")); err != nil {
		t.Fatalf("remove overlay: %v", err)
	}
	before := strings.TrimSpace(syncGitRun(t, bareDir, "rev-list", "--count", "main"))

	var out bytes.Buffer
	in := strings.NewReader(bareDir + "\n")
	if err := cmd.RunReset(storeDir, "test-machine", false, false, true, in, &out); err == nil {
		t.Fatal("reset --remote should fail when local restore fails")
	}
	if !strings.Contains(out.String(), "remote was left untouched") {
		t.Errorf("expected the remote-untouched message, got:\n%s", out.String())
	}
	// Remote history must be intact (wipe never ran).
	if after := strings.TrimSpace(syncGitRun(t, bareDir, "rev-list", "--count", "main")); after != before {
		t.Errorf("remote history changed despite local failure: before=%s after=%s", before, after)
	}
	_ = claude
}

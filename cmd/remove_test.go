package cmd_test

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/cmd"
	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
)

// seedRemovableProject builds a store with one registered project that has the
// given tracked files, and commits an overlay (repos/<key>/CLAUDE.md) plus
// metadata (metadata/<key>.json) into the store so `git rm` has real targets.
func seedRemovableProject(t *testing.T, key, display string, tracked []registry.TrackedFile) string {
	t.Helper()
	storeDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeStoreRepo(t, storeDir)

	reg := registry.New()
	reg.Projects[key] = &registry.Project{
		DisplayName: display,
		RemoteURL:   "https://example.com/" + display + ".git",
		Machines:    map[string]*registry.Machine{},
		Tracked:     tracked,
	}
	if err := registry.Save(filepath.Join(storeDir, ".aimd", "registry.json"), reg); err != nil {
		t.Fatalf("save registry: %v", err)
	}

	overlayDir := filepath.Join(storeDir, "repos", key)
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "CLAUDE.md"), []byte("# ctx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	metaFile := filepath.Join(storeDir, "metadata", key+".json")
	if err := os.WriteFile(metaFile, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gitStore(t, storeDir, "add", ".")
	gitStore(t, storeDir, "-c", "user.email=aimd@localhost", "-c", "user.name=aimd",
		"commit", "-m", "seed: overlay and metadata")
	return storeDir
}

func gitStore(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func reloadRegistry(t *testing.T, storeDir string) *registry.Registry {
	t.Helper()
	reg, err := registry.Load(filepath.Join(storeDir, ".aimd", "registry.json"))
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	return reg
}

func TestRunRemove_ExplicitByDisplayName(t *testing.T) {
	const key, display = "github.com~acme~app", "app"
	storeDir := seedRemovableProject(t, key, display, nil)

	var buf bytes.Buffer
	if err := cmd.RunRemove([]string{display}, storeDir, "test-machine",
		false, true, false, strings.NewReader(""), &buf); err != nil {
		t.Fatalf("RunRemove: %v", err)
	}

	if _, ok := reloadRegistry(t, storeDir).Projects[key]; ok {
		t.Error("project still present in registry after remove")
	}
	if _, err := os.Stat(filepath.Join(storeDir, "repos", key)); !os.IsNotExist(err) {
		t.Errorf("overlay dir still on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(storeDir, "metadata", key+".json")); !os.IsNotExist(err) {
		t.Errorf("metadata still on disk: %v", err)
	}

	subject := strings.TrimSpace(gitStore(t, storeDir, "log", "--format=%s", "-1"))
	if !strings.HasPrefix(subject, "remove:") {
		t.Errorf("HEAD subject = %q, want remove: prefix", subject)
	}
	body := gitStore(t, storeDir, "log", "--format=%B", "-1")
	if !strings.Contains(body, "Aimd-Verb: remove") {
		t.Errorf("commit body missing Aimd-Verb: remove; got:\n%s", body)
	}
}

func TestRunRemove_ExplicitByKey(t *testing.T) {
	const key, display = "github.com~acme~app", "app"
	storeDir := seedRemovableProject(t, key, display, nil)

	var buf bytes.Buffer
	if err := cmd.RunRemove([]string{key}, storeDir, "test-machine",
		false, true, false, strings.NewReader(""), &buf); err != nil {
		t.Fatalf("RunRemove: %v", err)
	}
	if _, ok := reloadRegistry(t, storeDir).Projects[key]; ok {
		t.Error("project still present in registry after remove by key")
	}
}

func TestRunRemove_RefusesTrackedWithoutForce(t *testing.T) {
	const key, display = "github.com~acme~app", "app"
	tracked := []registry.TrackedFile{{Path: "CLAUDE.md"}}
	storeDir := seedRemovableProject(t, key, display, tracked)

	var buf bytes.Buffer
	err := cmd.RunRemove([]string{display}, storeDir, "test-machine",
		false, true, false, strings.NewReader(""), &buf)
	if err == nil {
		t.Fatal("expected error when removing a project with tracked files without --force")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention --force; got %v", err)
	}
	if _, ok := reloadRegistry(t, storeDir).Projects[key]; !ok {
		t.Error("project should still be present after refusal")
	}
}

func TestRunRemove_ForceRemovesTrackedOverlay(t *testing.T) {
	const key, display = "github.com~acme~app", "app"
	tracked := []registry.TrackedFile{{Path: "CLAUDE.md"}}
	storeDir := seedRemovableProject(t, key, display, tracked)

	var buf bytes.Buffer
	if err := cmd.RunRemove([]string{display}, storeDir, "test-machine",
		true, true, false, strings.NewReader(""), &buf); err != nil {
		t.Fatalf("RunRemove --force: %v", err)
	}
	if _, ok := reloadRegistry(t, storeDir).Projects[key]; ok {
		t.Error("project still present after --force remove")
	}
	if _, err := os.Stat(filepath.Join(storeDir, "repos", key, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Errorf("tracked overlay should be git-rm'd with --force: %v", err)
	}
	if !strings.Contains(buf.String(), "WARNING") {
		t.Errorf("expected WARNING about tracked overlays; got:\n%s", buf.String())
	}
}

func TestRunRemove_ConfirmationAbort(t *testing.T) {
	const key, display = "github.com~acme~app", "app"
	storeDir := seedRemovableProject(t, key, display, nil)
	headBefore := strings.TrimSpace(gitStore(t, storeDir, "rev-parse", "HEAD"))

	var buf bytes.Buffer
	if err := cmd.RunRemove([]string{display}, storeDir, "test-machine",
		false, false, false, strings.NewReader("n\n"), &buf); err != nil {
		t.Fatalf("RunRemove abort returned error: %v", err)
	}
	if !strings.Contains(buf.String(), "Aborted.") {
		t.Errorf("expected Aborted. output; got:\n%s", buf.String())
	}
	if _, ok := reloadRegistry(t, storeDir).Projects[key]; !ok {
		t.Error("project should remain after abort")
	}
	headAfter := strings.TrimSpace(gitStore(t, storeDir, "rev-parse", "HEAD"))
	if headBefore != headAfter {
		t.Errorf("abort should not add a commit: %s -> %s", headBefore, headAfter)
	}
}

func TestRunRemove_DryRun(t *testing.T) {
	const key, display = "github.com~acme~app", "app"
	storeDir := seedRemovableProject(t, key, display, nil)
	headBefore := strings.TrimSpace(gitStore(t, storeDir, "rev-parse", "HEAD"))

	var buf bytes.Buffer
	if err := cmd.RunRemove([]string{display}, storeDir, "test-machine",
		false, true, true, strings.NewReader(""), &buf); err != nil {
		t.Fatalf("RunRemove dry-run: %v", err)
	}
	if !strings.Contains(buf.String(), "dry-run") {
		t.Errorf("expected dry-run output; got:\n%s", buf.String())
	}
	if _, ok := reloadRegistry(t, storeDir).Projects[key]; !ok {
		t.Error("dry-run must not mutate the registry")
	}
	headAfter := strings.TrimSpace(gitStore(t, storeDir, "rev-parse", "HEAD"))
	if headBefore != headAfter {
		t.Error("dry-run must not add a commit")
	}
}

func TestRunRemove_NotFound(t *testing.T) {
	storeDir := seedRemovableProject(t, "github.com~acme~app", "app", nil)
	var buf bytes.Buffer
	err := cmd.RunRemove([]string{"nonexistent"}, storeDir, "test-machine",
		false, true, false, strings.NewReader(""), &buf)
	if err == nil || !strings.Contains(err.Error(), "no such project") {
		t.Fatalf("expected no-such-project error, got %v", err)
	}
}

// TestRunRemove_Integration is the end-to-end anchor: track a file, untrack the
// last one (leaving a lingering empty project), confirm status --all shows the
// remove hint, then remove the current project (no arg) and confirm it is gone.
func TestRunRemove_Integration(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	projectDir, storeDir, _, _ := setupTrackedFile(t)

	// Untrack the only file (restore mode, yes) → project lingers with Tracked: [].
	if err := cmd.RunUntrack([]string{"CLAUDE.md"}, storeDir, "test-machine",
		false, true, false, strings.NewReader(""), io.Discard); err != nil {
		t.Fatalf("RunUntrack: %v", err)
	}

	var statusBuf bytes.Buffer
	if err := cmd.RunStatus(storeDir, "test-machine", true, false, false, &statusBuf); err != nil {
		t.Fatalf("RunStatus --all: %v", err)
	}
	if !strings.Contains(statusBuf.String(), "aimd remove") {
		t.Errorf("expected `aimd remove` hint in status --all; got:\n%s", statusBuf.String())
	}

	// Determine the project key for the post-remove assertion.
	regBefore := reloadRegistry(t, storeDir)
	if len(regBefore.Projects) != 1 {
		t.Fatalf("expected exactly one lingering project, got %d", len(regBefore.Projects))
	}
	var key string
	for k := range regBefore.Projects {
		key = k
	}

	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	var rmBuf bytes.Buffer
	if err := cmd.RunRemove(nil, storeDir, "test-machine",
		false, true, false, strings.NewReader(""), &rmBuf); err != nil {
		t.Fatalf("RunRemove (current project): %v", err)
	}
	if _, ok := reloadRegistry(t, storeDir).Projects[key]; ok {
		t.Error("project should be gone from registry after remove")
	}

	var status2 bytes.Buffer
	if err := cmd.RunStatus(storeDir, "test-machine", true, false, false, &status2); err != nil {
		t.Fatalf("RunStatus --all after remove: %v", err)
	}
	if strings.Contains(status2.String(), key) {
		t.Errorf("removed project key %q should not appear in status --all:\n%s", key, status2.String())
	}
}

func TestRunRemove_NoArgOutsideProject(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	storeDir := seedRemovableProject(t, "github.com~acme~app", "app", nil)

	// cd somewhere that is not a git project.
	nonProject := t.TempDir()
	if err := os.Chdir(nonProject); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	err = cmd.RunRemove(nil, storeDir, "test-machine",
		false, true, false, strings.NewReader(""), &buf)
	if err == nil {
		t.Fatal("expected error when running remove with no arg outside a project")
	}
}

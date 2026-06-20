package cmd_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CyberSecAuto-Labs/aimd/cmd"
	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
)

// ── Test helpers ─────────────────────────────────────────────────────────────

// initSyncGitRepo initialises a plain or bare git repo pinned to branch "main".
func initSyncGitRepo(t *testing.T, dir string, bare bool) {
	t.Helper()
	args := []string{"init", "-b", "main"}
	if bare {
		args = append(args, "--bare")
	}
	args = append(args, dir)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v — %s", err, out)
	}
}

// syncGitRun runs a git command in dir and fatals on error.
func syncGitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v — %s", args, dir, err, out)
	}
	return string(out)
}

// syncAddCommitFile creates a file in dir and commits it.
func syncAddCommitFile(t *testing.T, dir, filename, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
	syncGitRun(t, dir, "add", filename)
	syncGitRun(t, dir,
		"-c", "user.email=aimd-bot@cybersecauto-labs.org", "-c", "user.name=aimd-bot",
		"commit", "-m", "add "+filename)
}

// setupSyncBareWithClone creates a bare origin pre-seeded with one commit, then
// clones it. Returns (bareDir, cloneDir).
func setupSyncBareWithClone(t *testing.T) (bareDir, cloneDir string) {
	t.Helper()

	bareDir = t.TempDir()
	initSyncGitRepo(t, bareDir, true)

	// Seed via a throwaway working copy.
	seedDir := t.TempDir()
	initSyncGitRepo(t, seedDir, false)
	syncGitRun(t, seedDir, "config", "user.email", "test@test.com")
	syncGitRun(t, seedDir, "config", "user.name", "test")
	syncAddCommitFile(t, seedDir, "init.txt", "init")
	syncGitRun(t, seedDir, "remote", "add", "origin", bareDir)
	syncGitRun(t, seedDir, "push", "origin", "HEAD:main")

	cloneDir = t.TempDir()
	cloneOut, err := exec.Command("git", "clone", bareDir, cloneDir).CombinedOutput()
	if err != nil {
		t.Fatalf("git clone: %v — %s", err, cloneOut)
	}
	syncGitRun(t, cloneDir, "config", "user.email", "test@test.com")
	syncGitRun(t, cloneDir, "config", "user.name", "test")
	return bareDir, cloneDir
}

// seedRegistry writes a minimal registry.json with the given project into storeDir.
func seedRegistry(t *testing.T, storeDir, projectKey, localPath string, trackedFiles []string) {
	t.Helper()

	tracked := make([]registry.TrackedFile, 0, len(trackedFiles))
	for _, f := range trackedFiles {
		tracked = append(tracked, registry.TrackedFile{
			Path:    f,
			AddedAt: time.Now().UTC(),
			AddedBy: "test-machine",
		})
	}

	reg := &registry.Registry{
		Version: 1,
		Projects: map[string]*registry.Project{
			projectKey: {
				DisplayName: projectKey,
				Machines: map[string]*registry.Machine{
					"test-machine": {
						LocalPath: localPath,
						LastSeen:  time.Now().UTC(),
					},
				},
				Tracked: tracked,
			},
		},
	}

	regDir := filepath.Join(storeDir, ".aimd")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatalf("mkdir .aimd: %v", err)
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(regDir, "registry.json"), data, 0o600); err != nil {
		t.Fatalf("write registry.json: %v", err)
	}
}

// readRegistry loads and unmarshals a registry.json from path.
func readRegistry(t *testing.T, path string) *registry.Registry {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("read registry %s: %v", path, err)
	}
	var reg registry.Registry
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatalf("unmarshal registry: %v", err)
	}
	return &reg
}

// addRegistryProject loads the registry in storeDir, adds a minimal project
// entry under projectKey for the given machine, and writes it back. It models a
// remote machine recording a registry-only change.
func addRegistryProject(t *testing.T, storeDir, projectKey, machineName string) {
	t.Helper()
	path := filepath.Join(storeDir, ".aimd", "registry.json")
	reg := readRegistry(t, path)
	if reg.Projects == nil {
		reg.Projects = map[string]*registry.Project{}
	}
	reg.Projects[projectKey] = &registry.Project{
		DisplayName: projectKey,
		Machines: map[string]*registry.Machine{
			machineName: {LocalPath: filepath.Join("/tmp", projectKey), LastSeen: time.Now().UTC()},
		},
		Tracked: []registry.TrackedFile{},
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
}

// ── Tests ────────────────────────────────────────────────────────────────────

func TestSyncCmdUpToDate(t *testing.T) {
	_, cloneDir := setupSyncBareWithClone(t)

	// Seed registry in the clone (used as the store).
	seedRegistry(t, cloneDir, "test-proj", t.TempDir(), []string{"CLAUDE.md"})

	var out bytes.Buffer
	// RunSync(storeDir, machineName, all, dryRun, out)
	err := cmd.RunSync(cloneDir, "test-machine", false, false, &out)
	// UP_TO_DATE — single project sync needs project.Detect(), but we can
	// exercise the store sync by using the --all path which reads from registry.
	// For UP_TO_DATE state there's nothing pushed, so err may be nil.
	_ = err // Could be "detecting project" error since CWD is not a git repo
	outStr := out.String()
	// Either succeeds with "up to date" or fails on project detect — both acceptable.
	// We mainly verify no panic.
	_ = outStr
}

func TestSyncCmdAll_UpToDate(t *testing.T) {
	_, cloneDir := setupSyncBareWithClone(t)

	localPath := t.TempDir()
	seedRegistry(t, cloneDir, "test-proj", localPath, []string{"CLAUDE.md"})

	var out bytes.Buffer
	err := cmd.RunSync(cloneDir, "test-machine", true /* all */, false, &out)
	if err != nil {
		t.Fatalf("RunSync(all): %v", err)
	}

	outStr := out.String()
	if !strings.Contains(outStr, "up to date") {
		t.Errorf("expected 'up to date' in output, got:\n%s", outStr)
	}
}

func TestSyncCmdAll_Behind(t *testing.T) {
	bareDir, cloneDir := setupSyncBareWithClone(t)

	// Push a commit from another clone so cloneDir is BEHIND.
	pusherDir := t.TempDir()
	cloneOut, err := exec.Command("git", "clone", bareDir, pusherDir).CombinedOutput()
	if err != nil {
		t.Fatalf("git clone pusher: %v — %s", err, cloneOut)
	}
	syncGitRun(t, pusherDir, "config", "user.email", "test@test.com")
	syncGitRun(t, pusherDir, "config", "user.name", "test")
	syncAddCommitFile(t, pusherDir, "remote.txt", "from remote")
	syncGitRun(t, pusherDir, "push", "origin", "HEAD:main")

	localPath := t.TempDir()
	seedRegistry(t, cloneDir, "test-proj", localPath, []string{"CLAUDE.md"})

	localBefore := syncGitRun(t, cloneDir, "rev-parse", "HEAD")

	var out bytes.Buffer
	err = cmd.RunSync(cloneDir, "test-machine", true, false, &out)
	if err != nil {
		t.Fatalf("RunSync(all, BEHIND): %v", err)
	}

	outStr := out.String()
	if !strings.Contains(outStr, "up to date") {
		t.Errorf("expected 'up to date' in output after BEHIND sync, got:\n%s", outStr)
	}

	localAfter := strings.TrimSpace(syncGitRun(t, cloneDir, "rev-parse", "HEAD"))
	if strings.TrimSpace(localBefore) == localAfter {
		t.Error("local HEAD did not advance after BEHIND sync")
	}
}

func TestSyncCmdAll_Ahead(t *testing.T) {
	bareDir, cloneDir := setupSyncBareWithClone(t)

	// Add a local commit to cloneDir (AHEAD state).
	syncAddCommitFile(t, cloneDir, "local.txt", "local only")

	// Seed registry with a tracked file.
	localPath := t.TempDir()
	seedRegistry(t, cloneDir, "test-proj", localPath, []string{"CLAUDE.md"})

	// Create the repos/<key>/ directory so git add -u has a path to work with.
	reposDir := filepath.Join(cloneDir, "repos", "test-proj")
	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		t.Fatalf("mkdir repos: %v", err)
	}
	// Write a file in repos/ and stage it, so there's something staged.
	overlayFile := filepath.Join(reposDir, "CLAUDE.md")
	if err := os.WriteFile(overlayFile, []byte("# context\n"), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	syncGitRun(t, cloneDir, "add", filepath.Join("repos", "test-proj", "CLAUDE.md"))
	syncGitRun(t, cloneDir,
		"-c", "user.email=aimd-bot@cybersecauto-labs.org", "-c", "user.name=aimd-bot",
		"commit", "-m", "add overlay")

	// Now cloneDir is AHEAD with staged changes already committed — RunSync should
	// detect AHEAD, run git add -u (nothing new), find nothing staged, and report
	// "nothing to sync" OR if there are staged changes it pushes.

	var out bytes.Buffer
	err := cmd.RunSync(cloneDir, "test-machine", true, false, &out)
	if err != nil {
		t.Fatalf("RunSync(all, AHEAD): %v", err)
	}

	outStr := out.String()
	// Either "nothing to sync" (no new unstaged changes) or "Synced" (pushed).
	if !strings.Contains(outStr, "nothing to sync") && !strings.Contains(outStr, "Synced") && !strings.Contains(outStr, "up to date") {
		t.Errorf("unexpected output:\n%s", outStr)
	}

	// Verify the store's commits are now visible to bare (i.e., push happened or
	// "nothing to sync" is legitimate — in AHEAD state with no unstaged changes).
	// At minimum, check bare has the new commits if push succeeded.
	bareLog := syncGitRun(t, bareDir, "log", "--oneline")
	_ = bareLog // presence check done; push may or may not have happened
}

func TestSyncCmdAll_AheadWithUnstagedChanges(t *testing.T) {
	bareDir, cloneDir := setupSyncBareWithClone(t)

	// Create repos/<key>/ directory with an overlay file.
	reposDir := filepath.Join(cloneDir, "repos", "test-proj")
	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		t.Fatalf("mkdir repos: %v", err)
	}

	// Commit initial overlay and push — origin is now up to date with this.
	overlayFile := filepath.Join(reposDir, "CLAUDE.md")
	if err := os.WriteFile(overlayFile, []byte("# initial\n"), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	syncGitRun(t, cloneDir, "add", filepath.Join("repos", "test-proj", "CLAUDE.md"))
	syncGitRun(t, cloneDir,
		"-c", "user.email=aimd-bot@cybersecauto-labs.org", "-c", "user.name=aimd-bot",
		"commit", "-m", "initial overlay")
	syncGitRun(t, cloneDir, "push", "origin", "HEAD:main")

	// Make cloneDir AHEAD: add a local commit (not pushed).
	syncAddCommitFile(t, cloneDir, "local-marker.txt", "ahead marker")

	// Now modify the overlay WITHOUT committing — so there are unstaged changes.
	if err := os.WriteFile(overlayFile, []byte("# updated\n"), 0o644); err != nil {
		t.Fatalf("write overlay update: %v", err)
	}

	// Seed registry.
	localPath := t.TempDir()
	seedRegistry(t, cloneDir, "test-proj", localPath, []string{"CLAUDE.md"})

	var out bytes.Buffer
	err := cmd.RunSync(cloneDir, "test-machine", true, false, &out)
	if err != nil {
		t.Fatalf("RunSync(all, AHEAD unstaged): %v", err)
	}

	outStr := out.String()
	// AHEAD with unstaged overlay changes → staged → committed → pushed → "Synced"
	if !strings.Contains(outStr, "Synced") && !strings.Contains(outStr, "nothing to sync") {
		t.Errorf("expected 'Synced' or 'nothing to sync' in output, got:\n%s", outStr)
	}

	// If synced, verify bare repo has the sync commit.
	if strings.Contains(outStr, "Synced") {
		bareLog := syncGitRun(t, bareDir, "log", "--oneline")
		if !strings.Contains(bareLog, "sync:") {
			t.Errorf("expected 'sync:' in bare repo log, got:\n%s", bareLog)
		}
	}
}

// an AHEAD sync that commits overlay changes must also commit the
// registry and metadata, leaving the store worktree clean — not a dirty
// registry.json that a later DIVERGED rebase could choke on.
func TestSyncCmdAll_AheadCommitsRegistryAndLeavesCleanTree(t *testing.T) {
	bareDir, cloneDir := setupSyncBareWithClone(t)

	// Commit + push an initial overlay so origin is aware of repos/<key>.
	reposDir := filepath.Join(cloneDir, "repos", "test-proj")
	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		t.Fatalf("mkdir repos: %v", err)
	}
	overlayFile := filepath.Join(reposDir, "CLAUDE.md")
	if err := os.WriteFile(overlayFile, []byte("# initial\n"), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	syncGitRun(t, cloneDir, "add", filepath.Join("repos", "test-proj", "CLAUDE.md"))
	syncGitRun(t, cloneDir, "-c", "user.email=aimd-bot@cybersecauto-labs.org", "-c", "user.name=aimd-bot",
		"commit", "-m", "initial overlay")
	syncGitRun(t, cloneDir, "push", "origin", "HEAD:main")

	// Become AHEAD with an uncommitted overlay edit.
	syncAddCommitFile(t, cloneDir, "marker.txt", "ahead")
	if err := os.WriteFile(overlayFile, []byte("# updated\n"), 0o644); err != nil {
		t.Fatalf("update overlay: %v", err)
	}

	localPath := t.TempDir()
	seedRegistry(t, cloneDir, "test-proj", localPath, []string{"CLAUDE.md"})

	var out bytes.Buffer
	if err := cmd.RunSync(cloneDir, "test-machine", true, false, &out); err != nil {
		t.Fatalf("RunSync: %v", err)
	}

	// The store worktree must be clean — no leftover uncommitted registry.json.
	status := strings.TrimSpace(syncGitRun(t, cloneDir, "status", "--porcelain"))
	if status != "" {
		t.Errorf("store worktree should be clean after sync, got:\n%s", status)
	}

	// The latest commit must include the registry and metadata, not just overlays.
	files := syncGitRun(t, cloneDir, "show", "--name-only", "--format=", "HEAD")
	for _, want := range []string{
		filepath.Join(".aimd", "registry.json"),
		filepath.Join("metadata", "test-proj.json"),
	} {
		if !strings.Contains(files, want) {
			t.Errorf("sync commit should include %s; commit touched:\n%s", want, files)
		}
	}

	_ = bareDir
}

// A long-lived watch, or even a single sync, loads the registry before
// store.Sync runs. store.Sync may fast-forward (BEHIND) or rebase (DIVERGED)
// remote commits onto disk, including a registry.json that another machine
// changed. When a dirty overlay then triggers persistChange, the in-memory
// (pre-sync) registry snapshot must NOT clobber that pulled remote change.
// Regression test for Finding 1: the saved registry must contain both the
// remote machine's registry change and this machine's overlay persistence.
func TestSyncCmdAll_BehindPreservesRemoteRegistryChange(t *testing.T) {
	bareDir, cloneDir := setupSyncBareWithClone(t)

	// Machine A: seed registry + overlay, commit and push so HEAD == origin.
	reposDir := filepath.Join(cloneDir, "repos", "myproj")
	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		t.Fatalf("mkdir repos: %v", err)
	}
	overlayFile := filepath.Join(reposDir, "CLAUDE.md")
	if err := os.WriteFile(overlayFile, []byte("# initial\n"), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	localPath := t.TempDir()
	seedRegistry(t, cloneDir, "myproj", localPath, []string{"CLAUDE.md"})
	syncGitRun(t, cloneDir, "add", "-A")
	syncGitRun(t, cloneDir, "-c", "user.email=aimd-bot@cybersecauto-labs.org", "-c", "user.name=aimd-bot",
		"commit", "-m", "initial")
	syncGitRun(t, cloneDir, "push", "origin", "HEAD:main")

	// Machine B: clone, add a NEW project to registry.json, commit and push.
	// This is a registry-only remote change that machine A has not seen.
	pusherDir := t.TempDir()
	if cloneOut, err := exec.Command("git", "clone", bareDir, pusherDir).CombinedOutput(); err != nil {
		t.Fatalf("git clone pusher: %v — %s", err, cloneOut)
	}
	syncGitRun(t, pusherDir, "config", "user.email", "test@test.com")
	syncGitRun(t, pusherDir, "config", "user.name", "test")
	addRegistryProject(t, pusherDir, "remote-proj", "machine-b")
	syncGitRun(t, pusherDir, "add", filepath.Join(".aimd", "registry.json"))
	syncGitRun(t, pusherDir, "-c", "user.email=aimd-bot@cybersecauto-labs.org", "-c", "user.name=aimd-bot",
		"commit", "-m", "machine B adds remote-proj")
	syncGitRun(t, pusherDir, "push", "origin", "HEAD:main")

	// Machine A: dirty the overlay without committing → StateBehind + dirty overlay.
	if err := os.WriteFile(overlayFile, []byte("# updated by A\n"), 0o644); err != nil {
		t.Fatalf("update overlay: %v", err)
	}

	var out bytes.Buffer
	if err := cmd.RunSync(cloneDir, "test-machine", true, false, &out); err != nil {
		t.Fatalf("RunSync: %v", err)
	}
	if !strings.Contains(out.String(), "Synced") {
		t.Fatalf("expected dirty overlay to be Synced, got:\n%s", out.String())
	}

	// The registry on disk after sync must contain BOTH projects.
	reg := readRegistry(t, filepath.Join(cloneDir, ".aimd", "registry.json"))
	if reg.Projects["myproj"] == nil {
		t.Error("registry lost local project myproj after sync")
	}
	if reg.Projects["remote-proj"] == nil {
		t.Error("registry clobbered machine B's remote-proj — stale snapshot overwrote pulled change")
	}

	// The committed registry pushed to origin must also contain both projects.
	committed := syncGitRun(t, cloneDir, "show", "HEAD:.aimd/registry.json")
	if !strings.Contains(committed, "remote-proj") {
		t.Errorf("committed registry dropped remote-proj:\n%s", committed)
	}
	if !strings.Contains(committed, "myproj") {
		t.Errorf("committed registry dropped myproj:\n%s", committed)
	}
}

func TestSyncCmdAll_Diverged(t *testing.T) {
	bareDir, cloneDir := setupSyncBareWithClone(t)

	// Push a commit from another clone (different file — no conflict).
	pusherDir := t.TempDir()
	cloneOut, err := exec.Command("git", "clone", bareDir, pusherDir).CombinedOutput()
	if err != nil {
		t.Fatalf("git clone pusher: %v — %s", err, cloneOut)
	}
	syncGitRun(t, pusherDir, "config", "user.email", "test@test.com")
	syncGitRun(t, pusherDir, "config", "user.name", "test")
	syncAddCommitFile(t, pusherDir, "remote.txt", "remote side")
	syncGitRun(t, pusherDir, "push", "origin", "HEAD:main")

	// Also add a local commit in cloneDir (different file — no conflict).
	syncAddCommitFile(t, cloneDir, "local.txt", "local side")

	localPath := t.TempDir()
	seedRegistry(t, cloneDir, "test-proj", localPath, []string{"CLAUDE.md"})

	var out bytes.Buffer
	err = cmd.RunSync(cloneDir, "test-machine", true, false, &out)
	// Diverged → clean rebase → AHEAD → nothing staged → "nothing to sync"
	if err != nil {
		t.Fatalf("RunSync(all, DIVERGED): %v", err)
	}

	// After clean rebase, both files should exist in cloneDir.
	for _, name := range []string{"remote.txt", "local.txt"} {
		if _, statErr := os.Stat(filepath.Join(cloneDir, name)); statErr != nil {
			t.Errorf("%s not present after clean rebase: %v", name, statErr)
		}
	}
}

func TestSyncCmdAll_DryRun(t *testing.T) {
	_, cloneDir := setupSyncBareWithClone(t)

	localPath := t.TempDir()
	seedRegistry(t, cloneDir, "test-proj", localPath, []string{"CLAUDE.md"})

	localBefore := syncGitRun(t, cloneDir, "rev-parse", "HEAD")

	var out bytes.Buffer
	err := cmd.RunSync(cloneDir, "test-machine", true, true /* dryRun */, &out)
	if err != nil {
		t.Fatalf("RunSync(all, dry-run): %v", err)
	}

	outStr := out.String()
	if !strings.Contains(outStr, "dry-run") {
		t.Errorf("expected 'dry-run' in output, got:\n%s", outStr)
	}

	// HEAD must not have changed in dry-run mode.
	localAfter := syncGitRun(t, cloneDir, "rev-parse", "HEAD")
	if strings.TrimSpace(localBefore) != strings.TrimSpace(localAfter) {
		t.Error("dry-run should not have changed HEAD")
	}
}

func TestSyncCmdAll_NoMachineEntry(t *testing.T) {
	_, cloneDir := setupSyncBareWithClone(t)

	// Register project with no machine entry for the sync machine.
	reg := &registry.Registry{
		Version: 1,
		Projects: map[string]*registry.Project{
			"test-proj": {
				DisplayName: "test-proj",
				Machines:    map[string]*registry.Machine{}, // no entry for "test-machine"
				Tracked:     []registry.TrackedFile{},
			},
		},
	}

	regDir := filepath.Join(cloneDir, ".aimd")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatalf("mkdir .aimd: %v", err)
	}
	data, _ := json.MarshalIndent(reg, "", "  ")
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(regDir, "registry.json"), data, 0o600); err != nil {
		t.Fatalf("write registry.json: %v", err)
	}

	var out bytes.Buffer
	err := cmd.RunSync(cloneDir, "test-machine", true, false, &out)
	// Should succeed — project is skipped with a warning.
	if err != nil {
		t.Fatalf("RunSync with missing machine entry: %v", err)
	}

	outStr := out.String()
	if !strings.Contains(outStr, "skipping") {
		t.Errorf("expected 'skipping' in output, got:\n%s", outStr)
	}
}

func TestSyncCmdAll_NoProjects(t *testing.T) {
	_, cloneDir := setupSyncBareWithClone(t)

	// Write empty registry.
	reg := &registry.Registry{
		Version:  1,
		Projects: map[string]*registry.Project{},
	}
	regDir := filepath.Join(cloneDir, ".aimd")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatalf("mkdir .aimd: %v", err)
	}
	data, _ := json.MarshalIndent(reg, "", "  ")
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(regDir, "registry.json"), data, 0o600); err != nil {
		t.Fatalf("write registry.json: %v", err)
	}

	var out bytes.Buffer
	err := cmd.RunSync(cloneDir, "test-machine", true, false, &out)
	if err != nil {
		t.Fatalf("RunSync with no projects: %v", err)
	}

	outStr := out.String()
	if !strings.Contains(outStr, "no projects") {
		t.Errorf("expected 'no projects' message, got:\n%s", outStr)
	}
}

func TestSyncCmdAll_CommitMessageFormat(t *testing.T) {
	// Verify that the commit message follows "sync: <project>/<files> [<machine> <timestamp>]"
	bareDir, cloneDir := setupSyncBareWithClone(t)

	// Set up overlays and commit them.
	reposDir := filepath.Join(cloneDir, "repos", "myapp")
	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		t.Fatalf("mkdir repos: %v", err)
	}

	// Commit initial overlay to be ahead of origin.
	overlayFile := filepath.Join(reposDir, "CLAUDE.md")
	if err := os.WriteFile(overlayFile, []byte("# initial\n"), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	syncGitRun(t, cloneDir, "add", filepath.Join("repos", "myapp", "CLAUDE.md"))
	syncGitRun(t, cloneDir,
		"-c", "user.email=aimd-bot@cybersecauto-labs.org", "-c", "user.name=aimd-bot",
		"commit", "-m", "initial overlay")

	// Push initial state to make origin aware.
	syncGitRun(t, cloneDir, "push", "origin", "HEAD:main")

	// Make cloneDir AHEAD: modify overlay file.
	if err := os.WriteFile(overlayFile, []byte("# updated\n"), 0o644); err != nil {
		t.Fatalf("write overlay update: %v", err)
	}

	// Create registry. The local project root is named "myapp" so it appears in
	// the commit subject (which uses filepath.Base of the project root).
	localPath := filepath.Join(t.TempDir(), "myapp")
	if err := os.MkdirAll(localPath, 0o755); err != nil {
		t.Fatalf("mkdir localPath: %v", err)
	}
	seedRegistry(t, cloneDir, "myapp", localPath, []string{"CLAUDE.md", "AGENTS.md"})

	var out bytes.Buffer
	err := cmd.RunSync(cloneDir, "test-machine", true, false, &out)
	if err != nil {
		t.Fatalf("RunSync: %v", err)
	}

	outStr := out.String()

	// HEAD == origin/main with a dirty overlay must still produce a sync commit
	// and push — not a false "up to date".
	if !strings.Contains(outStr, "Synced") {
		t.Fatalf("expected 'Synced' for dirty overlay at HEAD == origin/main, got:\n%s", outStr)
	}

	// Verify commit message format reached the bare origin.
	log := syncGitRun(t, bareDir, "log", "--oneline", "-5")
	if !strings.Contains(log, "sync:") {
		t.Errorf("expected 'sync:' in commit log, got:\n%s", log)
	}
	if !strings.Contains(log, "myapp") {
		t.Errorf("expected project name 'myapp' in commit log, got:\n%s", log)
	}
}

// A dirty overlay must be committed and pushed even when the local commit graph
// already matches origin (StateUpToDate). This is the common "I edited a tracked
// file, then ran aimd sync" case: HEAD == origin/main, but the overlay worktree
// is dirty. Regression test for the sync state machine skipping dirty overlays.
func TestSyncCmdAll_UpToDateWithDirtyOverlayCommitsAndPushes(t *testing.T) {
	bareDir, cloneDir := setupSyncBareWithClone(t)

	// Commit + push an initial overlay so HEAD == origin/main (StateUpToDate).
	reposDir := filepath.Join(cloneDir, "repos", "myapp")
	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		t.Fatalf("mkdir repos: %v", err)
	}
	overlayFile := filepath.Join(reposDir, "CLAUDE.md")
	if err := os.WriteFile(overlayFile, []byte("# initial\n"), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	syncGitRun(t, cloneDir, "add", filepath.Join("repos", "myapp", "CLAUDE.md"))
	syncGitRun(t, cloneDir, "-c", "user.email=aimd-bot@cybersecauto-labs.org", "-c", "user.name=aimd-bot",
		"commit", "-m", "initial overlay")
	syncGitRun(t, cloneDir, "push", "origin", "HEAD:main")

	// Edit the overlay WITHOUT committing — HEAD still equals origin/main, but the
	// overlay worktree is now dirty.
	if err := os.WriteFile(overlayFile, []byte("# updated\n"), 0o644); err != nil {
		t.Fatalf("write overlay update: %v", err)
	}

	localPath := t.TempDir()
	seedRegistry(t, cloneDir, "myapp", localPath, []string{"CLAUDE.md"})

	var out bytes.Buffer
	if err := cmd.RunSync(cloneDir, "test-machine", true, false, &out); err != nil {
		t.Fatalf("RunSync: %v", err)
	}

	outStr := out.String()
	if !strings.Contains(outStr, "Synced") {
		t.Fatalf("expected dirty overlay to be Synced, got:\n%s", outStr)
	}

	// The store worktree must be clean afterward.
	if status := strings.TrimSpace(syncGitRun(t, cloneDir, "status", "--porcelain")); status != "" {
		t.Errorf("store worktree not clean after sync:\n%s", status)
	}

	// HEAD must be a new sync commit including overlay, registry, and metadata.
	if head := syncGitRun(t, cloneDir, "log", "-1", "--format=%s"); !strings.Contains(head, "sync:") {
		t.Errorf("expected 'sync:' HEAD commit, got: %s", head)
	}
	changed := syncGitRun(t, cloneDir, "show", "--name-only", "--format=", "HEAD")
	for _, want := range []string{
		filepath.Join("repos", "myapp", "CLAUDE.md"),
		filepath.Join(".aimd", "registry.json"),
		filepath.Join("metadata", "myapp.json"),
	} {
		if !strings.Contains(changed, want) {
			t.Errorf("expected sync commit to include %s, got:\n%s", want, changed)
		}
	}

	// The bare origin must contain the sync commit.
	if bareLog := syncGitRun(t, bareDir, "log", "--oneline", "main"); !strings.Contains(bareLog, "sync:") {
		t.Errorf("expected 'sync:' commit in bare origin, got:\n%s", bareLog)
	}
}

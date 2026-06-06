package cmd_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CyberSecAuto-Labs/aimd/cmd"
)

// statusProject describes one project to seed into a status test store.
type statusProject struct {
	key      string
	display  string
	root     string            // destination root for this machine's symlinks
	tracked  []string          // relative paths under repos/<key>/
	machines map[string]string // machineName -> localPath (for the registry block)
}

// makeStatusStore builds a committed store containing the given projects, with
// overlay files written under repos/<key>/ and a registry recording each
// project's tracked files and machines. lastSeen for every machine is set to
// `now` so relative-time output is deterministic-ish ("just now").
func makeStatusStore(t *testing.T, storeDir string, projects []statusProject) {
	t.Helper()

	for _, c := range [][]string{
		{"git", "-C", storeDir, "init"},
		{"git", "-C", storeDir, "config", "user.email", "aimd@localhost"},
		{"git", "-C", storeDir, "config", "user.name", "aimd"},
	} {
		if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", c, err, out)
		}
	}
	for _, sub := range []string{".aimd", "metadata"} {
		if err := os.MkdirAll(filepath.Join(storeDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)

	var projJSON strings.Builder
	for pi, p := range projects {
		reposDir := filepath.Join(storeDir, "repos", p.key)
		if err := os.MkdirAll(reposDir, 0o755); err != nil {
			t.Fatalf("mkdir repos: %v", err)
		}
		for _, f := range p.tracked {
			overlay := filepath.Join(reposDir, f)
			if err := os.MkdirAll(filepath.Dir(overlay), 0o755); err != nil {
				t.Fatalf("mkdir overlay parent: %v", err)
			}
			if err := os.WriteFile(overlay, []byte("# overlay for "+f+"\n"), 0o644); err != nil {
				t.Fatalf("write overlay: %v", err)
			}
		}

		if pi > 0 {
			projJSON.WriteString(",")
		}
		var trackedJSON strings.Builder
		for ti, f := range p.tracked {
			if ti > 0 {
				trackedJSON.WriteString(",")
			}
			_, _ = fmt.Fprintf(&trackedJSON, `{"path":%q,"addedAt":%q,"addedBy":"tester"}`, f, now)
		}
		var machinesJSON strings.Builder
		mi := 0
		for name, lp := range p.machines {
			if mi > 0 {
				machinesJSON.WriteString(",")
			}
			_, _ = fmt.Fprintf(&machinesJSON, `%q:{"localPath":%q,"lastSeen":%q}`, name, lp, now)
			mi++
		}
		_, _ = fmt.Fprintf(&projJSON,
			`%q:{"displayName":%q,"remoteUrl":"git@github.com:test/%s.git","machines":{%s},"tracked":[%s]}`,
			p.key, p.display, p.display, machinesJSON.String(), trackedJSON.String())
	}

	regJSON := `{"version":1,"projects":{` + projJSON.String() + `}}` + "\n"
	if err := os.WriteFile(filepath.Join(storeDir, ".aimd", "registry.json"), []byte(regJSON), 0o600); err != nil {
		t.Fatalf("write registry.json: %v", err)
	}

	for _, c := range [][]string{
		{"git", "-C", storeDir, "add", "."},
		{"git", "-C", storeDir, "-c", "user.email=aimd@localhost", "-c", "user.name=aimd",
			"commit", "-m", "init: scaffold aimd store"},
	} {
		if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", c, err, out)
		}
	}
}

// symlinkOverlay links projectDir/relPath -> storeDir/repos/key/relPath, the
// same shape `aimd restore` produces.
func symlinkOverlay(t *testing.T, storeDir, key, projectDir, relPath string) {
	t.Helper()
	src := filepath.Join(storeDir, "repos", key, relPath)
	dst := filepath.Join(projectDir, relPath)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir link parent: %v", err)
	}
	if err := os.Symlink(src, dst); err != nil {
		t.Fatalf("symlink %s: %v", relPath, err)
	}
}

// writeFile writes content to path, creating parent directories as needed.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// runGit runs each git argv in sequence, failing the test on the first error.
func runGit(t *testing.T, cmds [][]string) {
	t.Helper()
	for _, c := range cmds {
		if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", c, err, out)
		}
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

func TestRunStatus_Synced(t *testing.T) {
	// Not parallel — uses os.Chdir.
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	makeProjectWithRemote(t, projectDir)
	makeStatusStore(t, storeDir, []statusProject{{
		key: "github.com~test~myapp", display: "myapp", root: projectDir,
		tracked:  []string{"CLAUDE.md"},
		machines: map[string]string{"this-machine": projectDir},
	}})
	symlinkOverlay(t, storeDir, "github.com~test~myapp", projectDir, "CLAUDE.md")

	chdir(t, projectDir)

	var out bytes.Buffer
	if err := cmd.RunStatus(storeDir, "this-machine", false, false, &out); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "✓ CLAUDE.md") {
		t.Errorf("expected synced (✓) row, got:\n%s", got)
	}
	if !strings.Contains(got, "aimd • this-machine →") {
		t.Errorf("expected header line, got:\n%s", got)
	}
	if strings.Contains(got, "also tracked on") {
		t.Errorf("default view must not show cross-machine block:\n%s", got)
	}
}

func TestRunStatus_Modified(t *testing.T) {
	// Not parallel — uses os.Chdir.
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	makeProjectWithRemote(t, projectDir)
	makeStatusStore(t, storeDir, []statusProject{{
		key: "github.com~test~myapp", display: "myapp", root: projectDir,
		tracked:  []string{"CLAUDE.md"},
		machines: map[string]string{"this-machine": projectDir},
	}})
	symlinkOverlay(t, storeDir, "github.com~test~myapp", projectDir, "CLAUDE.md")

	// Edit the overlay so OverlayDirty reports uncommitted changes.
	overlay := filepath.Join(storeDir, "repos", "github.com~test~myapp", "CLAUDE.md")
	if err := os.WriteFile(overlay, []byte("# locally edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	chdir(t, projectDir)

	var out bytes.Buffer
	if err := cmd.RunStatus(storeDir, "this-machine", false, false, &out); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "✎ CLAUDE.md") {
		t.Errorf("expected modified (✎) row, got:\n%s", got)
	}
	if !strings.Contains(got, "local edits not synced") {
		t.Errorf("expected modified note, got:\n%s", got)
	}
}

func TestRunStatus_Broken(t *testing.T) {
	// Not parallel — uses os.Chdir.
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	makeProjectWithRemote(t, projectDir)
	makeStatusStore(t, storeDir, []statusProject{{
		key: "github.com~test~myapp", display: "myapp", root: projectDir,
		tracked:  []string{"CLAUDE.md"},
		machines: map[string]string{"this-machine": projectDir},
	}})
	// Plant a real file (not a symlink) at the destination → broken.
	if err := os.WriteFile(filepath.Join(projectDir, "CLAUDE.md"), []byte("real"), 0o644); err != nil {
		t.Fatal(err)
	}

	chdir(t, projectDir)

	var out bytes.Buffer
	if err := cmd.RunStatus(storeDir, "this-machine", false, false, &out); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "✗ CLAUDE.md") {
		t.Errorf("expected broken (✗) row, got:\n%s", got)
	}
}

func TestRunStatus_Conflict(t *testing.T) {
	// Not parallel — uses os.Chdir.
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	makeProjectWithRemote(t, projectDir)
	makeStatusStore(t, storeDir, []statusProject{{
		key: "github.com~test~myapp", display: "myapp", root: projectDir,
		tracked:  []string{"CLAUDE.md"},
		machines: map[string]string{"this-machine": projectDir},
	}})
	symlinkOverlay(t, storeDir, "github.com~test~myapp", projectDir, "CLAUDE.md")

	// Write conflict markers into the overlay and fake an in-progress rebase by
	// creating the .git/rebase-merge directory the store helper checks for.
	overlay := filepath.Join(storeDir, "repos", "github.com~test~myapp", "CLAUDE.md")
	conflicted := "<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> origin/main\n"
	if err := os.WriteFile(overlay, []byte(conflicted), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(storeDir, ".git", "rebase-merge"), 0o755); err != nil {
		t.Fatal(err)
	}

	chdir(t, projectDir)

	var out bytes.Buffer
	if err := cmd.RunStatus(storeDir, "this-machine", false, false, &out); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "⚡ CLAUDE.md") {
		t.Errorf("expected conflict (⚡) row, got:\n%s", got)
	}
}

func TestRunStatus_EmptyRegistry(t *testing.T) {
	// Not parallel — uses os.Chdir.
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	makeProjectWithRemote(t, projectDir)
	makeStatusStore(t, storeDir, nil) // no projects

	chdir(t, projectDir)

	var out bytes.Buffer
	if err := cmd.RunStatus(storeDir, "this-machine", true, false, &out); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "No projects tracked") {
		t.Errorf("expected empty-state message, got:\n%s", got)
	}
	if strings.Contains(got, "aimd •") {
		t.Errorf("empty state must not print the header:\n%s", got)
	}
}

func TestRunStatus_All_CrossMachine(t *testing.T) {
	// Not parallel — uses os.Chdir.
	base := t.TempDir()
	storeDir := filepath.Join(base, "store")
	appA := filepath.Join(base, "appA")
	appB := filepath.Join(base, "appB")
	for _, d := range []string{storeDir, appA, appB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	makeStatusStore(t, storeDir, []statusProject{
		{
			key: "github.com~test~appA", display: "appA", root: appA,
			tracked: []string{"CLAUDE.md"},
			machines: map[string]string{
				"this-machine": appA,
				"work-desktop": "/other/path/appA",
			},
		},
		{
			key: "github.com~test~appB", display: "appB", root: appB,
			tracked:  []string{"AGENTS.md"},
			machines: map[string]string{"this-machine": appB},
		},
	})
	symlinkOverlay(t, storeDir, "github.com~test~appA", appA, "CLAUDE.md")
	symlinkOverlay(t, storeDir, "github.com~test~appB", appB, "AGENTS.md")

	var out bytes.Buffer
	if err := cmd.RunStatus(storeDir, "this-machine", true, false, &out); err != nil {
		t.Fatalf("RunStatus --all: %v", err)
	}
	got := out.String()

	if !strings.Contains(got, "appA") || !strings.Contains(got, "appB") {
		t.Errorf("expected both projects listed, got:\n%s", got)
	}
	if !strings.Contains(got, "✓ CLAUDE.md") || !strings.Contains(got, "✓ AGENTS.md") {
		t.Errorf("expected synced rows for both projects, got:\n%s", got)
	}
	if !strings.Contains(got, "also tracked on: work-desktop") {
		t.Errorf("expected cross-machine block for appA, got:\n%s", got)
	}
	// appB has only this-machine, so no cross-machine line should mention it.
	if strings.Count(got, "also tracked on:") != 1 {
		t.Errorf("expected exactly one cross-machine line, got:\n%s", got)
	}
}

// writeRegistryLastSeen overwrites the store registry so a single project's
// this-machine lastSeen is `ago` before now, letting tests exercise the
// relative-time branches deterministically.
func writeRegistryLastSeen(t *testing.T, storeDir, key, display, root string, ago time.Duration) {
	t.Helper()
	ls := time.Now().UTC().Add(-ago).Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)
	reg := fmt.Sprintf(
		`{"version":1,"projects":{%q:{"displayName":%q,"remoteUrl":"git@github.com:test/%s.git",`+
			`"machines":{"this-machine":{"localPath":%q,"lastSeen":%q}},`+
			`"tracked":[{"path":"CLAUDE.md","addedAt":%q,"addedBy":"tester"}]}}}`+"\n",
		key, display, display, root, ls, now)
	if err := os.MkdirAll(filepath.Join(storeDir, ".aimd"), 0o755); err != nil {
		t.Fatalf("mkdir .aimd: %v", err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, ".aimd", "registry.json"), []byte(reg), 0o600); err != nil {
		t.Fatalf("rewrite registry: %v", err)
	}
}

func TestRunStatus_RelativeTimeBranches(t *testing.T) {
	// Not parallel — uses os.Chdir.
	cases := []struct {
		name string
		ago  time.Duration
		want string
	}{
		{"minutes", 5 * time.Minute, "last sync 5m ago"},
		{"hours", 3 * time.Hour, "last sync 3h ago"},
		{"days", 48 * time.Hour, "last sync 2d ago"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			projectDir := filepath.Join(base, "project")
			storeDir := filepath.Join(base, "store")
			for _, d := range []string{projectDir, storeDir} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			makeProjectWithRemote(t, projectDir)
			makeStatusStore(t, storeDir, []statusProject{{
				key: "github.com~test~myapp", display: "myapp", root: projectDir,
				tracked:  []string{"CLAUDE.md"},
				machines: map[string]string{"this-machine": projectDir},
			}})
			symlinkOverlay(t, storeDir, "github.com~test~myapp", projectDir, "CLAUDE.md")
			writeRegistryLastSeen(t, storeDir, "github.com~test~myapp", "myapp", projectDir, tc.ago)

			chdir(t, projectDir)

			var out bytes.Buffer
			if err := cmd.RunStatus(storeDir, "this-machine", false, false, &out); err != nil {
				t.Fatalf("RunStatus: %v", err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Errorf("want %q in output, got:\n%s", tc.want, out.String())
			}
		})
	}
}

// TestRunStatus_FetchBehind builds a store cloned from a bare remote that has
// advanced, then runs status --fetch so the live fetch path (DetectState)
// reports the store as behind.
func TestRunStatus_FetchBehind(t *testing.T) {
	// Not parallel — uses os.Chdir.
	base := t.TempDir()
	bareDir := filepath.Join(base, "bare")
	storeDir := filepath.Join(base, "store")
	projectDir := filepath.Join(base, "project")
	for _, d := range []string{base, projectDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Seed a bare remote with one commit, then point its HEAD at main so that
	// clones check it out (git's built-in default branch is master, which would
	// otherwise leave clones on an unrelated branch and break fast-forward pushes).
	seed := filepath.Join(base, "seed")
	writeFile(t, filepath.Join(seed, "init.txt"), "init")
	runGit(t, [][]string{
		{"git", "init", "--bare", bareDir},
		{"git", "init", seed},
		{"git", "-C", seed, "config", "user.email", "aimd@localhost"},
		{"git", "-C", seed, "config", "user.name", "aimd"},
		{"git", "-C", seed, "add", "."},
		{"git", "-C", seed, "commit", "-m", "init"},
		{"git", "-C", seed, "remote", "add", "origin", bareDir},
		{"git", "-C", seed, "push", "origin", "HEAD:main"},
		{"git", "-C", bareDir, "symbolic-ref", "HEAD", "refs/heads/main"},
		{"git", "clone", bareDir, storeDir},
		{"git", "-C", storeDir, "config", "user.email", "aimd@localhost"},
		{"git", "-C", storeDir, "config", "user.name", "aimd"},
	})

	// Lay out the store contents (overlay + registry) as untracked working-tree
	// files: the store HEAD stays exactly at origin/main's tip, so once the
	// pusher advances the remote the store is strictly BEHIND.
	makeProjectWithRemote(t, projectDir)
	writeFile(t, filepath.Join(storeDir, "repos", "github.com~test~myapp", "CLAUDE.md"), "# overlay\n")
	writeRegistryLastSeen(t, storeDir, "github.com~test~myapp", "myapp", projectDir, time.Minute)
	symlinkOverlay(t, storeDir, "github.com~test~myapp", projectDir, "CLAUDE.md")

	// Advance origin/main from another clone so the store falls behind.
	pusher := filepath.Join(base, "pusher")
	runGit(t, [][]string{{"git", "clone", bareDir, pusher}})
	writeFile(t, filepath.Join(pusher, "remote.txt"), "remote")
	runGit(t, [][]string{
		{"git", "-C", pusher, "config", "user.email", "aimd@localhost"},
		{"git", "-C", pusher, "config", "user.name", "aimd"},
		{"git", "-C", pusher, "add", "."},
		{"git", "-C", pusher, "commit", "-m", "remote change"},
		{"git", "-C", pusher, "push", "origin", "HEAD:main"},
	})

	chdir(t, projectDir)

	var out bytes.Buffer
	if err := cmd.RunStatus(storeDir, "this-machine", false, true, &out); err != nil {
		t.Fatalf("RunStatus --fetch: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "remote has new changes") {
		t.Errorf("expected behind-store header after --fetch, got:\n%s", got)
	}
	// --fetch path must not print the offline `--fetch` suggestion.
	if strings.Contains(got, "status --fetch") {
		t.Errorf("fetch path should not suggest --fetch again, got:\n%s", got)
	}
}

func TestRunStatus_NotInProject_NoAll(t *testing.T) {
	// Not parallel — uses os.Chdir.
	base := t.TempDir()
	nonRepo := filepath.Join(base, "plain")
	storeDir := filepath.Join(base, "store")
	for _, d := range []string{nonRepo, storeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	makeStatusStore(t, storeDir, nil)

	chdir(t, nonRepo)

	err := cmd.RunStatus(storeDir, "this-machine", false, false, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error outside a tracked project without --all")
	}
	if !strings.Contains(err.Error(), "--all") {
		t.Errorf("error should suggest --all, got: %v", err)
	}
}

func TestRunStatus_StoreNotInitialized(t *testing.T) {
	// Not parallel — uses os.Chdir.
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	storeDir := filepath.Join(base, "missing-store")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeProjectRepo(t, projectDir)

	chdir(t, projectDir)

	err := cmd.RunStatus(storeDir, "this-machine", false, false, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error when store missing")
	}
	if !strings.Contains(err.Error(), "aimd init") {
		t.Errorf("error should mention 'aimd init', got: %v", err)
	}
}

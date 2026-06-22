package cmd_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/cmd"
	"github.com/CyberSecAuto-Labs/aimd/internal/config"
)

// errReader is an io.Reader that always returns the given error.
type errReader struct{ err error }

func (r errReader) Read(_ []byte) (int, error) { return 0, r.err }

// makeBarRepo creates a bare git repository at dir/remote and returns its path.
func makeBarRepo(t *testing.T, dir string) string {
	t.Helper()
	remoteDir := filepath.Join(dir, "remote")
	out, err := exec.Command("git", "init", "--bare", remoteDir).CombinedOutput()
	if err != nil {
		t.Fatalf("git init --bare failed: %v\n%s", err, out)
	}
	return remoteDir
}

func TestRunInit_NewStore(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	remoteDir := makeBarRepo(t, baseDir)
	storeDir := filepath.Join(baseDir, "store")
	cfgPath := filepath.Join(baseDir, "aimd-config")

	var out bytes.Buffer
	in := strings.NewReader("") // URL provided directly — no prompt needed.

	err := cmd.RunInit(remoteDir, storeDir, "test-machine", cfgPath, false, in, &out)
	if err != nil {
		t.Fatalf("RunInit() error = %v\noutput: %s", err, out.String())
	}

	// Verify store directory was created.
	if _, err := os.Stat(storeDir); err != nil {
		t.Errorf("store directory not created: %v", err)
	}

	// Verify registry.json was scaffolded.
	registryPath := filepath.Join(storeDir, ".aimd", "registry.json")
	data, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("registry.json not found: %v", err)
	}
	var reg map[string]any
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatalf("registry.json is not valid JSON: %v", err)
	}
	version, ok := reg["version"]
	if !ok {
		t.Error("registry.json missing 'version' field")
	}
	if version != float64(1) {
		t.Errorf("registry.json version = %v, want 1", version)
	}

	// Verify config was written.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if cfg.Remote != remoteDir {
		t.Errorf("config.Remote = %q, want %q", cfg.Remote, remoteDir)
	}
	if cfg.MachineName != "test-machine" {
		t.Errorf("config.MachineName = %q, want %q", cfg.MachineName, "test-machine")
	}
	if cfg.LinkMode != "symlink" {
		t.Errorf("config.LinkMode = %q, want %q", cfg.LinkMode, "symlink")
	}

	// Verify success message in output.
	output := out.String()
	if !strings.Contains(output, "✓ aimd store initialised") {
		t.Errorf("output does not contain success message:\n%s", output)
	}
	// A brand-new empty store points the user at tracking, not restore.
	if !strings.Contains(output, "aimd track") {
		t.Errorf("output does not contain the next-step track hint:\n%s", output)
	}
}

func TestRunInit_AlreadyInitialisedSameURL(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	remoteDir := makeBarRepo(t, baseDir)
	storeDir := filepath.Join(baseDir, "store")
	cfgPath := filepath.Join(baseDir, "aimd-config")

	// Pre-write a config matching the remote URL.
	existingCfg := &config.Config{
		Remote:      remoteDir,
		MachineName: "old-machine",
		LinkMode:    "symlink",
	}
	if err := config.Save(cfgPath, existingCfg); err != nil {
		t.Fatalf("pre-writing config: %v", err)
	}

	var out bytes.Buffer
	in := strings.NewReader("")

	err := cmd.RunInit(remoteDir, storeDir, "test-machine", cfgPath, false, in, &out)
	if err != nil {
		t.Fatalf("RunInit() error = %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "store already initialised at") {
		t.Errorf("expected 'already initialised' message, got:\n%s", output)
	}
}

func TestRunInit_AlreadyInitialisedDifferentURL_Yes(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	remoteDir := makeBarRepo(t, baseDir)
	storeDir := filepath.Join(baseDir, "store")
	cfgPath := filepath.Join(baseDir, "aimd-config")

	// Pre-write config with a different remote.
	existingCfg := &config.Config{
		Remote:      "git@github.com:old/store.git",
		MachineName: "old-machine",
		LinkMode:    "symlink",
	}
	if err := config.Save(cfgPath, existingCfg); err != nil {
		t.Fatalf("pre-writing config: %v", err)
	}

	var out bytes.Buffer
	in := strings.NewReader("")

	// --yes should skip the overwrite prompt.
	err := cmd.RunInit(remoteDir, storeDir, "test-machine", cfgPath, true, in, &out)
	if err != nil {
		t.Fatalf("RunInit() error = %v\noutput: %s", err, out.String())
	}

	// Config should be updated to the new remote.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading updated config: %v", err)
	}
	if cfg.Remote != remoteDir {
		t.Errorf("config.Remote = %q, want %q", cfg.Remote, remoteDir)
	}
}

func TestRunInit_PromptForURL(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	remoteDir := makeBarRepo(t, baseDir)
	storeDir := filepath.Join(baseDir, "store")
	cfgPath := filepath.Join(baseDir, "aimd-config")

	// Provide the URL via stdin (simulating interactive input).
	var out bytes.Buffer
	in := strings.NewReader(remoteDir + "\n")

	// Pass empty URL to trigger the prompt.
	err := cmd.RunInit("", storeDir, "test-machine", cfgPath, false, in, &out)
	if err != nil {
		t.Fatalf("RunInit() error = %v\noutput: %s", err, out.String())
	}

	// Verify config was written with the prompted URL.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if cfg.Remote != remoteDir {
		t.Errorf("config.Remote = %q, want %q", cfg.Remote, remoteDir)
	}
}

func TestRunInit_EmptyURLReturnsError(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	storeDir := filepath.Join(baseDir, "store")
	cfgPath := filepath.Join(baseDir, "aimd-config")

	var out bytes.Buffer
	// Provide empty input for the URL prompt.
	in := strings.NewReader("\n")

	err := cmd.RunInit("", storeDir, "test-machine", cfgPath, false, in, &out)
	if err == nil {
		t.Fatal("RunInit() expected error for empty URL, got nil")
	}
	if !strings.Contains(err.Error(), "store URL is required") {
		t.Errorf("error = %q, want it to contain 'store URL is required'", err.Error())
	}
}

func TestRunInit_ReposDirAndMetadataDirCreated(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	remoteDir := makeBarRepo(t, baseDir)
	storeDir := filepath.Join(baseDir, "store")
	cfgPath := filepath.Join(baseDir, "aimd-config")

	var out bytes.Buffer
	in := strings.NewReader("")

	if err := cmd.RunInit(remoteDir, storeDir, "machine", cfgPath, false, in, &out); err != nil {
		t.Fatalf("RunInit() error = %v", err)
	}

	for _, dir := range []string{
		filepath.Join(storeDir, "repos"),
		filepath.Join(storeDir, "metadata"),
	} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("expected directory %s to exist: %v", dir, err)
		}
	}
}

func TestRunInit_AlreadyInitialisedDifferentURL_Decline(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	storeDir := filepath.Join(baseDir, "store")
	cfgPath := filepath.Join(baseDir, "aimd-config")

	// Pre-write config with a different remote.
	existingCfg := &config.Config{
		Remote:      "git@github.com:old/store.git",
		MachineName: "old-machine",
		LinkMode:    "symlink",
	}
	if err := config.Save(cfgPath, existingCfg); err != nil {
		t.Fatalf("pre-writing config: %v", err)
	}

	var out bytes.Buffer
	// User inputs "n" to decline overwrite.
	in := strings.NewReader("n\n")

	err := cmd.RunInit("git@github.com:new/store.git", storeDir, "test-machine", cfgPath, false, in, &out)
	if err != nil {
		t.Fatalf("RunInit() error = %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "Aborted.") {
		t.Errorf("expected 'Aborted.' in output, got:\n%s", output)
	}

	// Config should remain unchanged.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	if cfg.Remote != "git@github.com:old/store.git" {
		t.Errorf("config.Remote changed unexpectedly to %q", cfg.Remote)
	}
}

func TestRunInit_StdinReadError(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	storeDir := filepath.Join(baseDir, "store")
	cfgPath := filepath.Join(baseDir, "config")

	var out bytes.Buffer
	// errReader returns a non-EOF error, triggering the "reading store URL" error path.
	in := errReader{err: errors.New("stdin broken")}

	err := cmd.RunInit("", storeDir, "machine", cfgPath, false, in, &out)
	if err == nil {
		t.Fatal("RunInit() expected error when stdin read fails, got nil")
	}
	if !strings.Contains(err.Error(), "reading store URL") {
		t.Errorf("error = %q, want it to contain 'reading store URL'", err.Error())
	}
}

func TestRunInit_ConfirmReadError(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	storeDir := filepath.Join(baseDir, "store")
	cfgPath := filepath.Join(baseDir, "config")

	// Pre-write config with a different remote so the confirmation prompt is shown.
	existingCfg := &config.Config{
		Remote:      "git@github.com:old/store.git",
		MachineName: "old-machine",
		LinkMode:    "symlink",
	}
	if err := config.Save(cfgPath, existingCfg); err != nil {
		t.Fatalf("pre-writing config: %v", err)
	}

	var out bytes.Buffer
	in := errReader{err: errors.New("stdin broken")}

	err := cmd.RunInit("git@github.com:new/store.git", storeDir, "machine", cfgPath, false, in, &out)
	if err == nil {
		t.Fatal("RunInit() expected error when confirmation read fails, got nil")
	}
	if !strings.Contains(err.Error(), "reading confirmation") {
		t.Errorf("error = %q, want it to contain 'reading confirmation'", err.Error())
	}
}

func TestRunInit_CloneOrInitFails(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	// Place a file at storeDir so git clone AND git init both fail.
	storeDir := filepath.Join(baseDir, "store")
	if err := os.WriteFile(storeDir, []byte("block"), 0o600); err != nil {
		t.Fatalf("creating blocking file: %v", err)
	}
	cfgPath := filepath.Join(baseDir, "config")

	var out bytes.Buffer
	in := strings.NewReader("")

	err := cmd.RunInit("/nonexistent/invalid/url", storeDir, "machine", cfgPath, false, in, &out)
	if err == nil {
		t.Fatal("RunInit() expected error when store dir is a file, got nil")
	}
}

func TestRunInit_ExistingGitStoreRecovery(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	remoteDir := makeBarRepo(t, baseDir)
	storeDir := filepath.Join(baseDir, "store")
	cfgPath := filepath.Join(baseDir, "aimd-config")

	// Pre-create storeDir as a git repo with no remote — simulates a
	// corrupted store where .git/config lost its origin entry.
	if out, err := exec.Command("git", "init", "-b", "main", storeDir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", storeDir, "config", "user.email", "aimd-bot@cybersecauto-labs.org").CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", storeDir, "config", "user.name", "aimd-bot").CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}

	var out bytes.Buffer
	in := strings.NewReader("")

	if err := cmd.RunInit(remoteDir, storeDir, "test-machine", cfgPath, false, in, &out); err != nil {
		t.Fatalf("RunInit() error = %v\noutput: %s", err, out.String())
	}

	// Verify origin remote was added.
	gotURL, err := exec.Command("git", "-C", storeDir, "remote", "get-url", "origin").CombinedOutput()
	if err != nil {
		t.Fatalf("git remote get-url origin: %v", err)
	}
	if strings.TrimSpace(string(gotURL)) != remoteDir {
		t.Errorf("origin URL = %q, want %q", strings.TrimSpace(string(gotURL)), remoteDir)
	}

	// Verify registry.json was scaffolded.
	if _, err := os.Stat(filepath.Join(storeDir, ".aimd", "registry.json")); err != nil {
		t.Errorf("registry.json not found after recovery: %v", err)
	}
}

func TestRunInit_LocalInit_FallbackOnCloneFailure(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	// Use an invalid URL that will cause git clone to fail.
	invalidURL := "/nonexistent/path/that/does/not/exist"
	storeDir := filepath.Join(baseDir, "store")
	cfgPath := filepath.Join(baseDir, "aimd-config")

	var out bytes.Buffer
	in := strings.NewReader("")

	// RunInit should succeed: git clone fails, falls back to git init + remote add.
	err := cmd.RunInit(invalidURL, storeDir, "test-machine", cfgPath, false, in, &out)
	if err != nil {
		t.Fatalf("RunInit() error = %v\noutput: %s", err, out.String())
	}

	// Verify the store was initialised locally.
	if _, err := os.Stat(filepath.Join(storeDir, ".git")); err != nil {
		t.Errorf("expected .git directory in store: %v", err)
	}

	// Verify registry.json was scaffolded.
	registryPath := filepath.Join(storeDir, ".aimd", "registry.json")
	if _, err := os.ReadFile(registryPath); err != nil {
		t.Errorf("registry.json not found after local init: %v", err)
	}

	// Verify config was written.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if cfg.Remote != invalidURL {
		t.Errorf("config.Remote = %q, want %q", cfg.Remote, invalidURL)
	}
}

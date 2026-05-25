package cmd_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/cmd"
	"github.com/CyberSecAuto-Labs/aimd/internal/config"
)

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
	if !strings.Contains(output, "aborted") {
		t.Errorf("expected 'aborted' in output, got:\n%s", output)
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

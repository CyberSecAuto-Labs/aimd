package project_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/internal/project"
)

func TestDetectRoot_InGitRepo(t *testing.T) {
	// The aimd project itself is a git repo — this should succeed.
	// The test binary runs with cwd = internal/project; go up 2 levels to
	// reach the repository root (aimd/).
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(filepath.Join(original, "..", "..")); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	root, err := project.DetectRoot()
	if err != nil {
		t.Fatalf("DetectRoot() error = %v, want nil", err)
	}
	if root == "" {
		t.Error("DetectRoot() returned empty string")
	}
	if !filepath.IsAbs(root) {
		t.Errorf("DetectRoot() returned non-absolute path: %q", root)
	}
}

func TestDetectRoot_NotInGitRepo(t *testing.T) {
	tmp := t.TempDir()
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	_, err = project.DetectRoot()
	if err == nil {
		t.Fatal("DetectRoot() expected error in non-git dir, got nil")
	}
	if !strings.Contains(err.Error(), "git repository") {
		t.Errorf("DetectRoot() error = %q, want message containing 'git repository'", err.Error())
	}
}

func TestFetchRemoteURL_NoRemote(t *testing.T) {
	// Create a bare git repo with no remotes.
	tmp := t.TempDir()
	cmd := exec.Command("git", "init", tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
	// Configure git user for the temp repo to avoid errors
	exec.Command("git", "-C", tmp, "config", "user.email", "test@test.com").Run() //nolint:errcheck
	exec.Command("git", "-C", tmp, "config", "user.name", "Test").Run()           //nolint:errcheck

	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	_, err = project.FetchRemoteURL()
	if err == nil {
		t.Fatal("FetchRemoteURL() expected ErrNoRemote, got nil")
	}
	if !errors.Is(err, project.ErrNoRemote) {
		t.Errorf("FetchRemoteURL() error = %v, want ErrNoRemote", err)
	}
}

func TestDetect_ComposesCorrectly(t *testing.T) {
	// Run in the aimd project itself, which has a remote.
	// The test binary runs with cwd = internal/project; go up 2 levels.
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(filepath.Join(original, "..", "..")); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	info, err := project.Detect()
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if info.Root == "" {
		t.Error("Detect().Root is empty")
	}
	if info.Key == "" {
		t.Error("Detect().Key is empty")
	}
	if !filepath.IsAbs(info.Root) {
		t.Errorf("Detect().Root is not absolute: %q", info.Root)
	}
}

func TestDetect_NoRemoteFallsBackToLocalKey(t *testing.T) {
	// Create a git repo with no remote.
	tmp := t.TempDir()
	cmd := exec.Command("git", "init", tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
	exec.Command("git", "-C", tmp, "config", "user.email", "test@test.com").Run() //nolint:errcheck
	exec.Command("git", "-C", tmp, "config", "user.name", "Test").Run()           //nolint:errcheck

	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	info, err := project.Detect()
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if info.RemoteURL != "" {
		t.Errorf("Detect().RemoteURL = %q, want empty for no-remote repo", info.RemoteURL)
	}
	if !strings.HasPrefix(info.Key, "local~") {
		t.Errorf("Detect().Key = %q, want 'local~...' prefix for no-remote repo", info.Key)
	}
}

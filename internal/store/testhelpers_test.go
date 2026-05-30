package store_test

import (
	"os/exec"
	"testing"
)

// initGitRepo initialises a bare or regular git repo in dir.
// -b main pins the default branch so tests are independent of the runner's
// init.defaultBranch setting (macOS CI defaults to 'master').
func initGitRepo(t *testing.T, dir string, bare bool) {
	t.Helper()
	args := []string{"init", "-b", "main"}
	if bare {
		args = append(args, "--bare")
	}
	args = append(args, dir)
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git init: %v — %s", err, out)
	}
}

// gitRun runs a git command in dir and fatals on error.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v — %s", args, dir, err, out)
	}
	return string(out)
}

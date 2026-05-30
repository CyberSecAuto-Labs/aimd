package store

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Commit stages registry.json, repos/<projectKey>/, and metadata/<projectKey>.json
// then creates a git commit in storeDir with message "<verb>: <project> [<machine> <timestamp>]".
func Commit(storeDir, projectKey, projectRoot, verb, machineName string) error {
	registryRel := filepath.Join(".aimd", "registry.json")
	reposRel := filepath.Join("repos", projectKey)
	metaRel := filepath.Join("metadata", projectKey+".json")

	addOut, err := exec.Command("git", "-C", storeDir, "add",
		registryRel, reposRel, metaRel).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git add: %w — %s", err, strings.TrimSpace(string(addOut)))
	}

	msg := fmt.Sprintf("%s: %s [%s %s]",
		verb, filepath.Base(projectRoot), machineName,
		time.Now().UTC().Format(time.RFC3339))
	commitOut, err := exec.Command("git",
		"-C", storeDir,
		"-c", "user.email=aimd@localhost",
		"-c", "user.name=aimd",
		"commit", "-m", msg,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit: %w — %s", err, strings.TrimSpace(string(commitOut)))
	}

	return nil
}

// Push pushes HEAD to origin/main. Returns an error if the push fails;
// the caller decides whether this is fatal or a warning.
func Push(storeDir string) error {
	out, err := exec.Command("git", "-C", storeDir, "push", "origin", "HEAD:main").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

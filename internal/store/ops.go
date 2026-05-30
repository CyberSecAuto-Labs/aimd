package store

import (
	"fmt"
	"os"
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

// CommitMsg stages repos/<projectKey>/ (modified files only, via -u) and
// commits with a caller-supplied message. Use this when the commit message
// must embed content that Commit's fixed format cannot express (e.g. the
// tracked file list in "sync: app/CLAUDE.md [machine ts]").
func CommitMsg(storeDir, projectKey, msg string) error {
	reposRel := filepath.Join("repos", projectKey)

	// git add -u with a pathspec fails when the path doesn't exist at all in
	// the index. Guard against this: if the directory is absent there is nothing
	// to stage and the subsequent commit will return "nothing to commit".
	if _, statErr := os.Stat(filepath.Join(storeDir, reposRel)); statErr == nil {
		addOut, err := exec.Command("git", "-C", storeDir, "add", "-u", "--", reposRel).CombinedOutput()
		if err != nil {
			return fmt.Errorf("git add -u: %w — %s", err, strings.TrimSpace(string(addOut)))
		}
	}

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

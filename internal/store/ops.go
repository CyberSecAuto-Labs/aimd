package store

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// PushError is returned by Push when the git push fails.
// Transient is true for network failures (retry automatically via pending-push marker).
// Transient is false for hard rejections (non-fast-forward, auth failure) that may need user action.
type PushError struct {
	Transient bool
	Output    string
	Err       error
}

func (e *PushError) Error() string {
	return fmt.Sprintf("%v — %s", e.Err, e.Output)
}

func (e *PushError) Unwrap() error { return e.Err }

// pendingPushMarkerPath returns the path of the pending-push marker file.
func pendingPushMarkerPath(storeDir string) string {
	return filepath.Join(storeDir, ".aimd", "pending-push")
}

// gitCmd builds an *exec.Cmd for git that forces an English/C locale so its
// output is deterministic. All store git invocations route through this helper
// so that callers (and substring classifiers like isPushHard) can rely on
// English output regardless of the user's git locale.
func gitCmd(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	return cmd
}

// isPushHard returns true when the git output indicates a hard rejection
// (non-transient) rather than a transient network failure.
func isPushHard(output string) bool {
	hard := []string{
		"rejected",
		"[rejected]",
		"non-fast-forward",
		"denied",
		"403",
		"401",
		"Permission denied",
		"error: failed to push",
	}
	lower := strings.ToLower(output)
	for _, token := range hard {
		if strings.Contains(lower, strings.ToLower(token)) {
			return true
		}
	}
	return false
}

// verbLabel returns a past-tense label for known verbs or title-cases the verb.
func verbLabel(verb string) string {
	labels := map[string]string{
		"track":   "Tracked",
		"untrack": "Untracked",
		"restore": "Restored",
		"sync":    "Synced",
		"remove":  "Removed",
	}
	if l, ok := labels[verb]; ok {
		return l
	}
	if len(verb) == 0 {
		return verb
	}
	return strings.ToUpper(verb[:1]) + verb[1:]
}

// Commit stages registry.json, repos/<projectKey>/, and metadata/<projectKey>.json
// then creates a git commit in storeDir with message "<verb>: <project> [<machine> <timestamp>]".
// When files is non-empty a body is appended listing the affected files.
func Commit(storeDir, projectKey, projectRoot, verb, machineName string, files []string) error {
	registryRel := filepath.Join(".aimd", "registry.json")
	reposRel := filepath.Join("repos", projectKey)
	metaRel := filepath.Join("metadata", projectKey+".json")

	addOut, err := gitCmd("-C", storeDir, "add",
		registryRel, reposRel, metaRel).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git add: %w — %s", err, strings.TrimSpace(string(addOut)))
	}

	title := fmt.Sprintf("%s: %s [%s %s]",
		verb, filepath.Base(projectRoot), machineName,
		time.Now().UTC().Format(time.RFC3339))

	msg := buildCommitMessage(title, verb, projectKey, machineName, files)

	commitOut, err := gitCmd(
		"-C", storeDir,
		"-c", "user.email=aimd@localhost",
		"-c", "user.name=aimd",
		"-c", "commit.gpgsign=false",
		"commit", "-m", msg,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit: %w — %s", err, strings.TrimSpace(string(commitOut)))
	}

	return nil
}

// buildCommitMessage assembles a store commit message: the human-readable title,
// an optional body listing the affected files, and the machine-readable trailer
// block. `aimd log` sources its structured fields (verb, project, machine, files)
// from the trailers, never by parsing the human subject/body — so the trailer
// block must always be the final paragraph for git's trailer parser. This is the
// single definition of that format, shared by Commit and RemoveProject.
func buildCommitMessage(title, verb, projectKey, machineName string, files []string) string {
	var sb strings.Builder
	sb.WriteString(title)

	// Human-readable body: the affected files, kept for anyone reading the store
	// with `git log` directly.
	if len(files) > 0 {
		sb.WriteString("\n\n")
		sb.WriteString(verbLabel(verb))
		sb.WriteString(" files:\n")
		for _, f := range files {
			sb.WriteString("  ")
			sb.WriteString(f)
			sb.WriteString("\n")
		}
		// One more newline to leave a blank line before the trailer block.
		sb.WriteString("\n")
	} else {
		// Blank line separating the subject from the trailer block.
		sb.WriteString("\n\n")
	}

	sb.WriteString("Aimd-Verb: ")
	sb.WriteString(verb)
	sb.WriteString("\nAimd-Project: ")
	sb.WriteString(projectKey)
	sb.WriteString("\nAimd-Machine: ")
	sb.WriteString(machineName)
	sb.WriteString("\n")
	for _, f := range files {
		sb.WriteString("Aimd-File: ")
		sb.WriteString(f)
		sb.WriteString("\n")
	}

	return sb.String()
}

// RemoveProject stages the deletion of a project's overlay tree and metadata
// file from the store, stages the (already-saved) registry change, and commits
// with a "remove" verb + Aimd-* trailers. The caller saves registry.json and
// pushes separately.
//
// --ignore-unmatch lets this succeed even for a project that was never pushed
// (no overlay or metadata exists in the store yet) — the registry change still
// gets committed. remove affects no individual files, so no Aimd-File trailers
// are emitted.
func RemoveProject(storeDir, projectKey, displayName, machineName string) error {
	reposRel := filepath.Join("repos", projectKey)
	metaRel := filepath.Join("metadata", projectKey+".json")

	rmOut, err := gitCmd("-C", storeDir, "rm", "-r", "--ignore-unmatch", "--quiet",
		"--", reposRel, metaRel).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git rm: %w — %s", err, strings.TrimSpace(string(rmOut)))
	}

	registryRel := filepath.Join(".aimd", "registry.json")
	addOut, err := gitCmd("-C", storeDir, "add", registryRel).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git add: %w — %s", err, strings.TrimSpace(string(addOut)))
	}

	title := fmt.Sprintf("remove: %s [%s %s]",
		displayName, machineName, time.Now().UTC().Format(time.RFC3339))
	msg := buildCommitMessage(title, "remove", projectKey, machineName, nil)

	commitOut, err := gitCmd(
		"-C", storeDir,
		"-c", "user.email=aimd@localhost",
		"-c", "user.name=aimd",
		"-c", "commit.gpgsign=false",
		"commit", "-m", msg,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit: %w — %s", err, strings.TrimSpace(string(commitOut)))
	}

	return nil
}

// OverlayProjectDirty reports whether the project's overlay directory has uncommitted
// changes in the store worktree. A non-existent overlay path yields an empty
// status (git treats a non-matching pathspec as no changes, exit 0).
func OverlayProjectDirty(storeDir, projectKey string) (bool, error) {
	reposRel := filepath.Join("repos", projectKey)
	out, err := gitCmd("-C", storeDir, "status", "--porcelain", "--", reposRel).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git status: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// OverlayFileDirty reports whether a single tracked overlay file has uncommitted
// changes in the store worktree. relPath is the project-relative path of the
// tracked file (e.g. "CLAUDE.md"); it is scoped to repos/<projectKey>/<relPath>
// so the answer reflects that file alone, not the whole project overlay.
func OverlayFileDirty(storeDir, projectKey, relPath string) (bool, error) {
	reposRel := filepath.Join("repos", projectKey, relPath)
	out, err := gitCmd("-C", storeDir, "status", "--porcelain", "--", reposRel).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git status: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// Push pushes HEAD to origin/main.
// On failure it writes a pending-push marker and returns *PushError so callers
// can distinguish transient network failures from hard rejections.
// On success it removes any existing pending-push marker.
func Push(storeDir string) error {
	markerPath := pendingPushMarkerPath(storeDir)

	out, err := gitCmd("-C", storeDir, "push", "origin", "HEAD:main").CombinedOutput()
	outStr := strings.TrimSpace(string(out))

	if err != nil {
		// Write / update the pending-push marker.
		timestamp := time.Now().UTC().Format(time.RFC3339) + "\n"
		_ = os.MkdirAll(filepath.Dir(markerPath), 0o755)
		_ = os.WriteFile(markerPath, []byte(timestamp), 0o600)

		return &PushError{
			Transient: !isPushHard(outStr),
			Output:    outStr,
			Err:       err,
		}
	}

	// Success: remove marker if it exists.
	if _, statErr := os.Stat(markerPath); !errors.Is(statErr, os.ErrNotExist) {
		_ = os.Remove(markerPath)
	}

	return nil
}

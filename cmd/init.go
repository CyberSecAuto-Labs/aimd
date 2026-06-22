package cmd

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CyberSecAuto-Labs/aimd/internal/config"
	"github.com/CyberSecAuto-Labs/aimd/internal/output"
	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
	"github.com/CyberSecAuto-Labs/aimd/internal/store"
)

var initYes bool

var initCmd = &cobra.Command{
	Use:     "init [<store-url>]",
	GroupID: "setup",
	Short:   "Initialise the aimd store",
	Long: `Clone an existing aimd store or create a new one at ~/.aimd/store/.

If <store-url> is not provided, you will be prompted to enter a Git remote URL.

For a new (empty) remote, aimd will initialise a local store, scaffold the
registry, make an initial commit, and push to the remote.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var storeURL string
		if len(args) == 1 {
			storeURL = args[0]
		}
		cfgPath, err := config.DefaultPath()
		if err != nil {
			return fmt.Errorf("determining config path: %w", err)
		}
		return RunInit(storeURL, storePath, machine, cfgPath, initYes, cmd.InOrStdin(), cmd.OutOrStdout())
	},
}

// registryData is the structure written to .aimd/registry.json on first init.
type registryData struct {
	Version  int            `json:"version"`
	Projects map[string]any `json:"projects"`
}

// RunInit is the testable core of the init command.
//
// url may be empty (interactive prompt will be shown).
// storeDir is the resolved path to ~/.aimd/store (or --store override).
// machineName is the machine identifier.
// cfgPath is the path to the aimd config file (typically ~/.aimd/config).
// yes skips all confirmation prompts.
// in/out allow injection of readers and writers in tests.
func RunInit(url, storeDir, machineName, cfgPath string, yes bool, in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)

	// Step 1: determine store URL.
	if url == "" {
		_, _ = fmt.Fprint(out, "Enter store URL (git remote): ")
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("reading store URL: %w", err)
		}
		url = strings.TrimSpace(line)
		if url == "" {
			return errors.New("store URL is required")
		}
	}

	// Step 3: check if already initialised.
	existing, loadErr := config.Load(cfgPath)
	if loadErr != nil && !errors.Is(loadErr, config.ErrNotFound) {
		return fmt.Errorf("loading existing config: %w", loadErr)
	}
	if loadErr == nil {
		// Config exists.
		if existing.Remote == url {
			_, _ = fmt.Fprintf(out, "store already initialised at %s\n", storeDir)
			return nil
		}
		// Different remote — ask for confirmation unless --yes.
		if !yes {
			_, _ = fmt.Fprintf(out, "warning: config already exists with remote %q\nOverwrite with %q? [y/N] ", existing.Remote, url)
			line, readErr := reader.ReadString('\n')
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return fmt.Errorf("reading confirmation: %w", readErr)
			}
			answer := strings.TrimSpace(strings.ToLower(line))
			if answer != "y" && answer != "yes" {
				_, _ = fmt.Fprintln(out, output.Colorize(out, output.Red, "Aborted."))
				return nil
			}
		}
	}

	// Step 5: clone or init the store.
	if err := cloneOrInit(url, storeDir); err != nil {
		return err
	}

	// The store directory now exists, so take the exclusive lock before
	// scaffolding the registry and writing config — this serialises init
	// against any other aimd process touching the same store. (The lock can't
	// be taken before the clone: it would create files in the destination and
	// make `git clone` refuse a non-empty target.)
	release, lockErr := lockStoreExclusive(storeDir)
	if lockErr != nil {
		return lockErr
	}
	defer release()

	// Step 6: scaffold if registry.json is missing.
	registryPath := filepath.Join(storeDir, ".aimd", "registry.json")
	if _, statErr := os.Stat(registryPath); os.IsNotExist(statErr) {
		if scaffoldErr := scaffoldStore(storeDir, registryPath, out); scaffoldErr != nil {
			return scaffoldErr
		}
	}

	// Step 7: write the config file.
	cfg := &config.Config{
		Remote:      url,
		MachineName: machineName,
		LinkMode:    "symlink",
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	// Step 8: success message.
	_, _ = fmt.Fprintf(out, "✓ aimd store initialised\n  remote: %s\n  store:  %s\n  machine: %s\n",
		url, storeDir, machineName)

	// Step 9: point at the obvious next step.
	printInitNextStep(out, registryPath, machineName)
	return nil
}

// printInitNextStep points the user at what to do after init. The right next
// step depends on what the freshly-loaded registry holds for this machine:
//
//   - projects already checked out here → `restore --all` re-links them all at once;
//   - projects tracked only on other machines → this hostname isn't in the registry
//     yet, so `restore --all` would find nothing; the user must cd into each project
//     and run `aimd restore` once to register this machine before --all can see it;
//   - a brand-new (empty) store → start tracking.
func printInitNextStep(out io.Writer, registryPath, machineName string) {
	reg, regErr := registry.LoadOrNew(registryPath)
	if regErr != nil || len(reg.Projects) == 0 {
		_, _ = fmt.Fprintf(out, "\nNext: cd into a project and run `aimd track <file>` to start tracking.\n")
		return
	}
	if local, _ := machineLocalProjects(reg, machineName); len(local) > 0 {
		_, _ = fmt.Fprintf(out, "\nNext: run `aimd restore --all` to materialise your tracked files.\n")
		return
	}
	// The store holds projects, but none checked out on this machine yet.
	_, _ = fmt.Fprintf(out, "\nNext: cd into each of your projects and run `aimd restore` to re-link its tracked files.\n")
}

// cloneOrInit sets up the store at storeDir from the given remote URL.
// If storeDir already contains a git repo, it ensures the remote URL is
// correct without re-cloning — this handles re-runs and store recovery.
// Otherwise it tries git clone; if the remote is empty/unreachable it falls
// back to git init + remote add.
func cloneOrInit(url, storeDir string) error {
	// Existing git repo: skip clone, just fix the remote if needed.
	if _, err := os.Stat(filepath.Join(storeDir, ".git")); err == nil {
		return ensureRemote(url, storeDir)
	}

	cloneOut, err := exec.Command("git", "clone", url, storeDir).CombinedOutput()
	if err == nil {
		return nil
	}
	// Clone failed — assume empty/new remote.
	if initErr := gitLocalInit(url, storeDir); initErr != nil {
		return fmt.Errorf("clone failed (%s) and local init also failed: %w",
			strings.TrimSpace(string(cloneOut)), initErr)
	}
	return nil
}

// ensureRemote makes sure the git repo at storeDir has origin pointing to url.
// If origin already points to url it is a no-op. If origin exists with a
// different URL it is updated. If no origin exists one is added.
func ensureRemote(url, storeDir string) error {
	out, err := exec.Command("git", "-C", storeDir, "remote", "get-url", "origin").CombinedOutput()
	if err == nil {
		if strings.TrimSpace(string(out)) == url {
			return nil
		}
		if out, err := exec.Command("git", "-C", storeDir, "remote", "set-url", "origin", url).CombinedOutput(); err != nil {
			return fmt.Errorf("git remote set-url: %w — %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if out, err := exec.Command("git", "-C", storeDir, "remote", "add", "origin", url).CombinedOutput(); err != nil {
		return fmt.Errorf("git remote add origin: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// gitLocalInit runs `git init` in storeDir and adds url as the origin remote.
func gitLocalInit(url, storeDir string) error {
	if out, err := exec.Command("git", "init", "-b", "main", storeDir).CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %w — %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("git", "-C", storeDir, "remote", "add", "origin", url).CombinedOutput(); err != nil {
		return fmt.Errorf("git remote add origin: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// scaffoldStore creates the store directory layout and makes the initial commit.
func scaffoldStore(storeDir, registryPath string, out io.Writer) error {
	// Create required directories.
	for _, dir := range []string{
		filepath.Join(storeDir, ".aimd"),
		filepath.Join(storeDir, "repos"),
		filepath.Join(storeDir, "metadata"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("creating store directory %s: %w", dir, err)
		}
	}

	// Write registry.json.
	registry := registryData{
		Version:  1,
		Projects: map[string]any{},
	}
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding registry: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(registryPath, data, 0o600); err != nil {
		return fmt.Errorf("writing registry.json: %w", err)
	}

	// Stage all files.
	if gitOut, err := exec.Command("git", "-C", storeDir, "add", ".").CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %w — %s", err, strings.TrimSpace(string(gitOut)))
	}

	// Initial commit.
	commitArgs := append([]string{"-C", storeDir}, store.CommitIdentityArgs...)
	commitArgs = append(commitArgs, "commit", "-m", "init: scaffold aimd store")
	commitCmd := exec.Command("git", commitArgs...)
	if gitOut, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w — %s", err, strings.TrimSpace(string(gitOut)))
	}

	// Push to remote — non-fatal if it fails (offline scenario).
	pushOut, err := exec.Command("git", "-C", storeDir, "push", "origin", "HEAD:main").CombinedOutput()
	if err != nil {
		_, _ = fmt.Fprintf(out, "warning: could not push to remote (offline or no upstream branch set). Run `git push` manually.\n  (%s)\n",
			strings.TrimSpace(string(pushOut)))
	}

	return nil
}

func init() {
	initCmd.Flags().BoolVarP(&initYes, "yes", "y", false, "Skip all confirmation prompts")
	rootCmd.AddCommand(initCmd)
}

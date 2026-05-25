// Package cmd implements the aimd command-line interface.
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	storePath string
	machine   string
	dryRun    bool
	verbose   bool
)

var rootCmd = &cobra.Command{
	Use:   "aimd",
	Short: "Private AI .md files for Git repositories",
	Long: `aimd lets developers track AI context files (such as CLAUDE.md) in a
private Git store and sync them across machines — without committing those
files to the project repository.

It uses .git/info/exclude to hide tracked files from git status and
symlinks to make them available in the project directory.`,
	PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
		if machine == "" {
			h, err := os.Hostname()
			if err != nil {
				return fmt.Errorf("--machine not set and hostname detection failed: %w", err)
			}
			machine = h
		}
		if storePath == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("--store not set and home directory detection failed: %w", err)
			}
			storePath = filepath.Join(home, ".aimd", "store")
		}
		return nil
	},
}

// SetBuildInfo wires GoReleaser-injected build metadata into the root command.
// Call this from main before Execute.
func SetBuildInfo(version, commit, date string) {
	rootCmd.Version = version
	rootCmd.SetVersionTemplate(fmt.Sprintf("aimd %s (commit %s, built %s)\n", version, commit, date))
}

// Execute runs the root command and exits on error.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&storePath, "store", "", "Path to the aimd store (default: ~/.aimd/store)")
	rootCmd.PersistentFlags().StringVar(&machine, "machine", "", "Machine name override (default: system hostname)")
	rootCmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "Show what would happen without making changes")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")
}

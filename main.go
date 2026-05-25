// Command aimd is the entry point for the aimd CLI tool.
package main

import "github.com/CyberSecAuto-Labs/aimd/cmd"

// Version information injected at build time by GoReleaser via ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cmd.SetBuildInfo(version, commit, date)
	cmd.Execute()
}

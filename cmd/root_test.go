package cmd_test

import (
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/cmd"
)

func TestSetBuildInfo(t *testing.T) {
	t.Parallel()
	// SetBuildInfo is called from main before Execute to inject GoReleaser metadata.
	// Exercise the function to confirm it runs without panic.
	cmd.SetBuildInfo("v0.0.0-test", "abc1234", "2026-01-01")
}

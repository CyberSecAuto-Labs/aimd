package cmd_test

import (
	"os"
	"testing"
)

// TestMain isolates the suite from the developer's / CI runner's global and
// system Git configuration. Most importantly it neutralises a global
// commit.gpgsign=true, which would make the raw setup commits in these tests
// fail on machines that sign by default (the product code already disables
// signing for its own commits). Tests that need specific global config — e.g.
// the dedicated signing test — override GIT_CONFIG_GLOBAL per-test with
// t.Setenv, which takes precedence over the value set here.
func TestMain(m *testing.M) {
	_ = os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	_ = os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Exit(m.Run())
}

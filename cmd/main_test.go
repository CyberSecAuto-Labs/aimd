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
//
// It also disables git's background gc/maintenance via the GIT_CONFIG_* env
// injection (applied to every git subprocess, regardless of GIT_CONFIG_GLOBAL).
// Without this, a `git commit` can detach a maintenance process that keeps
// writing into the repo's .git/ while t.TempDir cleanup runs, intermittently
// failing teardown with "directory not empty".
func TestMain(m *testing.M) {
	_ = os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	_ = os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	_ = os.Setenv("GIT_CONFIG_COUNT", "2")
	_ = os.Setenv("GIT_CONFIG_KEY_0", "gc.auto")
	_ = os.Setenv("GIT_CONFIG_VALUE_0", "0")
	_ = os.Setenv("GIT_CONFIG_KEY_1", "maintenance.auto")
	_ = os.Setenv("GIT_CONFIG_VALUE_1", "false")
	os.Exit(m.Run())
}

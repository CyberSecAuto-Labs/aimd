package store_test

import (
	"os"
	"testing"
)

// TestMain isolates the suite from the developer's / CI runner's global and
// system Git configuration. Most importantly it neutralises a global
// commit.gpgsign=true, which would make the raw setup commits in these tests
// fail on machines that sign by default (the product code already disables
// signing for its own commits). TestCommitSucceedsWithGlobalGPGSigningEnabled
// deliberately sets its own GIT_CONFIG_GLOBAL via t.Setenv, which takes
// precedence over the value set here for the duration of that test.
func TestMain(m *testing.M) {
	_ = os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	_ = os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Exit(m.Run())
}

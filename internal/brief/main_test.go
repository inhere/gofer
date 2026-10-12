package brief

import (
	"os"
	"testing"
)

// TestMain turns off git's automatic maintenance for every git these tests run.
// Newer git (2.47+) detaches `git maintenance run --auto` after a commit; the
// background process can still be writing under .git when the test's TempDir is
// removed ("directory not empty", seen on a CI runner). GIT_CONFIG_* applies to the
// git child processes only.
func TestMain(m *testing.M) {
	for k, v := range map[string]string{
		"GIT_CONFIG_COUNT":   "2",
		"GIT_CONFIG_KEY_0":   "maintenance.auto",
		"GIT_CONFIG_VALUE_0": "false",
		"GIT_CONFIG_KEY_1":   "gc.auto",
		"GIT_CONFIG_VALUE_1": "0",
	} {
		_ = os.Setenv(k, v)
	}
	os.Exit(m.Run())
}

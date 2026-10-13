// Package gitenv quiets git for tests that create repositories in a TempDir.
package gitenv

import "os"

// DisableAutoMaintenance turns off git's automatic maintenance and gc for every git
// child process of the calling test binary. Call it from TestMain.
//
// Newer git (2.47+) detaches `git maintenance run --auto` after commands such as
// commit; the background process can still be writing under .git when the test's
// TempDir is removed, failing the test with "directory not empty". GIT_CONFIG_*
// only reaches git processes, so nothing else in the test changes.
func DisableAutoMaintenance() {
	for k, v := range map[string]string{
		"GIT_CONFIG_COUNT":   "2",
		"GIT_CONFIG_KEY_0":   "maintenance.auto",
		"GIT_CONFIG_VALUE_0": "false",
		"GIT_CONFIG_KEY_1":   "gc.auto",
		"GIT_CONFIG_VALUE_1": "0",
	} {
		_ = os.Setenv(k, v)
	}
}

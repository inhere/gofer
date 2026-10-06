//go:build windows

package bdmigrate

// lockHeld cannot probe advisory locks on Windows; the recent-write and lease
// checks still apply.
func lockHeld(string) bool { return false }

//go:build !linux

package bdmigrate

// findBdProcesses is Linux-only (it reads /proc); other platforms rely on the
// lock, recent-write and lease checks.
func findBdProcesses(string) []string { return nil }

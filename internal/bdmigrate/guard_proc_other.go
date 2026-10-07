//go:build !linux

package bdmigrate

import "time"

// findBdProcesses is Linux-only (it reads /proc); other platforms rely on the
// lock, recent-write and lease checks.
func findBdProcesses(string) []string { return nil }

// settleChildren is a no-op where process scanning is unavailable.
func settleChildren(string, time.Duration) {}

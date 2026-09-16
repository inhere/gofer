//go:build !windows && !darwin && !linux

package hookrelay

// probeSystemIdle always reports unknown: no desktop idle source is wired for
// this platform, so sessions keep the explicit relay switch as their only gate.
func probeSystemIdle() int64 { return -1 }

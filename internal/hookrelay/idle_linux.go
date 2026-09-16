//go:build linux

package hookrelay

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
)

// probeSystemIdle shells out to xprintidle, the X11 tool that prints
// milliseconds since the last input event. Wayland and headless sessions have
// no such tool (and no portable X11-independent source), so auto-arming simply
// stays unavailable there — the explicit relay switch is unaffected.
func probeSystemIdle() int64 {
	path, err := exec.LookPath("xprintidle")
	if err != nil {
		return -1
	}
	ctx, cancel := context.WithTimeout(context.Background(), idleProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path).Output()
	if err != nil {
		return -1
	}
	ms, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || ms < 0 {
		return -1
	}
	return ms / 1000
}

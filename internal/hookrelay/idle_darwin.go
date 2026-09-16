//go:build darwin

package hookrelay

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
)

// hidIdleRe matches the IOHIDSystem property `"HIDIdleTime" = 1234567890`
// (nanoseconds since the last HID event) in `ioreg -c IOHIDSystem` output.
var hidIdleRe = regexp.MustCompile(`"HIDIdleTime"\s*=\s*(\d+)`)

// probeSystemIdle parses HIDIdleTime out of ioreg. A session without a GUI
// (ssh, launchd daemon, CI) has no IOHIDSystem node, so the probe reports
// unknown instead of guessing.
func probeSystemIdle() int64 {
	ctx, cancel := context.WithTimeout(context.Background(), idleProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ioreg", "-c", "IOHIDSystem").Output()
	if err != nil {
		return -1
	}
	m := hidIdleRe.FindSubmatch(out)
	if m == nil {
		return -1
	}
	ns, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil || ns < 0 {
		return -1
	}
	return ns / int64(1e9)
}

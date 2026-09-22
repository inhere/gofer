//go:build windows

package daemon

import (
	"strings"
	"testing"
)

// TestTerminateMissingTargetReportsHint: a pid that is not a gofer started by
// this build (here: an exited child, whose stop event therefore does not exist)
// must FAIL with the actionable hint instead of killing something — the stop
// path never guesses, because a hard kill would be free to shoot a reused pid.
func TestTerminateMissingTargetReportsHint(t *testing.T) {
	pid := deadChildPid(t)
	err := Terminate(pid)
	if err == nil {
		t.Fatalf("Terminate(%d) without a stop event should error, not silently succeed", pid)
	}
	if !strings.Contains(err.Error(), "taskkill") {
		t.Fatalf("error must carry the manual-stop hint (KillHint), got: %v", err)
	}
}

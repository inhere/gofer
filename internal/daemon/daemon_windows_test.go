//go:build windows

package daemon

import (
	"strings"
	"testing"
)

// TestSessionInfoInteractiveIsAnyUserSession pins what W2 measured on an RDP-only
// host: "interactive" must mean "can touch a user desktop", i.e. anything but the
// service session 0 — an RDP session qualifies (there the console session is the
// empty session 1, so the two answers differ). Console is the narrower fact and
// therefore implies Interactive.
func TestSessionInfoInteractiveIsAnyUserSession(t *testing.T) {
	s := SessionInfo()
	if want := s.ID != 0; s.Interactive != want {
		t.Fatalf("SessionInfo() = %+v: Interactive must be %v for session %d (any user session is interactive)",
			s, want, s.ID)
	}
	if s.Console && !s.Interactive {
		t.Fatalf("SessionInfo() = %+v: Console=true must imply Interactive=true", s)
	}
}

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

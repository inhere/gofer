package wsproto

import "testing"

// TestACPMirrorCapability pins the v22 floor of the "acp" log stream (gofer-e2x7):
// a v21 peer must not be sent the stream (an older hub writes any non-stderr log
// frame into stdout) and must be reported as unable to mirror.
func TestACPMirrorCapability(t *testing.T) {
	if ACPMirrorMinProtocolVersion != 22 {
		t.Fatalf("ACPMirrorMinProtocolVersion = %d, want 22", ACPMirrorMinProtocolVersion)
	}
	if SupportsACPMirror(ACPMirrorMinProtocolVersion - 1) {
		t.Fatal("a v21 peer must not speak the acp log stream")
	}
	if !SupportsACPMirror(CurrentProtocolVersion) {
		t.Fatal("this build must speak the acp log stream")
	}
	if LogStreamACP == "stdout" || LogStreamACP == "stderr" {
		t.Fatalf("LogStreamACP = %q collides with a stdio stream", LogStreamACP)
	}
}

package hookrelay

import "time"

// idleProbeTimeout bounds the out-of-process probes (macOS ioreg, Linux
// xprintidle) so a hook never stalls on a wedged helper: detecting idle must
// stay well under 200ms, which is what the relay's "is the human here?" check
// can afford on every Stop.
const idleProbeTimeout = 200 * time.Millisecond

// probeIdle is the platform idle detector (probeSystemIdle, one file per GOOS:
// idle_windows.go / idle_darwin.go / idle_linux.go / idle_other.go). Tests
// replace it — CI and containers have no desktop session to read, and the
// macOS/Linux probes shell out.
var probeIdle = probeSystemIdle

// idleSeconds reports how long the machine has seen no keyboard or mouse input,
// in whole seconds, or -1 when unknown (unsupported platform, helper missing,
// probe failed). Callers must read -1 as "no evidence", never as "the human is
// here": the relay only ever arms on a known, long idle.
func idleSeconds() int64 {
	if probeIdle == nil {
		return -1
	}
	if sec := probeIdle(); sec >= 0 {
		return sec
	}
	return -1
}

// idleSecPtr is idleSeconds in heartbeat form: the reading is always reported
// (a nil field would leave the server's previous value in place, which is what
// events that do not probe want — see client.SessionHeartbeat).
func idleSecPtr() *int64 {
	sec := idleSeconds()
	return &sec
}

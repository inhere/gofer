package hookrelay

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/client"
)

// idleUnknown is the stand-in probe installed for the whole package: the real
// one reads the OS (and shells out on macOS/Linux), which a test machine — let
// alone CI — cannot be asked to have. Tests that need a reading inject their own
// through injectIdleProbe.
func idleUnknown() int64 { return -1 }

// TestMain keeps every test off the real system idle API.
func TestMain(m *testing.M) {
	probeIdle = idleUnknown
	os.Exit(m.Run())
}

// injectIdleProbe replaces the package probe for one test.
func injectIdleProbe(t *testing.T, sec int64) {
	t.Helper()
	probeIdle = func() int64 { return sec }
	t.Cleanup(func() { probeIdle = idleUnknown })
}

func TestIdleSecondsUnknownIsMinusOne(t *testing.T) {
	// No probe at all (unsupported platform / probe disabled) → unknown, never 0:
	// 0 would read as "the human is right here" and defeat the auto-arm.
	probeIdle = nil
	assert.Eq(t, int64(-1), idleSeconds())
	// A failing probe (missing xprintidle, no GUI session, timed-out ioreg):
	// negative readings are unknown too.
	probeIdle = func() int64 { return -1 }
	assert.Eq(t, int64(-1), idleSeconds())
	probeIdle = idleUnknown
	t.Cleanup(func() { probeIdle = idleUnknown })

	// A real reading passes through, including 0 ("just typed").
	injectIdleProbe(t, 0)
	assert.Eq(t, int64(0), idleSeconds())
	injectIdleProbe(t, 612)
	assert.Eq(t, int64(612), idleSeconds())

	// The heartbeat form always reports something: nil would leave the server's
	// previous reading in place, which is the opposite of what a Stop means.
	sec := idleSecPtr()
	assert.True(t, sec != nil)
	assert.Eq(t, int64(612), *sec)
}

// TestStopBlocksWhenAutoArmed verifies the auto-armed path end to end: the
// server armed relay because the human is away (AutoArmed, switch still off),
// so the Stop hook opens a turn and waits like an explicitly relayed session.
func TestStopBlocksWhenAutoArmed(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", RelayMode: client.RelayModeAuto, AutoArmed: true, WaitReason: client.WaitIdleProbe, IdleSec: 600}
	f.answerAfter, f.answer = 1, "carry on"
	injectIdleProbe(t, 600)
	var log strings.Builder
	res, err := Run(f, payload(t, "codex", map[string]any{
		"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": "A or B?",
	}), fastOpts(&log))
	assert.NoErr(t, err)
	assert.True(t, res.Blocked)
	assert.Eq(t, ReplyPrefix+"carry on", res.Reason)
	// The human never came back, so no release was ever requested.
	assert.Eq(t, 0, f.releases)
	assert.True(t, strings.Contains(log.String(), "reason=idle_probe"))

	// The reading travelled with the Stop heartbeat (that is what arms it).
	first := f.beats[0]
	assert.True(t, first.IdleSec != nil)
	assert.Eq(t, int64(600), *first.IdleSec)
}

// TestStopReleasesWhenUserReturns verifies the release path: on an auto-armed
// wait the hook re-probes the keyboard between polls, reports the reading and
// lets the server close the turn — the agent stops normally at its prompt.
func TestStopReleasesWhenUserReturns(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", RelayMode: client.RelayModeAuto, AutoArmed: true, WaitReason: client.WaitIdleProbe, IdleSec: 600}
	f.released = true
	injectIdleProbe(t, 0) // the human is back at the keyboard
	var log strings.Builder
	opts := fastOpts(&log)
	opts.Wait = 30 * time.Second
	res, err := Run(f, payload(t, "codex", map[string]any{
		"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": "waiting",
	}), opts)
	assert.NoErr(t, err)
	assert.False(t, res.Blocked)
	assert.Eq(t, 1, f.releases)
	assert.Eq(t, 1, len(f.idleReports))
	assert.Eq(t, int64(0), f.idleReports[0]) // the zero reading is what released it
	assert.Eq(t, 1, f.waits)                 // released on the first poll boundary
	assert.Eq(t, "idle", f.sessions["s1"].State)
	assert.True(t, strings.Contains(log.String(), "release confirmed"))
}

// TestStopIgnoresIdleWhenRelayExplicit pins the boundary of the release rule: a
// session whose switch the human flipped on keeps waiting even though the
// keyboard shows activity — only the human ends that wait (typing, or /off).
func TestStopIgnoresIdleWhenRelayExplicit(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn, IdleSec: 600}
	f.answerAfter, f.answer = 2, "go on"
	injectIdleProbe(t, 0) // the probe would release an auto-armed wait
	var log strings.Builder
	res, err := Run(f, payload(t, "codex", map[string]any{
		"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": "A or B?",
	}), fastOpts(&log))
	assert.NoErr(t, err)
	assert.True(t, res.Blocked)
	assert.Eq(t, 0, f.releases)
	assert.Eq(t, ReplyPrefix+"go on", res.Reason)
}

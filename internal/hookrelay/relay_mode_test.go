package hookrelay

import (
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/client"
)

// TestStopWaitsOnModeOn pins the explicit switch (R1): with the mode `on` every
// Stop opens a turn and blocks, whatever the keyboard says — and the keyboard
// probe is never used to release it, because only its owner ends that wait.
func TestStopWaitsOnModeOn(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{
		SessionID: "s1", RelayMode: client.RelayModeOn,
		WaitReason: client.WaitModeOn, IdleSec: 0,
	}
	f.answerAfter, f.answer = 2, "keep going"
	injectIdleProbe(t, 0) // the human is demonstrably at the keyboard — irrelevant here
	var log strings.Builder
	res, err := Run(f, payload(t, "codex", map[string]any{
		"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": "A or B?",
	}), fastOpts(&log))
	assert.NoErr(t, err)
	assert.True(t, res.Blocked)
	assert.Eq(t, ReplyPrefix+"keep going", res.Reason)
	assert.Eq(t, 0, f.releases, "an explicit switch is never released by the keyboard probe")
	assert.Eq(t, "A or B?", f.turns["dec-1"].Question)
	assert.True(t, strings.Contains(log.String(), "reason=mode_on"))
	// The Stop still carried its reading: the server keeps the evidence fresh.
	assert.Eq(t, int64(0), *f.beats[0].IdleSec)
}

// TestStopSkipsOnModeOff pins the other explicit switch: `off` never waits, no
// matter how long the human has been away — the Stop stays a plain stop.
func TestStopSkipsOnModeOff(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", RelayMode: client.RelayModeOff, IdleSec: 99999}
	injectIdleProbe(t, 99999) // hours of keyboard silence
	var log strings.Builder
	res, err := Run(f, payload(t, "codex", map[string]any{
		"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": "done",
	}), fastOpts(&log))
	assert.NoErr(t, err)
	assert.False(t, res.Blocked)
	assert.Eq(t, 0, f.waits, "an `off` session must not even poll")
	assert.Eq(t, "done", f.sessions["s1"].LastMessage)
	assert.True(t, strings.Contains(log.String(), "relay off"))
}

// TestStopAutoWaitsOnTurnAgeWhenIdleUnknown is the container case R2 exists for:
// the hook runs where no keyboard can be probed (idle stays -1), so the server
// arms the wait from the time since the last HUMAN input — and the hook waits
// exactly like an idle-armed one, without ever sending a release probe.
func TestStopAutoWaitsOnTurnAgeWhenIdleUnknown(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{
		SessionID: "s1", RelayMode: client.RelayModeAuto, WaitReason: client.WaitTurnAge,
		IdleSec: -1, LastHumanAt: time.Now().Unix() - 22*60,
	}
	f.answerAfter, f.answer = 1, "carry on"
	injectIdleProbe(t, -1) // no X11 / no probe: never a reading
	var log strings.Builder
	res, err := Run(f, payload(t, "codex", map[string]any{
		"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": "still there?",
	}), fastOpts(&log))
	assert.NoErr(t, err)
	assert.True(t, res.Blocked)
	assert.Eq(t, ReplyPrefix+"carry on", res.Reason)
	assert.True(t, strings.Contains(log.String(), "reason=turn_age"))
	// Without a reading there is nothing to re-probe: no release request is sent.
	assert.Eq(t, 0, f.releases)
	assert.Eq(t, int64(-1), *f.beats[0].IdleSec)
}

// waitForTurn blocks until the given fake hub has an OPEN turn.
func waitForTurn(t *testing.T, f *fakeAPI) {
	t.Helper()
	for i := 0; i < 5000; i++ {
		f.mu.Lock()
		open := len(f.turns) > 0 && f.turns["dec-1"].State == "OPEN"
		f.mu.Unlock()
		if open {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the Stop hook never opened its turn")
}

// TestStopAutoReleasedByInterruptOrPrompt covers the release of a fallback-armed
// wait (R2): the wait has no keyboard probe, so the evidence the human is back is
// an EVENT — a prompt they typed, or an interrupt (Esc). Either one settles the
// open turn server-side and the blocked Stop hook releases on its next poll,
// with no web answer involved.
func TestStopAutoReleasedByInterruptOrPrompt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		event   string
		payload map[string]any
	}{
		{"human prompt", "UserPromptSubmit", map[string]any{"prompt": "back at the keyboard"}},
		{"interrupt", "Interrupt", map[string]any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.sessions["s1"] = client.AgentSession{
				SessionID: "s1", RelayMode: client.RelayModeAuto, WaitReason: client.WaitTurnAge,
				IdleSec: -1, LastHumanAt: time.Now().Unix() - 22*60,
			}
			injectIdleProbe(t, -1)
			var log strings.Builder
			opts := fastOpts(&log)
			opts.Wait = 10 * time.Second

			done := make(chan Result, 1)
			go func() {
				res, _ := Run(f, payload(t, "codex", map[string]any{
					"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": "waiting",
				}), opts)
				done <- res
			}()
			waitForTurn(t, f)

			// The human comes back to the terminal: Esc (Interrupt) or a fresh
			// prompt reaches the hub while the Stop hook is still blocked.
			p := map[string]any{"session_id": "s1", "hook_event_name": tc.event, "cwd": "/w"}
			for k, v := range tc.payload {
				p[k] = v
			}
			_, err := Run(f, payload(t, "codex", p), fastOpts(&log))
			assert.NoErr(t, err)

			select {
			case res := <-done:
				assert.False(t, res.Blocked)
			case <-time.After(20 * time.Second):
				t.Fatal("the Stop hook did not release after the human-input event")
			}
			assert.Eq(t, 1, f.humanEvents)
			assert.Eq(t, 0, f.releases, "a fallback wait has no probe to report")
			assert.Eq(t, "EXPIRED", f.turns["dec-1"].State)
			assert.Eq(t, "user_returned", f.turns["dec-1"].ReleasedBy)
		})
	}
}

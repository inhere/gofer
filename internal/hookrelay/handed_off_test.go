package hookrelay

import (
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/client"
)

// handedOffNotice is the one-liner the server puts on a taken-over session (session
// relay §9.1 B): the person at the ORIGINAL terminal has to learn why their relay
// went quiet and where the conversation continues.
const handedOffNotice = "该会话已于 2026-09-18 20:11 在 web 接管（job job-takeover-1），继续请在 web 终端或 --resume；本终端的中继已停用"

// TestHookPrintsHandedOffNotice pins path B's terminal-side half: a heartbeat on a
// HANDED-OFF session carries a `notice`, and the hook must surface it for the
// person at the keyboard instead of blocking. A Stop in that state never opens a
// turn (the switch is dead here — the session already belongs to the takeover
// process), and a human UserPromptSubmit reports it too, because that is the
// moment someone is typing into the abandoned terminal.
func TestHookPrintsHandedOffNotice(t *testing.T) {
	newHandedOff := func() *fakeAPI {
		f := newFake()
		f.sessions["s1"] = client.AgentSession{
			SessionID: "s1", State: "handed_off", RelayMode: client.RelayModeOn,
			Notice: handedOffNotice,
		}
		return f
	}

	t.Run("stop", func(t *testing.T) {
		f := newHandedOff()
		var log strings.Builder
		res, err := Run(f, payload(t, "claude", map[string]any{
			"session_id": "s1", "hook_event_name": "Stop",
		}), fastOpts(&log))
		assert.NoErr(t, err)
		assert.Eq(t, handedOffNotice, res.Notice)
		assert.False(t, res.Blocked)
		assert.Eq(t, 0, f.waits, "a handed-off session must not open or poll a turn")
		if _, ok := f.turns["dec-1"]; ok {
			t.Fatal("a handed-off session must not open a turn")
		}
	})

	t.Run("human prompt", func(t *testing.T) {
		f := newHandedOff()
		var log strings.Builder
		res, err := Run(f, payload(t, "claude", map[string]any{
			"session_id": "s1", "hook_event_name": "UserPromptSubmit", "prompt": "are you still there?",
		}), fastOpts(&log))
		assert.NoErr(t, err)
		assert.Eq(t, handedOffNotice, res.Notice)
		assert.False(t, res.Blocked)
	})

	t.Run("ordinary session carries none", func(t *testing.T) {
		f := newFake()
		f.sessions["s1"] = client.AgentSession{SessionID: "s1", RelayMode: client.RelayModeOff}
		var log strings.Builder
		res, err := Run(f, payload(t, "claude", map[string]any{
			"session_id": "s1", "hook_event_name": "Stop",
		}), fastOpts(&log))
		assert.NoErr(t, err)
		assert.Eq(t, "", res.Notice)
	})
}

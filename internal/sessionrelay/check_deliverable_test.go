package sessionrelay

import (
	"errors"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// CheckDeliverable agrees with Deliver about what is reachable, without sending anything.
func TestCheckDeliverable(t *testing.T) {
	s := newSvc(t)
	assert.True(t, errors.Is(s.CheckDeliverable("nope"), ErrUnknownSession))

	// No injector wired and no open turn: nothing can reach the terminal.
	tmuxSession(t, s, "sid-chk-noinj")
	assert.Eq(t, ReasonNoRunner, DeliverReason(s.CheckDeliverable("sid-chk-noinj")))

	inj := &fakeInjector{}
	s.SetInjector(inj)
	assert.NoErr(t, s.CheckDeliverable("sid-chk-noinj")) // tmux pane + runner + injector
	assert.Len(t, inj.reqs, 0)                           // nothing was sent

	// A session without a pane is unreachable (no deliver_command here)...
	_, err := s.Register(RegisterInput{SessionID: "sid-chk-nopane", Agent: "claude", ProjectKey: "self", Runner: "w-claude", Cwd: "/w", Event: EventSessionStart})
	assert.NoErr(t, err)
	assert.Eq(t, ReasonNoTmux, DeliverReason(s.CheckDeliverable("sid-chk-nopane")))

	// ...until a turn is open (the hook's long poll reaches it).
	_, err = s.SetRelayMode("sid-chk-nopane", "on")
	assert.NoErr(t, err)
	_, err = s.OpenTurn("sid-chk-nopane", "which?", 60)
	assert.NoErr(t, err)
	assert.NoErr(t, s.CheckDeliverable("sid-chk-nopane"))
}

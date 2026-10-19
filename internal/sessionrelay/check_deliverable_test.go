package sessionrelay

import (
	"errors"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

type stubMessenger struct{}

func (stubMessenger) SubmitMessenger(string, string, string, []string, string, string) (string, error) {
	return "j", nil
}
func (stubMessenger) MessengerJob(string) (bool, string, int, string, error) {
	return true, "done", 0, "已发送", nil
}

// CheckDeliverable mirrors SendMessage's routing without sending anything.
func TestCheckDeliverableMirrorsSendMessage(t *testing.T) {
	reg := func(s *Service, in RegisterInput) string {
		in.Agent, in.Event = "claude", EventSessionStart
		if in.Cwd == "" {
			in.Cwd = "/w"
		}
		_, err := s.Register(in)
		assert.NoErr(t, err)
		return in.SessionID
	}
	t.Run("unknown", func(t *testing.T) {
		assert.True(t, errors.Is(newSvc(t).CheckDeliverable("nope"), ErrUnknownSession))
	})
	t.Run("ended", func(t *testing.T) {
		s := newSvc(t)
		sid := reg(s, RegisterInput{SessionID: "e", ProjectKey: "self", Runner: "w"})
		_, err := s.store.SetSessionState(sid, jobstore.SessionEnded)
		assert.NoErr(t, err)
		assert.Eq(t, ReasonEnded, DeliverReason(s.CheckDeliverable(sid)))
	})
	t.Run("messaging disabled", func(t *testing.T) {
		s := newSvc(t)
		sid := reg(s, RegisterInput{SessionID: "d", ProjectKey: "self", Runner: "w", TmuxPane: "%1"})
		s.messagingEnabled = false
		assert.True(t, errors.Is(s.CheckDeliverable(sid), ErrInvalidInput))
	})
	t.Run("waiting for a reply", func(t *testing.T) {
		s := newSvc(t)
		sid := reg(s, RegisterInput{SessionID: "w"})
		_, err := s.store.SetSessionState(sid, jobstore.SessionWaitingReply)
		assert.NoErr(t, err)
		assert.NoErr(t, s.CheckDeliverable(sid))
	})
	t.Run("messenger only", func(t *testing.T) {
		s := newSvc(t) // no injector, no pane, no deliver_command
		s.SetMessenger(stubMessenger{})
		sid := reg(s, RegisterInput{SessionID: "m", ProjectKey: "self", Runner: "w-c", PeerName: "peer-1", PeerMessaging: true})
		assert.NoErr(t, s.CheckDeliverable(sid))
	})
	t.Run("messenger preconditions", func(t *testing.T) {
		s := newSvc(t)
		sid := reg(s, RegisterInput{SessionID: "m1", ProjectKey: "self", Runner: "w-c", PeerName: "p", PeerMessaging: true})
		assert.Eq(t, ReasonNoMessenger, DeliverReason(s.CheckDeliverable(sid)))
		s.SetMessenger(stubMessenger{})
		sid = reg(s, RegisterInput{SessionID: "m2", Runner: "w-c", PeerName: "p", PeerMessaging: true})
		assert.Eq(t, ReasonNoProject, DeliverReason(s.CheckDeliverable(sid)))
		sid = reg(s, RegisterInput{SessionID: "m3", ProjectKey: "self", PeerName: "p", PeerMessaging: true})
		assert.Eq(t, ReasonNoRunner, DeliverReason(s.CheckDeliverable(sid)))
	})
	t.Run("deliver ladder", func(t *testing.T) {
		s := newSvc(t)
		sid := reg(s, RegisterInput{SessionID: "l", ProjectKey: "self", Runner: "w", TmuxPane: "%3"})
		assert.Eq(t, ReasonNoRunner, DeliverReason(s.CheckDeliverable(sid))) // no injector wired
		inj := &fakeInjector{}
		s.SetInjector(inj)
		assert.NoErr(t, s.CheckDeliverable(sid))
		assert.Len(t, inj.reqs, 0) // nothing was sent
		sid = reg(s, RegisterInput{SessionID: "l2", ProjectKey: "self", Runner: "w"})
		assert.Eq(t, ReasonNoTmux, DeliverReason(s.CheckDeliverable(sid)))
		_, err := s.SetRelayMode(sid, "on")
		assert.NoErr(t, err)
		_, err = s.OpenTurn(sid, "which?", 60)
		assert.NoErr(t, err)
		assert.NoErr(t, s.CheckDeliverable(sid)) // an open turn is reachable
	})
}

package sessionrelay

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func newSvc(t *testing.T) *Service {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = st.Close() })
	s := NewService(st)
	s.SetPollInterval(10 * time.Millisecond)
	return s
}

func TestRelayHappyPath(t *testing.T) {
	s := newSvc(t)
	a, err := s.Register(RegisterInput{SessionID: "sid-a", Agent: "Claude", Cwd: "/w/repo", Event: EventSessionStart})
	assert.NoErr(t, err)
	assert.Eq(t, "claude", a.Agent)
	assert.False(t, a.Relay)

	// Relay off: Stop heartbeat → idle, no turn possible.
	hb, err := s.Heartbeat("sid-a", HeartbeatInput{Event: EventStop, LastMessage: "first stop"})
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.SessionIdle, hb.State)
	assert.False(t, hb.Relay)
	_, err = s.OpenTurn("sid-a", "x", 60)
	assert.True(t, errors.Is(err, ErrRelayOff))

	// Switch on, open a turn, wait → answered via Say.
	on, err := s.SetRelay("sid-a", true)
	assert.NoErr(t, err)
	assert.True(t, on.Relay)
	d, err := s.OpenTurn("sid-a", "need a decision", 60)
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.DecisionKindRelay, d.Kind)
	assert.Eq(t, "sid-a", d.SessionID)
	assert.True(t, len(d.Title) > 0)
	got, _ := s.Get("sid-a", 10)
	assert.Eq(t, jobstore.SessionWaitingReply, got.Session.State)
	assert.Eq(t, int64(1), got.Session.TurnNo)
	assert.Len(t, got.Turns, 1)

	st, err := s.WaitTurn(context.Background(), "sid-a", d.ID, 0)
	assert.NoErr(t, err)
	assert.Eq(t, TurnOpen, st.Outcome)

	go func() {
		time.Sleep(30 * time.Millisecond)
		_, _ = s.Say("sid-a", "go with plan B", "human")
	}()
	st, err = s.WaitTurn(context.Background(), "sid-a", d.ID, 2*time.Second)
	assert.NoErr(t, err)
	assert.Eq(t, TurnAnswered, st.Outcome)
	assert.Eq(t, "go with plan B", st.Decision.Answer)
	got, _ = s.Get("sid-a", 10)
	assert.Eq(t, jobstore.SessionRunning, got.Session.State)

	// Nothing open now.
	_, err = s.Say("sid-a", "again", "human")
	assert.True(t, errors.Is(err, ErrNoOpenTurn))
}

func TestRelayOffReleasesWaitAndAutoOff(t *testing.T) {
	s := newSvc(t)
	_, err := s.Register(RegisterInput{SessionID: "sid-b", Agent: "codex"})
	assert.NoErr(t, err)
	_, err = s.SetRelay("sid-b", true)
	assert.NoErr(t, err)
	d, err := s.OpenTurn("sid-b", "waiting", 60)
	assert.NoErr(t, err)

	go func() {
		time.Sleep(30 * time.Millisecond)
		_, _ = s.SetRelay("sid-b", false)
	}()
	st, err := s.WaitTurn(context.Background(), "sid-b", d.ID, 2*time.Second)
	assert.NoErr(t, err)
	assert.Eq(t, TurnRelayOff, st.Outcome)
	assert.False(t, st.Relay)
	assert.Eq(t, jobstore.DecisionExpired, st.Decision.State)
	got, _ := s.Get("sid-b", 5)
	assert.Eq(t, jobstore.SessionIdle, got.Session.State)

	// Auto-off on UserPromptSubmit.
	_, err = s.SetRelay("sid-b", true)
	assert.NoErr(t, err)
	hb, err := s.Heartbeat("sid-b", HeartbeatInput{Event: EventUserPromptSubmit, Title: "repo: task"})
	assert.NoErr(t, err)
	assert.False(t, hb.Relay)
	assert.Eq(t, jobstore.SessionRunning, hb.State)
	assert.Eq(t, "repo: task", hb.Title)

	s.AutoOffOnPrompt = false
	_, _ = s.SetRelay("sid-b", true)
	hb, _ = s.Heartbeat("sid-b", HeartbeatInput{Event: EventUserPromptSubmit})
	assert.True(t, hb.Relay)
}

func TestRelayExpiryAndErrors(t *testing.T) {
	s := newSvc(t)
	_, err := s.Register(RegisterInput{SessionID: "sid-c", Agent: "claude"})
	assert.NoErr(t, err)
	_, _ = s.SetRelay("sid-c", true)
	d, err := s.OpenTurn("sid-c", "short fuse", 2) // store min clamp = 2s
	assert.NoErr(t, err)
	assert.Eq(t, int64(2), d.TimeoutSec)

	st, err := s.WaitTurn(context.Background(), "sid-c", d.ID, 4*time.Second)
	assert.NoErr(t, err)
	assert.Eq(t, TurnExpired, st.Outcome)

	// A new turn expires the stale one and is the only answerable turn.
	d2, err := s.OpenTurn("sid-c", "again", 60)
	assert.NoErr(t, err)
	open, _ := s.store.ListSessionDecisions("sid-c", jobstore.DecisionOpen, 10)
	assert.Len(t, open, 1)
	assert.Eq(t, d2.ID, open[0].ID)

	_, err = s.WaitTurn(context.Background(), "sid-c", "dec-nope", 0)
	assert.True(t, errors.Is(err, ErrUnknownTurn))
	_, err = s.WaitTurn(context.Background(), "sid-other", d2.ID, 0)
	assert.True(t, errors.Is(err, ErrUnknownTurn))
	_, err = s.Heartbeat("sid-nope", HeartbeatInput{Event: EventStop})
	assert.True(t, errors.Is(err, ErrUnknownSession))
	_, err = s.Register(RegisterInput{SessionID: "sid-new"})
	assert.True(t, errors.Is(err, ErrInvalidInput))
	_, err = s.Say("sid-c", "  ", "h")
	assert.True(t, errors.Is(err, ErrInvalidInput))
	assert.True(t, errors.Is(s.Delete("sid-nope"), ErrUnknownSession))

	// OnAnswered from the generic decision endpoint moves waiting → running.
	ok, _ := s.store.AnswerDecision(d2.ID, "via bell", "h")
	assert.True(t, ok)
	full, _, _ := s.store.GetDecision(d2.ID)
	s.OnAnswered(full)
	got, _ := s.Get("sid-c", 5)
	assert.Eq(t, jobstore.SessionRunning, got.Session.State)

	// Resolve by cwd.
	_, _ = s.Register(RegisterInput{SessionID: "sid-d", Agent: "claude", Cwd: "/w/repo"})
	found, err := s.Resolve("/w/repo/sub")
	assert.NoErr(t, err)
	assert.Len(t, found, 1)
	assert.Eq(t, "sid-d", found[0].SessionID)
	assert.NoErr(t, s.Delete("sid-d"))
}

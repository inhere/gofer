package sessionrelay

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// gofer-eh53, relay half: the refusal surfaces on the session, the admin hand-over
// moves ownership, and the "somebody else is beating here" warning is rate-limited
// (a hook beats on every event, so an unfixed token split must not fill the log).

func registerOwned(t *testing.T, s *Service, sid, owner string) {
	t.Helper()
	registerReplySession(t, s, sid, "server", "peer-"+sid, owner)
}

func storedRefusal(t *testing.T, s *Service, sid string) jobstore.AgentSession {
	t.Helper()
	a, ok, err := s.store.GetAgentSession(sid)
	assert.NoErr(t, err)
	assert.True(t, ok)
	return a
}

func TestRefusedReportsAreRecordedAndCleared(t *testing.T) {
	s := newSvc(t)
	registerOwned(t, s, "sid-owner", "alice")

	// Heartbeat: refused (as before) AND noted.
	_, err := s.HeartbeatForOwner("sid-owner", HeartbeatInput{Event: EventStop, CallerID: "bob"})
	assert.True(t, errors.Is(err, ErrSessionHeartbeatOwner))
	a := storedRefusal(t, s, "sid-owner")
	assert.Eq(t, "bob", a.RefusedCallerID)
	assert.True(t, a.RefusedAt > 0)
	assert.Eq(t, "alice", a.CallerID, "the refusal never moves ownership")

	// Registration: same.
	_, allowed, err := s.RegisterForOwner(RegisterInput{SessionID: "sid-owner", Agent: "claude", CallerID: "bob"})
	assert.NoErr(t, err)
	assert.False(t, allowed)
	assert.Eq(t, "bob", storedRefusal(t, s, "sid-owner").RefusedCallerID)

	// Reply: same.
	_, _, err = s.RecordReply("sid-owner", "答复", "", jobstore.SessionReplySourceSession, "bob")
	assert.True(t, errors.Is(err, ErrSessionHeartbeatOwner))
	assert.Eq(t, "bob", storedRefusal(t, s, "sid-owner").RefusedCallerID)

	// The owner's own report lands and clears the note.
	if _, err := s.HeartbeatForOwner("sid-owner", HeartbeatInput{Event: EventStop, CallerID: "alice"}); err != nil {
		t.Fatalf("owner heartbeat: %v", err)
	}
	a = storedRefusal(t, s, "sid-owner")
	assert.Eq(t, "", a.RefusedCallerID)
	assert.Eq(t, int64(0), a.RefusedAt)
}

func TestSetSessionOwnerHandsTheSessionOver(t *testing.T) {
	s := newSvc(t)
	registerOwned(t, s, "sid-move", "alice")
	_, err := s.HeartbeatForOwner("sid-move", HeartbeatInput{Event: EventStop, CallerID: "bob"})
	assert.True(t, errors.Is(err, ErrSessionHeartbeatOwner))

	if _, _, err := s.SetSessionOwner("sid-nope", "bob"); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("unknown session: err=%v, want ErrUnknownSession", err)
	}

	a, from, err := s.SetSessionOwner("sid-move", "bob")
	assert.NoErr(t, err)
	assert.Eq(t, "alice", from)
	assert.Eq(t, "bob", a.CallerID)
	assert.Eq(t, "", a.RefusedCallerID, "the hand-over clears the note")
	assert.Eq(t, "peer-sid-move", a.PeerName, "the session itself is untouched")

	// The caller the hooks now use reports successfully...
	if _, err := s.HeartbeatForOwner("sid-move", HeartbeatInput{Event: EventStop, CallerID: "bob"}); err != nil {
		t.Fatalf("new owner heartbeat: %v", err)
	}
	// ...and the previous owner is the refused one now.
	_, err = s.HeartbeatForOwner("sid-move", HeartbeatInput{Event: EventStop, CallerID: "alice"})
	assert.True(t, errors.Is(err, ErrSessionHeartbeatOwner))
	assert.Eq(t, "alice", storedRefusal(t, s, "sid-move").RefusedCallerID)
}

// captureHandler collects the messages the relay logs, so a test can count them.
type captureHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.msgs = append(h.msgs, r.Message)
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) count(sub string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.msgs {
		if strings.Contains(m, sub) {
			n++
		}
	}
	return n
}

const refusedWarnMsg = "refused a session report from a caller that does not own it"

// TestRefusedOwnerWarnIsRateLimited: one line per session + refused caller per
// window, however many beats arrive (the hook beats on every event).
func TestRefusedOwnerWarnIsRateLimited(t *testing.T) {
	// Not parallel: it swaps the process-wide slog handler.
	h := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	defer slog.SetDefault(prev)

	s := newSvc(t)
	registerOwned(t, s, "sid-warn", "alice")
	for i := 0; i < 3; i++ {
		_, err := s.HeartbeatForOwner("sid-warn", HeartbeatInput{Event: EventStop, CallerID: "bob"})
		assert.True(t, errors.Is(err, ErrSessionHeartbeatOwner))
	}
	assert.Eq(t, 1, h.count(refusedWarnMsg), "three refused beats are one line")

	// A different refused caller is its own line.
	_, err := s.HeartbeatForOwner("sid-warn", HeartbeatInput{Event: EventStop, CallerID: "carol"})
	assert.True(t, errors.Is(err, ErrSessionHeartbeatOwner))
	assert.Eq(t, 2, h.count(refusedWarnMsg))

	// Past the window the same pair is reported again.
	base := time.Now()
	s.nowFn = func() time.Time { return base.Add(RefusedLogWindow + time.Minute) }
	_, err = s.HeartbeatForOwner("sid-warn", HeartbeatInput{Event: EventStop, CallerID: "bob"})
	assert.True(t, errors.Is(err, ErrSessionHeartbeatOwner))
	assert.Eq(t, 3, h.count(refusedWarnMsg))
}

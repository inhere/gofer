package sessionrelay

import (
	"context"
	"log/slog"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// The "this session can take a message again" hook. Nothing in the relay has to do when a
// terminal comes back — except that gofer may hold texts for it: the seat schedule whose due
// run found the session offline queues its text (jobstore.SeatPending) and the seat service
// sends it late. Until this hook existed the late send waited for the next periodic sweep,
// so a person resuming a terminal read their queued schedule up to half a minute later.
//
// The relay does not know about seats (G021–G024): it only reports the transition, and the
// assembly wires the report to the seat service. The report runs OFF the hook request path —
// a hook beat must never wait for a message to be delivered — and is best effort: every
// failure is logged and the sweep is still there as the fallback.

// liveHookTimeout bounds one call of the came-back hook: the same order as the late send
// that follows a bind (seat.bindFlushTimeout), because that is what the hook runs.
const liveHookTimeout = 30 * time.Second

// liveHookFunc is the came-back hook: sid can receive messages again.
type liveHookFunc func(ctx context.Context, sid string)

// SetOnSessionLive installs (nil clears) the came-back hook. It is wired once at serve
// assembly; the call reads it lock-free on every beat.
func (s *Service) SetOnSessionLive(fn func(ctx context.Context, sid string)) {
	if fn == nil {
		s.onLive.Store(nil)
		return
	}
	h := liveHookFunc(fn)
	s.onLive.Store(&h)
}

// noteCameBack reports a session that just moved INTO a state that can receive a message, out
// of one that could not (offline / ended). seen=false (the row did not exist yet, or could not
// be read) is not a comeback: a brand-new id has no seat holding a text for it, and a failed
// read is no evidence that anything came back.
//
// A beat that keeps a live session live (Stop, a prompt, a notification) is not a comeback and
// does not fire: the queued texts of a session that never went away are no business of this
// hook, and firing on every beat would mean a seat lookup per hook event.
func (s *Service) noteCameBack(sid, prev, now string, seen bool) {
	if sid == "" || !seen || !sessionReceivable(now) {
		return
	}
	if prev != jobstore.SessionOffline && prev != jobstore.SessionEnded {
		return
	}
	s.fireSessionLive(sid)
}

// sessionStateBefore reads the state a hook beat is about to overwrite. The relay's in-memory
// state memory is not enough: the offline sweep (serve, MarkStaleSessionsOffline) marks a silent
// session offline WITHOUT the relay seeing it, so a beat that brings it back reads as "running"
// there. The store is the only place that knows.
func (s *Service) sessionStateBefore(sid string) (string, bool) {
	a, ok, err := s.store.GetAgentSession(sid)
	if err != nil {
		slog.Warn("sessionrelay: read the session state before a beat failed", "session", sid, "err", err)
		return "", false
	}
	if !ok {
		return "", false
	}
	return a.State, true
}

// cameBackBefore is the read behind noteCameBack: the state a write is about to overwrite. It
// costs nothing when no came-back hook is installed (a bare relay in tests, or an embedder with
// no seat service) and nothing for a beat that cannot leave a state which takes messages (state
// "" = the beat keeps the stored state). With the hook installed it is one indexed read per hook
// beat — the price of reliability: the offline sweep marks a session offline without the relay
// seeing it, so the in-memory state memory cannot answer this question.
func (s *Service) cameBackBefore(sid, state string) (string, bool) {
	if s.onLive.Load() == nil {
		return "", false
	}
	if state != "" && !sessionReceivable(state) {
		return "", false
	}
	return s.sessionStateBefore(sid)
}

// sessionReceivable reports whether a session in this state can be handed a message: every
// state but the ones that say the process is gone (offline / ended) or that another process
// drives it (handed_off). The seat service makes the same call per delivery (its deliverNow);
// this one only decides whether the report is worth making.
func sessionReceivable(state string) bool {
	switch state {
	case "", jobstore.SessionOffline, jobstore.SessionEnded, jobstore.SessionHandedOff:
		return false
	}
	return true
}

// fireSessionLive runs the hook in its own goroutine under liveHookTimeout. A panic in a hook
// must not take the server down with the hook beat that triggered it.
func (s *Service) fireSessionLive(sid string) {
	h := s.onLive.Load()
	if h == nil {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("session came-back hook panicked", "session", sid, "recover", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), liveHookTimeout)
		defer cancel()
		(*h)(ctx, sid)
	}()
}

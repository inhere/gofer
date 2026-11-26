package sessionrelay

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// The came-back hook (live_hook.go): the relay reports a session that the store just moved out
// of offline / ended to any state that takes messages, through the real write paths (a
// re-registration and a heartbeat), and stays quiet for everything that is not a comeback.

// liveRecorder records the sessions the relay reported as back.
type liveRecorder struct {
	mu   sync.Mutex
	sids []string
}

func (r *liveRecorder) hook(_ context.Context, sid string) {
	r.mu.Lock()
	r.sids = append(r.sids, sid)
	r.mu.Unlock()
}

func (r *liveRecorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sids...)
}

// TestCameBackHookFiresOnRegistrationAndHeartbeat: an offline session that re-registers (a
// resumed CLI's SessionStart) and one that was marked offline by the sweep and then beats are
// both reported; a session that never went away is not, and the report happens off the caller's
// goroutine.
func TestCameBackHookFiresOnRegistrationAndHeartbeat(t *testing.T) {
	s := newSvc(t)
	rec := &liveRecorder{}
	s.SetOnSessionLive(rec.hook)
	if _, err := s.Register(RegisterInput{SessionID: "sid-live-1", Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	// A brand-new session is no comeback: there is no earlier state, and no seat holds a text.
	if got := rec.got(); len(got) != 0 {
		t.Fatalf("the first registration was reported as a comeback: %v", got)
	}

	// A live beat is not a comeback either: the session never went away.
	if _, err := s.Heartbeat("sid-live-1", HeartbeatInput{Event: EventStop}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(RegisterInput{SessionID: "sid-live-1", Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	if got := rec.got(); len(got) != 0 {
		t.Fatalf("a session that never went away was reported as back: %v", got)
	}

	// The offline sweep marks it silent (no relay call at all), then a beat brings it back.
	if _, err := s.store.SetSessionState("sid-live-1", jobstore.SessionOffline); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Heartbeat("sid-live-1", HeartbeatInput{Event: EventNotification}); err != nil {
		t.Fatal(err)
	}
	wait.For(t, 5*time.Second, "the beat that revived the session is reported", func() (bool, any) {
		return len(rec.got()) == 1, rec.got()
	})
	if got := rec.got(); got[0] != "sid-live-1" {
		t.Fatalf("reported %v, want sid-live-1", got)
	}

	// A session that ended and re-registers comes back the same way.
	if _, err := s.Heartbeat("sid-live-1", HeartbeatInput{Event: EventSessionEnd}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(RegisterInput{SessionID: "sid-live-1", Agent: "claude", Event: EventSessionStart}); err != nil {
		t.Fatal(err)
	}
	wait.For(t, 5*time.Second, "the re-registration after the end is reported", func() (bool, any) {
		return len(rec.got()) == 2, rec.got()
	})
}

// TestCameBackHookQuietWithoutAHook: a relay with no came-back hook wired reads nothing extra on
// the hook path — the state memory alone decides, and no report is made.
func TestCameBackHookQuietWithoutAHook(t *testing.T) {
	s := newSvc(t)
	if _, err := s.Register(RegisterInput{SessionID: "sid-live-2", Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.SetSessionState("sid-live-2", jobstore.SessionOffline); err != nil {
		t.Fatal(err)
	}
	// Nothing to assert but the absence of a panic / a report: the store read is skipped
	// entirely, which cameBackBefore makes happen by answering "no hook" first.
	if _, ok := s.cameBackBefore("sid-live-2", jobstore.SessionRunning); ok {
		t.Fatal("a relay with no came-back hook read the prior state")
	}
	if _, err := s.Heartbeat("sid-live-2", HeartbeatInput{Event: EventNotification}); err != nil {
		t.Fatal(err)
	}
}

// TestCameBackHookIgnoresSessionsThatCannotTakeMessages: a beat that moves a session into
// offline / ended / handed_off is not a comeback even when it comes from an offline session,
// because nothing could take a queued text there.
func TestCameBackHookIgnoresSessionsThatCannotTakeMessages(t *testing.T) {
	s := newSvc(t)
	rec := &liveRecorder{}
	s.SetOnSessionLive(rec.hook)
	if _, err := s.Register(RegisterInput{SessionID: "sid-live-3", Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.SetSessionState("sid-live-3", jobstore.SessionOffline); err != nil {
		t.Fatal(err)
	}
	// offline -> ended: a state change, but nothing can take a message at the far end.
	if _, err := s.Heartbeat("sid-live-3", HeartbeatInput{Event: EventSessionEnd}); err != nil {
		t.Fatal(err)
	}
	if got := rec.got(); len(got) != 0 {
		t.Fatalf("a transition into ended was reported as back: %v", got)
	}
}

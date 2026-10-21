package sessionrelay

import (
	"context"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// TestNotifyJobEndedLeavesTheNoticeToTheParkedHook is gofer-fv6a: while a session's
// Stop hook is PARKED on its watched jobs (it announced StopParkSec, then polls the
// watches), the notice must be left to that hook. Claiming the watch and sending the
// notice through the messenger instead could not reach the agent — it was blocked in
// its own Stop hook — and the watch that would have woken it was already gone, so the
// notice waited for the next human prompt (~25 minutes in the reported case).
func TestNotifyJobEndedLeavesTheNoticeToTheParkedHook(t *testing.T) {
	s := newSvc(t)
	f := &notifyFake{}
	s.SetMessenger(f)
	notifySession(t, s, "sid-park")
	if _, err := s.store.AddSessionJobWatch("sid-park", "job-pk"); err != nil {
		t.Fatal(err)
	}
	park := 120
	_, err := s.Heartbeat("sid-park", HeartbeatInput{Event: EventStop, StopParkSec: &park})
	assert.NoErr(t, err)
	a, err := s.Session("sid-park")
	assert.NoErr(t, err)
	assert.True(t, StopHookParked(a, s.now().Unix()), "the announced park must be stored")

	n := JobEndedNotice{TargetSession: "sid-park", JobID: "job-pk", Title: "build", Status: "done"}
	assert.NoErr(t, s.NotifyJobEnded(context.Background(), n))
	assert.Eq(t, 0, f.submitted, "the parked hook is the delivery channel, not the messenger")
	// The watch stays: it is what the parked hook polls AND the at-most-once record.
	watches, err := s.store.ListSessionJobWatches("sid-park")
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(watches), "the parked hook's watch must not be claimed")
	assert.Eq(t, 0, len(outboxMessages(t, s, "sid-park")))

	// The park ends (the person typed): the notice now goes out through the messenger,
	// once, claiming the watch exactly as before.
	_, err = s.Heartbeat("sid-park", HeartbeatInput{Event: EventUserPromptSubmit})
	assert.NoErr(t, err)
	a, err = s.Session("sid-park")
	assert.NoErr(t, err)
	assert.Eq(t, int64(0), a.StopParkUntil, "a human prompt ends an announced park")

	assert.NoErr(t, s.NotifyJobEnded(context.Background(), n))
	assert.Eq(t, 1, f.submitted)
	watches, err = s.store.ListSessionJobWatches("sid-park")
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(watches))
	// A repeated ended-hook cannot deliver a second notice.
	assert.NoErr(t, s.NotifyJobEnded(context.Background(), n))
	assert.Eq(t, 1, f.submitted)
}

// TestNotifyJobEndedDeliversAfterTheParkExpires: a park the hook never cleared (its
// process died inside the wait) must not hold notices for ever — past the announced
// deadline the session is an ordinary idle one again and the messenger carries it.
func TestNotifyJobEndedDeliversAfterTheParkExpires(t *testing.T) {
	s := newSvc(t)
	f := &notifyFake{}
	s.SetMessenger(f)
	notifySession(t, s, "sid-park-exp")
	if _, err := s.store.AddSessionJobWatch("sid-park-exp", "job-pe"); err != nil {
		t.Fatal(err)
	}
	park := 60
	_, err := s.Heartbeat("sid-park-exp", HeartbeatInput{Event: EventStop, StopParkSec: &park})
	assert.NoErr(t, err)

	base := time.Now()
	s.nowFn = func() time.Time { return base.Add(time.Duration(park+10) * time.Second) }
	assert.NoErr(t, s.NotifyJobEnded(context.Background(), JobEndedNotice{
		TargetSession: "sid-park-exp", JobID: "job-pe", Status: "done",
	}))
	assert.Eq(t, 1, f.submitted)
}

// TestSendMessageAnswersTheOpenTurn pins the ladder SendMessage shares with Deliver
// (gofer-fv6a): the TURN decides, not the session state. A probe beat that moves the
// state while the turn stays open used to send the message to the messenger, where the
// agent — blocked in its Stop hook — could not read it.
func TestSendMessageAnswersTheOpenTurn(t *testing.T) {
	s := newSvc(t)
	s.ConfigureMessaging(true, "claude", time.Second, time.Second)
	notifySession(t, s, "sid-drift")
	if _, err := s.SetRelayMode("sid-drift", jobstore.RelayModeOn); err != nil {
		t.Fatal(err)
	}
	turn, err := s.OpenTurn("sid-drift", "waiting for a reply", 300)
	assert.NoErr(t, err)
	// The state drifts off waiting_reply while the turn stays open.
	_, err = s.Heartbeat("sid-drift", HeartbeatInput{State: jobstore.SessionRunning})
	assert.NoErr(t, err)

	f := &notifyFake{}
	s.SetMessenger(f)
	m, err := s.SendMessage(context.Background(), "sid-drift", "hello", "web", "")
	assert.NoErr(t, err)
	assert.Eq(t, PathTurn, m.Channel)
	assert.Eq(t, turn.ID, m.DecisionID)
	assert.Eq(t, 0, f.submitted, "an open turn is answered directly, never through the messenger")
	msgs, err := s.store.ListOpenSessionTurns("sid-drift", 1)
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(msgs), "the turn was answered")
}

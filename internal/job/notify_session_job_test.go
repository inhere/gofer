package job

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// --notify-session <job id> (hosted resident session design §4): a job can name a
// persistent session JOB as the place its result goes. The delivery reuses the resident
// parent's child receipt (wakeup_session.go), so these cases assert the row it
// registers and the say that row turns into — no second delivery path.

// notifyChild submits one short exec job that asks to tell the given target and waits
// for it to end.
func notifyChild(t *testing.T, s *Service, target string) JobResult {
	t.Helper()
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"},
		Cwd: ".", TimeoutSec: 60, Notify: true, NotifySession: target,
	})
	if err != nil {
		t.Fatalf("Submit with notify_session %q: %v", target, err)
	}
	if _, ok := s.Wait(res.ID); !ok {
		t.Fatalf("Wait: job %s not found", res.ID)
	}
	return res
}

// notifySessionFixture is wakeupSessionFixture with a real job id on the resident
// parent: --notify-session recognizes a session job by its `job-` prefixed id.
func notifySessionFixture(t *testing.T) (*Service, *recordingSessionSender, *jobEntry) {
	t.Helper()
	s, sender := remoteSayService(t, 0)
	parent := addRemoteSaySession(t, s, JobIDPrefix+"parent-job", StatusAwaitingInput, 1)
	return s, sender, parent
}

// TestNotifySessionJobRegistersReceipt: naming a session JOB registers the same
// continuous event receipt a resident parent gets for a child, watching the notifying
// job; the result then arrives as the session's next turn.
func TestNotifySessionJobRegistersReceipt(t *testing.T) {
	t.Parallel()
	s, sender, parent := notifySessionFixture(t)

	res := notifyChild(t, s, parent.result.ID)

	rows := childReceipts(t, s, parent.result.ID)
	assert.Eq(t, 1, len(rows), "receipts = %+v", rows)
	w := rows[0]
	assert.Eq(t, jobstore.WakeupKindEvent, w.Kind)
	assert.Eq(t, res.ID, w.FilterJobID, "the receipt must watch the notifying job")
	assert.Eq(t, 1, w.AutoChild, "the receipt is spoken, not resumed")
	assert.True(t, contains(jobstore.DecodeStringList(w.EventTypesJSON), EventJobTerminal),
		"events = %v, want the end of the job", jobstore.DecodeStringList(w.EventTypesJSON))

	wait.Until(t, 15*time.Second, "the result turn of "+res.ID+" reaches the session", func() bool {
		return len(sayTexts(sender)) > 0
	})
	texts := sayTexts(sender)
	assert.Eq(t, 1, len(texts), "says = %v, want one result turn", texts)
	assert.True(t, strings.Contains(texts[0], res.ID), "say = %q, want it to name %s", texts[0], res.ID)
	assert.True(t, strings.Contains(texts[0], StatusDone), "say = %q, want the outcome", texts[0])
}

// TestNotifySessionJobParksWhileBusy: a session in the middle of a turn is not
// interrupted; the result waits for awaiting_input and is delivered then.
func TestNotifySessionJobParksWhileBusy(t *testing.T) {
	t.Parallel()
	s, sender, parent := notifySessionFixture(t)
	parent.mu.Lock()
	parent.result.Status = StatusRunning
	parent.mu.Unlock()

	res := notifyChild(t, s, parent.result.ID)

	rows := childReceipts(t, s, parent.result.ID)
	assert.Eq(t, 1, len(rows), "receipts = %+v", rows)
	wait.Until(t, 15*time.Second, "the receipt of "+res.ID+" parked as pending-say", func() bool {
		return childWakeRow(t, s, rows[0].ID).ContinuationJobID == jobstore.WakeupPendingSay
	})
	assert.Eq(t, 0, len(sayTexts(sender)), "a busy session was interrupted")

	parent.mu.Lock()
	parent.result.Status = StatusAwaitingInput
	parent.result.TurnNo = 5
	parent.mu.Unlock()
	s.onParentAwaitingInput(parent.result.ID)

	texts := sayTexts(sender)
	assert.Eq(t, 1, len(texts), "says = %v, want the parked result delivered", texts)
	assert.True(t, strings.Contains(texts[0], res.ID), "say = %q, want it to name %s", texts[0], res.ID)
}

// TestNotifySessionJobRejected: only a live persistent session of this server can be
// named. Anything else is a 400 (ErrNotifyTargetRejected) that says why, never a job
// whose notice silently goes nowhere.
func TestNotifySessionJobRejected(t *testing.T) {
	t.Parallel()
	s, _, _ := notifySessionFixture(t)
	plain := JobIDPrefix + "plain-job"
	ended := JobIDPrefix + "ended-session"
	for _, r := range []JobResult{
		{ID: plain, ProjectKey: "self", Agent: "exec", Runner: "local", Status: StatusDone},
		{ID: ended, ProjectKey: "self", Agent: "exec", Runner: "local", Status: StatusDone, Session: true},
	} {
		if err := s.persist(r); err != nil {
			t.Fatalf("persist %s: %v", r.ID, err)
		}
	}

	cases := map[string]string{
		"unknown job":        JobIDPrefix + "20261117-101010-abcdef12",
		"not a session job":  plain,
		"a finished session": ended,
	}
	for name, target := range cases {
		_, err := s.Submit(JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"},
			Cwd: ".", TimeoutSec: 60, Notify: true, NotifySession: target,
		})
		if !errors.Is(err, ErrNotifyTargetRejected) || !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("%s: err = %v, want ErrInvalidRequest + ErrNotifyTargetRejected", name, err)
		}
	}
}

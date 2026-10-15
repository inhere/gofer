package sessionrelay

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// notifyFake counts the messenger jobs a notice started, so a test can assert that
// a notification was (or was not) sent exactly once.
type notifyFake struct {
	submitted  int
	submitErr  error
	deliverErr error
	lastTitle  string
	lastCaller string
}

func (f *notifyFake) SubmitMessenger(_, _, _ string, _ []string, title, caller string) (string, error) {
	if f.submitErr != nil {
		return "", f.submitErr
	}
	f.submitted++
	f.lastTitle, f.lastCaller = title, caller
	return "messenger-job-1", nil
}

func (f *notifyFake) MessengerJob(string) (bool, string, int, string, error) {
	if f.deliverErr != nil {
		return true, "failed", 1, f.deliverErr.Error(), nil
	}
	return true, "done", 0, "已发送", nil
}

// notifySession registers one live Claude session with a messenger address.
func notifySession(t *testing.T, s *Service, sid string) {
	t.Helper()
	_, err := s.Register(RegisterInput{
		SessionID: sid, Agent: "claude", ProjectKey: "self", Runner: "local",
		PeerName: "proj-" + sid, PeerMessaging: true, Event: EventSessionStart,
	})
	assert.NoErr(t, err)
}

func outboxMessages(t *testing.T, s *Service, sid string) []jobstore.SessionMessage {
	t.Helper()
	list, err := s.store.ReadSessionOutbox(sid)
	assert.NoErr(t, err)
	return list
}

// TestNotifyJobEndedDeliversOnceAndClaimsTheWatch is the core of gofer-3db4: the
// notice is sent through the messenger, and the WATCH is the durable claim, so a
// repeated ended-hook (a needs_review job accepted later, or the same event after a
// restart) cannot send a second one.
func TestNotifyJobEndedDeliversOnceAndClaimsTheWatch(t *testing.T) {
	s := newSvc(t)
	f := &notifyFake{}
	s.SetMessenger(f)
	notifySession(t, s, "sid-notify")
	if _, err := s.store.AddSessionJobWatch("sid-notify", "job-n1"); err != nil {
		t.Fatal(err)
	}

	n := JobEndedNotice{TargetSession: "sid-notify", JobID: "job-n1", Title: "build it",
		Status: "failed", ExitCode: 2, Duration: 95 * time.Second, WrapUp: "max_steps"}
	assert.NoErr(t, s.NotifyJobEnded(context.Background(), n))

	assert.Eq(t, 1, f.submitted)
	assert.Eq(t, OperatorGofer, f.lastCaller)
	msgs := outboxMessages(t, s, "sid-notify")
	if len(msgs) != 1 {
		t.Fatalf("outbox messages = %d, want 1 (%+v)", len(msgs), msgs)
	}
	assert.Eq(t, jobstore.SessionMessageDelivered, msgs[0].Status)
	assert.Eq(t, OperatorGofer, msgs[0].Operator)
	text := msgs[0].Text
	for _, want := range []string{JobDoneTag, "job-n1", "build it", "status=failed", "exit=2",
		"wrap_up=max_steps", "gofer job show job-n1", "gofer job logs job-n1", "系统通知"} {
		if !strings.Contains(text, want) {
			t.Errorf("notice %q does not contain %q", text, want)
		}
	}
	if strings.Count(text, "\n") != 0 {
		t.Errorf("notice must be one line: %q", text)
	}

	// Second dispatch of the SAME event/terminal state: nothing is sent again.
	assert.NoErr(t, s.NotifyJobEnded(context.Background(), n))
	assert.Eq(t, 1, f.submitted)
	assert.Eq(t, 1, len(outboxMessages(t, s, "sid-notify")))
	watches, err := s.store.ListSessionJobWatches("sid-notify")
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(watches), "the claimed watch must be gone (that IS the dedupe record)")
}

// TestNotifyJobEndedRestoresTheWatchWhenDeliveryFails: a failed delivery keeps the
// pending record, so the Stop hook / catch-up path still delivers the notice — the
// fallback the design leans on — and a later attempt can deliver it for real.
func TestNotifyJobEndedRestoresTheWatchWhenDeliveryFails(t *testing.T) {
	s := newSvc(t)
	f := &notifyFake{submitErr: errors.New("runner offline")}
	s.SetMessenger(f)
	notifySession(t, s, "sid-fail")
	if _, err := s.store.AddSessionJobWatch("sid-fail", "job-f1"); err != nil {
		t.Fatal(err)
	}
	n := JobEndedNotice{TargetSession: "sid-fail", JobID: "job-f1", Status: "done"}

	assert.Err(t, s.NotifyJobEnded(context.Background(), n))
	watches, err := s.store.ListSessionJobWatches("sid-fail")
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(watches), "a failed delivery must keep the watch for the hook fallback")

	// The second attempt (the same ended-hook on a later event) now succeeds and
	// claims the watch for good. The outbox keeps the FAILED attempt as well, so the
	// assertion is on the delivered notice, not on the message count.
	f.submitErr = nil
	assert.NoErr(t, s.NotifyJobEnded(context.Background(), n))
	watches, err = s.store.ListSessionJobWatches("sid-fail")
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(watches))
	delivered := 0
	for _, m := range outboxMessages(t, s, "sid-fail") {
		if m.Status == jobstore.SessionMessageDelivered {
			delivered++
			assert.Eq(t, OperatorGofer, m.Operator)
		}
	}
	assert.Eq(t, 1, delivered)
}

// TestNotifyJobEndedSkipsWithoutAPendingWatch: no watch means somebody else already
// delivered this job's notice (the session's own hook, most often) — a messenger is
// never started for it.
func TestNotifyJobEndedSkipsWithoutAPendingWatch(t *testing.T) {
	s := newSvc(t)
	f := &notifyFake{}
	s.SetMessenger(f)
	notifySession(t, s, "sid-nw")

	assert.NoErr(t, s.NotifyJobEnded(context.Background(), JobEndedNotice{
		TargetSession: "sid-nw", JobID: "job-nw", Status: "done",
	}))
	assert.Eq(t, 0, f.submitted)
	assert.Eq(t, 0, len(outboxMessages(t, s, "sid-nw")))
}

// TestNotifyJobEndedRequiresATarget pins the usage error: the notification is only
// ever called for a job that named a session.
func TestNotifyJobEndedRequiresATarget(t *testing.T) {
	s := newSvc(t)
	assert.Err(t, s.NotifyJobEnded(context.Background(), JobEndedNotice{JobID: "job-x"}))
}

// TestFormatJobEndedNoticeNeedsReviewHint: a delivery parked for a human says where
// to rule on it, like the hook's own notice does.
func TestFormatJobEndedNoticeNeedsReviewHint(t *testing.T) {
	text := FormatJobEndedNotice(JobEndedNotice{JobID: "job-r", Title: "review me", Status: "needs_review"})
	if !strings.HasPrefix(text, JobDoneTag+" job-r review me") {
		t.Fatalf("notice = %q", text)
	}
	if !strings.Contains(text, "gofer job show job-r") {
		t.Fatalf("notice = %q", text)
	}
}

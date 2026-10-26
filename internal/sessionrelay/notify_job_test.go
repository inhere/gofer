package sessionrelay

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// notifyFake counts the messenger jobs a notice started, so a test can assert that
// a notification was (or was not) sent exactly once. lastCommand is the argv the
// messenger was started with (the prompt tells how the target will read the text).
type notifyFake struct {
	submitted   int
	submitErr   error
	deliverErr  error
	lastTitle   string
	lastCaller  string
	lastCommand []string
}

func (f *notifyFake) SubmitMessenger(_, _, _ string, command []string, title, caller string) (string, error) {
	if f.submitErr != nil {
		return "", f.submitErr
	}
	f.submitted++
	f.lastTitle, f.lastCaller, f.lastCommand = title, caller, command
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
		PeerName: "proj-" + sid, PeerMessaging: true, Event: EventSessionStart, CallerID: "alice",
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
	// The messenger job carries the SESSION OWNER as its ceiling owner; the notice itself is
	// spoken by gofer (the outbox operator below), which is a label nobody holds a ceiling for.
	assert.Eq(t, "alice", f.lastCaller)
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
	text := FormatJobEndedNotice(i18n.ZhCN, JobEndedNotice{JobID: "job-r", Title: "review me", Status: "needs_review"})
	if !strings.HasPrefix(text, JobDoneTag+" job-r review me") {
		t.Fatalf("notice = %q", text)
	}
	if !strings.Contains(text, "gofer job show job-r") {
		t.Fatalf("notice = %q", text)
	}
}

// The notice opens with gofer's own prefix and never with the web's, which a session
// (and the rules an agent is given) reads as "the user typed this on the web"
// (gofer-11gk).
func TestFormatJobEndedNoticeUsesTheSystemNoticePrefix(t *testing.T) {
	text := FormatJobEndedNotice(i18n.ZhCN, JobEndedNotice{JobID: "job-p", Status: "done"})
	if !strings.HasPrefix(text, JobDoneTag+" ") {
		t.Fatalf("notice = %q, want the %q prefix", text, JobDoneTag)
	}
	if strings.Contains(text, WebMessagePrefix) {
		t.Fatalf("notice %q must not carry the web message prefix", text)
	}
}

// The messenger is handed the notice line verbatim: no web prefix is glued in front
// of it, so the target session reads a system notice and not a message from a web
// user called "gofer" (gofer-11gk). A person's message keeps the web prefix.
func TestMessengerPromptKeepsNoticesApartFromWebMessages(t *testing.T) {
	notice := FormatJobEndedNotice(i18n.ZhCN, JobEndedNotice{JobID: "job-m", Status: "done"})
	prompt := messengerPrompt(i18n.ZhCN, "proj-a", OperatorGofer, notice)
	line := prompt[strings.LastIndex(prompt, "\n")+1:]
	if !strings.HasPrefix(line, JobDoneTag) {
		t.Fatalf("forwarded line = %q, want the notice under %q", line, JobDoneTag)
	}
	if strings.Contains(prompt, WebMessagePrefix) {
		t.Fatalf("a system notice must not wear the web prefix: %q", prompt)
	}
	if web := messengerPrompt(i18n.ZhCN, "proj-a", "alice", "hi"); !strings.Contains(web, WebMessagePrefix+"，alice] hi") {
		t.Fatalf("a person's message keeps the web prefix: %q", web)
	}
}

// TestNotifyJobEndedMessengerPromptIsASystemNotice is the delivered-text half: the
// messenger job the notice starts is one whose forwarded line opens with the
// job-done prefix (the same words the hook's caught-up lines use).
func TestNotifyJobEndedMessengerPromptIsASystemNotice(t *testing.T) {
	s := newSvc(t)
	f := &notifyFake{}
	s.SetMessenger(f)
	notifySession(t, s, "sid-notice-prompt")
	if _, err := s.store.AddSessionJobWatch("sid-notice-prompt", "job-np"); err != nil {
		t.Fatal(err)
	}
	n := JobEndedNotice{TargetSession: "sid-notice-prompt", JobID: "job-np", Status: "done"}
	assert.NoErr(t, s.NotifyJobEnded(context.Background(), n))

	prompt := ""
	for i, arg := range f.lastCommand {
		if arg == "-p" && i+1 < len(f.lastCommand) {
			prompt = f.lastCommand[i+1]
		}
	}
	if prompt == "" {
		t.Fatalf("messenger started without a -p prompt: %q", f.lastCommand)
	}
	line := prompt[strings.LastIndex(prompt, "\n")+1:]
	if !strings.HasPrefix(line, JobDoneTag) || !strings.Contains(line, "job-np") {
		t.Fatalf("forwarded line = %q, want %q + job-np", line, JobDoneTag)
	}
	if strings.Contains(prompt, WebMessagePrefix) {
		t.Fatalf("the notice prompt must not carry the web prefix: %q", prompt)
	}
}

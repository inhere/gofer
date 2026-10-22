package mail

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// The 信箱广播 (design 2026-10-22): one letter per recipient, one thread, one broadcast group,
// the per-recipient permission table, and the broadcast limits.

func permitAll(Target) error { return nil }

// TestBroadcastExplicitRecipients: repeated addresses each get a row, all in one thread, all in
// one broadcast group (the first row's id).
func TestBroadcastExplicitRecipients(t *testing.T) {
	f := threeSessionMailbox(t)
	res, err := f.svc.Broadcast(BroadcastInput{
		To: []string{"session:sess-a", "session:sess-off"}, Body: "v0.183.1 已发布，合并前不必先提交 tracker",
		Origin: sender(),
	})
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if len(res.Mails) != 2 || len(res.Recipients) != 2 {
		t.Fatalf("mails=%d recipients=%d, want 2 and 2", len(res.Mails), len(res.Recipients))
	}
	if res.BroadcastID != res.Mails[0].ID {
		t.Fatalf("broadcast id = %d, want the first letter %d", res.BroadcastID, res.Mails[0].ID)
	}
	if res.Matched != 2 || res.Skipped != 0 {
		t.Fatalf("matched=%d skipped=%d, want 2/0", res.Matched, res.Skipped)
	}
	for i, m := range res.Mails {
		if m.ThreadID != res.ThreadID {
			t.Fatalf("letter %d thread = %q, want %q", m.ID, m.ThreadID, res.ThreadID)
		}
		if m.BroadcastID != res.BroadcastID {
			t.Fatalf("letter %d broadcast = %d, want %d", m.ID, m.BroadcastID, res.BroadcastID)
		}
		if m.State != jobstore.MailPending {
			t.Fatalf("letter %d state = %s, want pending", m.ID, m.State)
		}
		if res.Recipients[i].MailID != m.ID || res.Recipients[i].ID == "" {
			t.Fatalf("recipient %d = %+v, want mail %d", i, res.Recipients[i], m.ID)
		}
	}
	// The copies are one broadcast in the store too (the count the injected tag shows).
	if n, err := f.store.CountBroadcastMail(res.BroadcastID); err != nil || n != 2 {
		t.Fatalf("count broadcast = %d, %v; want 2", n, err)
	}
}

// TestBroadcastEachTopicFiltersByPermission: an each: selector sends to the matches it may reach
// and reports the rest, without naming them.
func TestBroadcastEachTopicFiltersByPermission(t *testing.T) {
	f := newFixture(t)
	f.addSession(t, "sess-a", "claude", "login 页面", jobstore.SessionRunning)
	f.addSession(t, "sess-b", "claude", "login 修复", jobstore.SessionRunning)
	f.addSession(t, "sess-c", "codex", "login 部署", jobstore.SessionRunning)
	denied := errors.New("another caller's session")
	res, err := f.svc.Broadcast(BroadcastInput{
		To: []string{"each:topic:login"}, Body: "群发", Origin: sender(),
		Authorize: func(tg Target) error {
			if tg.ID == "sess-c" {
				return denied
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if len(res.Mails) != 2 {
		t.Fatalf("delivered %d, want 2", len(res.Mails))
	}
	if res.Matched != 3 || res.Skipped != 1 {
		t.Fatalf("matched=%d skipped=%d, want 3/1", res.Matched, res.Skipped)
	}
	var skipped *BroadcastRecipient
	for i := range res.Recipients {
		if res.Recipients[i].Skipped {
			skipped = &res.Recipients[i]
		}
	}
	if skipped == nil {
		t.Fatal("no skipped recipient reported")
	}
	if skipped.Address != "each:topic:login" || skipped.Reason == "" {
		t.Fatalf("skipped = %+v, want the selector and a reason", *skipped)
	}
	if skipped.ID != "" {
		t.Fatalf("a skipped recipient named the session it may not reach: %q", skipped.ID)
	}
}

// TestBroadcastExplicitRefusedRefusesTheWhole: one explicitly named recipient without permission
// refuses everything, so the sender never believes a letter went out.
func TestBroadcastExplicitRefusedRefusesTheWhole(t *testing.T) {
	f := threeSessionMailbox(t)
	denied := errors.New("another caller's session")
	_, err := f.svc.Broadcast(BroadcastInput{
		To: []string{"session:sess-a", "session:sess-off"}, Body: "群发", Origin: sender(),
		Authorize: func(tg Target) error {
			if tg.ID == "sess-off" {
				return denied
			}
			return nil
		},
	})
	if !errors.Is(err, denied) {
		t.Fatalf("err = %v, want the refusal", err)
	}
	rows, err := f.store.ListMail(jobstore.ListMailOpts{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("%d letters were stored by a refused broadcast", len(rows))
	}
}

// TestBroadcastLimits: the broadcast ceiling (recipients, rate, urgency) refuses before anything
// is stored.
func TestBroadcastLimits(t *testing.T) {
	f := threeSessionMailbox(t)
	f.svc.Limits.BroadcastMaxRecipients = 1
	if _, err := f.svc.Broadcast(BroadcastInput{
		To: []string{"session:sess-a", "session:sess-off"}, Body: "群发", Origin: sender(),
	}); !errors.Is(err, ErrBroadcastTooMany) {
		t.Fatalf("over the recipient ceiling: err = %v, want ErrBroadcastTooMany", err)
	}

	// each: matching nobody is a 400, not an empty broadcast.
	if _, err := f.svc.Broadcast(BroadcastInput{
		To: []string{"each:topic:nothing-matches"}, Body: "群发", Origin: sender(),
	}); !errors.Is(err, ErrNoRecipients) {
		t.Fatalf("no recipient: err = %v, want ErrNoRecipients", err)
	}

	f.svc.Limits.BroadcastMaxRecipients = 20
	if _, err := f.svc.Broadcast(BroadcastInput{
		To: []string{"session:sess-a", "session:sess-off"}, Body: "急件", Urgent: true, Origin: sender(),
	}); !errors.Is(err, ErrUrgentBroadcast) {
		t.Fatalf("urgent broadcast: err = %v, want ErrUrgentBroadcast", err)
	}

	f.svc.Limits.BroadcastRate = 1
	f.svc.Limits.BroadcastWindow = 10 * time.Minute
	if _, err := f.svc.Broadcast(BroadcastInput{
		To: []string{"session:sess-a", "session:sess-off"}, Body: "第一次群发", Origin: sender(),
	}); err != nil {
		t.Fatalf("first broadcast: %v", err)
	}
	if _, err := f.svc.Broadcast(BroadcastInput{
		To: []string{"session:sess-a", "session:sess-off"}, Body: "第二次群发", Origin: sender(),
	}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("second broadcast: err = %v, want ErrRateLimited", err)
	}
	// The window is one broadcast per window, not one per copy.
	if n, err := f.store.CountBroadcastFrom(sender().Kind, sender().ID, sender().Caller, 0); err != nil || n != 1 {
		t.Fatalf("broadcasts counted = %d, %v; want 1", n, err)
	}
}

// TestBroadcastEachProjectAndAgent: the two new selectors fan out to a project's / an agent's
// deliverable sessions.
func TestBroadcastEachProjectAndAgent(t *testing.T) {
	f := newFixture(t)
	for _, s := range []struct{ sid, agent, project, state string }{
		{"sess-p1-a", "claude", "proj-one", jobstore.SessionRunning},
		{"sess-p1-b", "suga", "proj-one", jobstore.SessionIdle},
		{"sess-p2", "claude", "proj-two", jobstore.SessionRunning},
		{"sess-ended-p1", "claude", "proj-one", jobstore.SessionEnded},
	} {
		if _, err := f.store.UpsertAgentSession(jobstore.AgentSession{
			SessionID: s.sid, Agent: s.agent, CallerID: "alice", ProjectKey: s.project, State: s.state,
		}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := f.svc.Broadcast(BroadcastInput{To: []string{"each:project:proj-one"}, Body: "群发", Origin: sender()})
	if err != nil {
		t.Fatalf("each:project: %v", err)
	}
	if len(res.Mails) != 2 {
		t.Fatalf("project broadcast delivered %d, want 2 (the ended session is skipped)", len(res.Mails))
	}
	res, err = f.svc.Broadcast(BroadcastInput{To: []string{"each:agent:suga"}, Body: "群发", Origin: sender()})
	if err != nil {
		t.Fatalf("each:agent: %v", err)
	}
	if len(res.Mails) != 1 || res.Recipients[0].ID != "sess-p1-b" {
		t.Fatalf("agent broadcast = %+v, want sess-p1-b only", res.Recipients)
	}
}

// TestBroadcastEachProjectPagesThroughAllSessions: an each:project: selector reads EVERY page of
// live sessions, not just the first one (gofer-ndy5: a project with more than one page of
// sessions used to lose the tail).
func TestBroadcastEachProjectPagesThroughAllSessions(t *testing.T) {
	f := newFixture(t)
	const n = eachScanLimit + 60
	want := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		sid := fmt.Sprintf("sess-bulk-%03d", i)
		want[sid] = true
		if _, err := f.store.UpsertAgentSession(jobstore.AgentSession{
			SessionID: sid, Agent: "claude", CallerID: "alice", ProjectKey: "proj-bulk",
			State: jobstore.SessionIdle,
		}); err != nil {
			t.Fatal(err)
		}
	}
	f.svc.SetBroadcastLimits(n+10, 5, 10*time.Minute)
	res, err := f.svc.Preview(BroadcastInput{To: []string{"each:project:proj-bulk"}, Body: "群发", Origin: sender()})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if res.Matched != n || len(res.Recipients) != n {
		t.Fatalf("matched=%d recipients=%d, want %d (every page)", res.Matched, len(res.Recipients), n)
	}
	got := make(map[string]bool, n)
	for _, rc := range res.Recipients {
		got[rc.ID] = true
	}
	for sid := range want {
		if !got[sid] {
			t.Fatalf("session %s of the last page was dropped", sid)
		}
	}
}

// TestBroadcastPreviewStoresNothing: the preview answers the same table a send would write and
// stores no letter (nor counts as a broadcast for the rate limit).
func TestBroadcastPreviewStoresNothing(t *testing.T) {
	f := threeSessionMailbox(t)
	res, err := f.svc.Preview(BroadcastInput{
		To: []string{"session:sess-a", "session:sess-off"}, Body: "预览", Origin: sender(),
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if res.Matched != 2 || res.Skipped != 0 || len(res.Recipients) != 2 {
		t.Fatalf("preview = %+v, want 2 recipients", res)
	}
	for _, rc := range res.Recipients {
		if rc.MailID != 0 || rc.ID == "" {
			t.Fatalf("preview recipient = %+v, want an id and no stored letter", rc)
		}
	}
	mails, err := f.store.ListMail(jobstore.ListMailOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(mails) != 0 {
		t.Fatalf("preview stored %d letters, want none", len(mails))
	}
}

// TestBroadcastNeedsExplicitEach: several matches without each: still park the letter, exactly as
// before the broadcast existed.
func TestBroadcastNeedsExplicitEach(t *testing.T) {
	f := newFixture(t)
	f.addSession(t, "sess-a", "claude", "login 页面", jobstore.SessionRunning)
	f.addSession(t, "sess-b", "claude", "login 修复", jobstore.SessionRunning)
	res, err := f.svc.Send(SendInput{To: "topic:login", Body: "单发", Origin: sender()})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !res.Held || len(res.Candidates) != 2 {
		t.Fatalf("a multi-match send without each: = held %v with %d candidates, want a held letter with 2",
			res.Held, len(res.Candidates))
	}
}

// TestBroadcastReplyStaysInThread: a recipient's answer goes back to the sender inside the same
// thread (设计「D3」: no reply-all).
func TestBroadcastReplyStaysInThread(t *testing.T) {
	f := threeSessionMailbox(t)
	res, err := f.svc.Broadcast(BroadcastInput{
		To: []string{"session:sess-a", "session:sess-off"}, Body: "群发", Origin: sender(),
	})
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	answer, err := f.svc.Reply(ReplyInput{
		ID: res.Mails[0].ID, Body: "收到",
		Origin: Origin{Kind: jobstore.MailKindSession, ID: "sess-a", Caller: "alice"},
	})
	if err != nil {
		t.Fatalf("reply: %v", err)
	}
	if answer.Mail.ThreadID != res.ThreadID {
		t.Fatalf("answer thread = %q, want the broadcast thread %q", answer.Mail.ThreadID, res.ThreadID)
	}
	if answer.Target.Kind != jobstore.MailKindSession || answer.Target.ID != sender().ID {
		t.Fatalf("answer goes to %+v, want the sender session %s", answer.Target, sender().ID)
	}
	// Only the copy that was answered is marked: the other recipient's copy is untouched.
	other, _, err := f.store.GetMail(res.Mails[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if other.State != jobstore.MailPending {
		t.Fatalf("the other copy state = %s, want pending", other.State)
	}
}

// TestBroadcastAddressParsing: the each: prefix and the two new selectors.
func TestBroadcastAddressParsing(t *testing.T) {
	for _, raw := range []string{"each:topic:login", "each:path:inhere-tools/gofer", "each:project:gofer", "each:agent:suga"} {
		a, err := ParseAddress(raw)
		if err != nil || !a.Each {
			t.Fatalf("%s: addr=%+v err=%v, want an each: address", raw, a, err)
		}
	}
	for _, raw := range []string{"each:session:sess-a", "each:job:j1", "each:caller", "project:gofer"} {
		if _, err := ParseAddress(raw); err == nil {
			t.Fatalf("%s must be refused", raw)
		}
	}
	// A plain address keeps each=false.
	if a, err := ParseAddress("topic:login"); err != nil || a.Each {
		t.Fatalf("topic:login = %+v, %v; want a plain address", a, err)
	}
}

// TestBroadcastTagShowsGroup: the injected tag of a copy names the group and the recipient count,
// and a single letter's tag stays exactly as before.
func TestBroadcastTagShowsGroup(t *testing.T) {
	tag := RenderLetter(Letter{ID: 5, From: "claude 会话 sess-a", BroadcastID: 5, Recipients: 3, Body: "hi"})
	if !strings.Contains(tag, `broadcast="b5"`) || !strings.Contains(tag, `recipients="3"`) {
		t.Fatalf("broadcast tag = %s, want the group and the count", tag)
	}
	if single := RenderLetter(Letter{ID: 6, Body: "hi"}); strings.Contains(single, "broadcast=") {
		t.Fatalf("a single letter's tag carries broadcast attributes: %s", single)
	}
}

// mailRows is a helper for the "nothing stored" assertions.
func mailRows(t *testing.T, f *fixture) int {
	t.Helper()
	rows, err := f.store.ListMail(jobstore.ListMailOpts{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// TestBroadcastRejectedStoresNothing keeps the "leave no trace" promise for every refusal.
func TestBroadcastRejectedStoresNothing(t *testing.T) {
	f := newFixture(t)
	f.addSession(t, "sess-a", "claude", "login 页面", jobstore.SessionRunning)
	if _, err := f.svc.Broadcast(BroadcastInput{To: []string{"session:sess-a"}, Body: " ", Origin: sender()}); err == nil {
		t.Fatal("an empty body must be refused")
	}
	if n := mailRows(t, f); n != 0 {
		t.Fatalf("%d letters stored by refused broadcasts", n)
	}
}

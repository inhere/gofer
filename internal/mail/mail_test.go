package mail

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
)

// M2 of the mailbox (design 2026-10-16 §「信箱」): sending, address resolution, limits, the
// capture box and replies.

type fixture struct {
	svc   *Service
	store *jobstore.Store
	relay *sessionrelay.Service
	now   time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store, err := jobstore.Open(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	relay := sessionrelay.NewService(store)
	f := &fixture{store: store, relay: relay, now: time.Unix(1_760_000_000, 0)}
	// The store's clock too: created_at is what the rate windows count.
	store.SetClock(func() time.Time { return f.now })
	f.svc = NewService(store, relay)
	f.svc.SetClock(func() time.Time { return f.now })
	// An ended session: a known address that nobody can be handed a letter at (a held letter).
	f.addSession(t, "sess-ended", "claude", "", jobstore.SessionEnded)
	return f
}

// addSession registers one session with a declared topic and scope.
func (f *fixture) addSession(t *testing.T, sid, agent, topic, state string, scope ...string) {
	t.Helper()
	if _, err := f.store.UpsertAgentSession(jobstore.AgentSession{
		SessionID: sid, Agent: agent, CallerID: "alice", State: state,
	}); err != nil {
		t.Fatal(err)
	}
	if topic == "" && len(scope) == 0 {
		return
	}
	if _, err := f.relay.SetTopic(sid, &topic, &scope, jobstore.SessionTopicSelf); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) addJob(t *testing.T, id, status string) {
	t.Helper()
	if err := f.store.UpsertJob(jobstore.JobRecord{
		ID: id, ProjectKey: "gofer", Agent: "claude", Runner: "local", Status: status,
		ResultDir: filepath.Join(t.TempDir(), id), StartedAt: f.now.Unix(), UpdatedAt: f.now.Unix(),
	}); err != nil {
		t.Fatal(err)
	}
}

// threeSessionMailbox is the fixture every address test starts from: two live sessions with
// distinct topics and scopes, plus an OFFLINE one (it takes part in resolution and is flagged).
func threeSessionMailbox(t *testing.T) *fixture {
	f := newFixture(t)
	f.addSession(t, "sess-a", "claude", "gofer 开发", jobstore.SessionRunning, "inhere-tools/gofer")
	f.addSession(t, "sess-b", "suga", "suga 开发", jobstore.SessionIdle, "inhere-tools/suga")
	f.addSession(t, "sess-off", "codex", "gofer 部署", jobstore.SessionOffline, "inhere-tools/deploy")
	return f
}

func sender() Origin {
	return Origin{Kind: jobstore.MailKindSession, ID: "sess-b", Caller: "alice"}
}

func (f *fixture) item(t *testing.T, id string) jobstore.WorkItem {
	t.Helper()
	w, ok, err := f.store.GetWorkItem(id)
	if err != nil || !ok {
		t.Fatalf("capture item %q: ok=%v err=%v", id, ok, err)
	}
	return w
}

func TestSendDirectAddresses(t *testing.T) {
	f := threeSessionMailbox(t)
	f.addJob(t, "job-live", "running")
	f.addJob(t, "job-done", "done")

	res, err := f.svc.Send(SendInput{To: "session:sess-a", Body: "hello", Origin: sender()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Held || res.Mail.State != jobstore.MailPending {
		t.Fatalf("session: send = %+v", res)
	}
	if res.Target.Kind != jobstore.MailKindSession || res.Target.ID != "sess-a" || res.Target.Offline {
		t.Fatalf("session target = %+v", res.Target)
	}
	if res.Mail.ToAddress != "session:sess-a" || res.Mail.ToKind != jobstore.MailKindSession || res.Mail.ToID != "sess-a" {
		t.Fatalf("stored letter = %+v", res.Mail)
	}
	if res.Mail.ThreadID != mailThreadIDOf(res.Mail.ID) {
		t.Fatalf("a root letter's thread = %q", res.Mail.ThreadID)
	}
	if want := f.now.Unix() + int64(jobstore.DefaultMailDeliverBy/time.Second); res.Mail.DeliverBy != want {
		t.Fatalf("deliver_by=%d want %d", res.Mail.DeliverBy, want)
	}
	if n, err := f.store.PendingCount(jobstore.MailKindSession, "sess-a"); err != nil || n != 1 {
		t.Fatalf("pending count n=%d err=%v", n, err)
	}

	job, err := f.svc.Send(SendInput{To: "job:job-live", Body: "status?", Origin: sender()})
	if err != nil {
		t.Fatal(err)
	}
	if job.Held || job.Target.Kind != jobstore.MailKindJob || job.Mail.JobID != "job-live" {
		t.Fatalf("job: send = %+v", job)
	}
	// An ended job is not a delivery target: the letter is parked instead.
	dead, err := f.svc.Send(SendInput{To: "job:job-done", Body: "status?", Origin: sender()})
	if err != nil {
		t.Fatal(err)
	}
	if !dead.Held || dead.Mail.State != jobstore.MailHeld || dead.CaptureItemID == "" {
		t.Fatalf("job: to an ended job = %+v", dead)
	}
	unknown, err := f.svc.Send(SendInput{To: "session:sess-ended", Body: "anyone?", Origin: sender()})
	if err != nil {
		t.Fatal(err)
	}
	if !unknown.Held || len(unknown.Candidates) != 0 || unknown.Mail.ToKind != "" {
		t.Fatalf("session: to an ended session = %+v", unknown)
	}
}

func TestSendTopicAddressUniqueAmbiguousAndOffline(t *testing.T) {
	f := threeSessionMailbox(t)

	unique, err := f.svc.Send(SendInput{To: "topic:suga", Body: "ping", Origin: sender()})
	if err != nil {
		t.Fatal(err)
	}
	if unique.Held || unique.Target.ID != "sess-b" || unique.Target.Topic != "suga 开发" || unique.Target.Offline {
		t.Fatalf("unique topic target = %+v", unique)
	}

	// "gofer" matches the live session AND the offline one: the design does not guess.
	ambiguous, err := f.svc.Send(SendInput{To: "topic:gofer", Body: "who owns this?", Origin: sender()})
	if err != nil {
		t.Fatal(err)
	}
	if !ambiguous.Held || len(ambiguous.Candidates) != 2 || ambiguous.CaptureItemID == "" {
		t.Fatalf("ambiguous topic = %+v", ambiguous)
	}
	seenOffline := false
	for _, c := range ambiguous.Candidates {
		if c.ID == "sess-off" && c.Offline {
			seenOffline = true
		}
		if len(c.Why) == 0 {
			t.Fatalf("candidate %+v has no reason", c)
		}
	}
	if !seenOffline {
		t.Fatalf("the offline session was not among the candidates: %+v", ambiguous.Candidates)
	}
	text := f.item(t, ambiguous.CaptureItemID).Title
	if !strings.Contains(text, "邮件 #") || !strings.Contains(text, "topic:gofer") {
		t.Fatalf("capture item title %q does not reference the letter", text)
	}

	// An offline session is still a recipient when it is the ONLY match; it is flagged.
	offline, err := f.svc.Send(SendInput{To: "topic:部署", Body: "deploy?", Origin: sender()})
	if err != nil {
		t.Fatal(err)
	}
	if offline.Held || offline.Target.ID != "sess-off" || !offline.Target.Offline {
		t.Fatalf("offline-only topic target = %+v", offline)
	}

	// An inferred topic never takes part in delivery: a session with no declared topic is not
	// a topic: recipient at all.
	if _, err := f.store.UpsertAgentSession(jobstore.AgentSession{SessionID: "sess-no-topic", Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	none, err := f.svc.Send(SendInput{To: "topic:sess-no-topic", Body: "hello?", Origin: sender()})
	if err != nil {
		t.Fatal(err)
	}
	if !none.Held {
		t.Fatalf("an inferred topic matched: %+v", none)
	}

	// The reader-permission judgement is the caller's: a hidden session is not a candidate.
	hidden, err := f.svc.Send(SendInput{
		To: "topic:suga", Body: "ping", Origin: sender(),
		Visible: func(jobstore.AgentSession) bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hidden.Held || len(hidden.Candidates) != 0 {
		t.Fatalf("a session the reader may not see was used: %+v", hidden)
	}
}

func TestSendPathAddressNeedsAUniqueStrongHit(t *testing.T) {
	f := threeSessionMailbox(t)

	strong, err := f.svc.Send(SendInput{To: "path:inhere-tools/gofer", Body: "yours?", Origin: sender()})
	if err != nil {
		t.Fatal(err)
	}
	if strong.Held || strong.Target.ID != "sess-a" {
		t.Fatalf("path: strong hit = %+v", strong)
	}

	// The query is an ANCESTOR of both scopes: weak hits only, not enough to deliver on.
	weak, err := f.svc.Send(SendInput{To: "path:inhere-tools", Body: "yours?", Origin: sender()})
	if err != nil {
		t.Fatal(err)
	}
	if !weak.Held || len(weak.Candidates) != 3 {
		t.Fatalf("path: weak hits = %+v", weak)
	}

	unknown, err := f.svc.Send(SendInput{To: "path:inhere-tools/nothing", Body: "yours?", Origin: sender()})
	if err != nil {
		t.Fatal(err)
	}
	if !unknown.Held || len(unknown.Candidates) != 0 {
		t.Fatalf("path: unknown = %+v", unknown)
	}
}

func TestSendToTheCallerGoesToTheCaptureBox(t *testing.T) {
	f := threeSessionMailbox(t)
	for _, to := range []string{"caller", "", "   "} {
		res, err := f.svc.Send(SendInput{To: to, Body: "note to self", Origin: sender()})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Held || res.Mail.State != jobstore.MailHeld || res.CaptureItemID == "" {
			t.Fatalf("to %q = %+v", to, res)
		}
		if res.Target.Kind != jobstore.MailKindCaller || res.Mail.ToCaller != "alice" {
			t.Fatalf("to %q target = %+v mail=%+v", to, res.Target, res.Mail)
		}
		if res.Mail.ToKind != jobstore.MailKindCaller {
			t.Fatalf("to %q stored kind = %q", to, res.Mail.ToKind)
		}
	}
	// The item and the letter reference each other.
	held, err := f.store.ListMail(jobstore.ListMailOpts{States: []string{jobstore.MailHeld}})
	if err != nil || len(held) != 3 {
		t.Fatalf("held letters=%d err=%v", len(held), err)
	}
	for _, m := range held {
		if m.CaptureItemID == "" {
			t.Fatalf("held letter without a capture item: %+v", m)
		}
		item := f.item(t, m.CaptureItemID)
		if item.Status != jobstore.WorkCaptured || item.Kind != captureKind {
			t.Fatalf("item %+v is not a captured %s item", item, captureKind)
		}
		if !strings.Contains(item.Title, "邮件 #"+itoa(m.ID)) {
			t.Fatalf("item title %q does not name letter %d", item.Title, m.ID)
		}
	}
}

// TestSendAgentAddressNeedsConfiguration: `agent:` parses now (设计 2026-10-22「D1」); a name that
// the mailbox does not configure is refused before anything is stored.
func TestSendAgentAddressNeedsConfiguration(t *testing.T) {
	f := threeSessionMailbox(t)
	if a, err := ParseAddress("agent:codex"); err != nil || a.Scheme != SchemeAgent || a.Value != "codex" {
		t.Fatalf("ParseAddress(agent:codex) = %+v, %v", a, err)
	}
	if _, err := f.svc.Send(SendInput{To: "agent:codex", Body: "hi", Origin: sender()}); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("Send(agent:codex) err=%v, want ErrUnknownAgent", err)
	}
	if _, err := f.svc.Send(SendInput{To: "agent:", Body: "hi", Origin: sender()}); !errors.Is(err, ErrInvalidAddress) {
		t.Fatalf("Send(agent:) with no name err=%v, want ErrInvalidAddress", err)
	}
	for _, bad := range []string{"session:", "job:", "topic:", "path:", "ghost:x", "caller:me"} {
		if _, err := ParseAddress(bad); !errors.Is(err, ErrInvalidAddress) {
			t.Fatalf("ParseAddress(%q) err=%v, want ErrInvalidAddress", bad, err)
		}
	}
}

func TestSendOriginValidation(t *testing.T) {
	f := threeSessionMailbox(t)
	cases := []struct {
		name string
		org  Origin
	}{
		{"no kind", Origin{ID: "sess-a"}},
		{"unknown kind", Origin{Kind: "ghost", ID: "x"}},
		{"session without id", Origin{Kind: jobstore.MailKindSession, Caller: "alice"}},
		{"job without id", Origin{Kind: jobstore.MailKindJob, Caller: "alice"}},
		{"caller with an id", Origin{Kind: jobstore.MailKindCaller, ID: "x", Caller: "alice"}},
	}
	for _, tc := range cases {
		if _, err := f.svc.Send(SendInput{To: "session:sess-a", Body: "hi", Origin: tc.org}); !errors.Is(err, ErrInvalidOrigin) {
			t.Fatalf("%s: err=%v, want ErrInvalidOrigin", tc.name, err)
		}
	}
	// The caller (web / CLI) origin is legal, and a session / job origin stamps the letter.
	caller, err := f.svc.Send(SendInput{
		To: "session:sess-a", Body: "hi", Origin: Origin{Kind: jobstore.MailKindCaller, Caller: "alice"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if caller.Mail.FromKind != jobstore.MailKindCaller || caller.Mail.FromCaller != "alice" || caller.Mail.FromID != "" {
		t.Fatalf("caller letter = %+v", caller.Mail)
	}
	if got := OriginOf(caller.Mail).Display(); got != "调用方 alice" {
		t.Fatalf("caller display = %q", got)
	}
	fromJob, err := f.svc.Send(SendInput{
		To: "session:sess-a", Body: "hi", Origin: Origin{Kind: jobstore.MailKindJob, ID: "job-7", Caller: "alice"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fromJob.Mail.JobID != "job-7" {
		t.Fatalf("job sender did not stamp job_id: %+v", fromJob.Mail)
	}
	if got := OriginOf(fromJob.Mail).Display(); got != "job job-7（调用方 alice）" {
		t.Fatalf("job display = %q", got)
	}
}

func TestSendLimits(t *testing.T) {
	f := threeSessionMailbox(t)
	long := strings.Repeat("x", 32*1024+1)
	if _, err := f.svc.Send(SendInput{To: "session:sess-a", Body: long, Origin: sender()}); !errors.Is(err, ErrMailTooLong) {
		t.Fatalf("oversized body err=%v, want ErrMailTooLong", err)
	}
	if _, err := f.svc.Send(SendInput{To: "session:sess-a", Body: "  \n ", Origin: sender()}); !errors.Is(err, ErrInvalidBody) {
		t.Fatalf("empty body err=%v, want ErrInvalidBody", err)
	}

	f.svc.Limits.PerMinute, f.svc.Limits.PerHour = 2, 0
	for i := 0; i < 2; i++ {
		if _, err := f.svc.Send(SendInput{To: "session:sess-a", Body: "hi", Origin: sender()}); err != nil {
			t.Fatalf("send %d err=%v", i, err)
		}
	}
	err := func() error {
		_, err := f.svc.Send(SendInput{To: "session:sess-a", Body: "hi", Origin: sender()})
		return err
	}()
	if !errors.Is(err, ErrRateLimited) || !IsRateLimited(err) {
		t.Fatalf("third send in the window err=%v, want ErrRateLimited", err)
	}
	// A different sender has its own window.
	other := Origin{Kind: jobstore.MailKindSession, ID: "sess-a", Caller: "alice"}
	if _, err := f.svc.Send(SendInput{To: "session:sess-a", Body: "hi", Origin: other}); err != nil {
		t.Fatalf("another session is under the same limit: %v", err)
	}
	// The minute window slides.
	f.now = f.now.Add(61 * time.Second)
	if _, err := f.svc.Send(SendInput{To: "session:sess-a", Body: "hi", Origin: sender()}); err != nil {
		t.Fatalf("send after the window err=%v", err)
	}

	// The hourly window is enforced on its own.
	hourly := threeSessionMailbox(t)
	hourly.svc.Limits.PerMinute, hourly.svc.Limits.PerHour = 0, 2
	for i := 0; i < 2; i++ {
		if _, err := hourly.svc.Send(SendInput{To: "session:sess-a", Body: "hi", Origin: sender()}); err != nil {
			t.Fatalf("hourly send %d err=%v", i, err)
		}
	}
	if _, err := hourly.svc.Send(SendInput{To: "session:sess-a", Body: "hi", Origin: sender()}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("hourly ceiling err=%v, want ErrRateLimited", err)
	}
	hourly.now = hourly.now.Add(61 * time.Second) // the minute window is off, the hour one is not
	if _, err := hourly.svc.Send(SendInput{To: "session:sess-a", Body: "hi", Origin: sender()}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("the hour window did not hold err=%v", err)
	}
}

func TestReplyKeepsTheThreadAndAnswersTheOriginal(t *testing.T) {
	f := threeSessionMailbox(t)
	first, err := f.svc.Send(SendInput{To: "session:sess-a", Body: "你改一下 acp", Origin: sender()})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := f.svc.Reply(ReplyInput{
		ID: first.Mail.ID, Body: "收到，下一版适配",
		Origin: Origin{Kind: jobstore.MailKindSession, ID: "sess-a", Caller: "alice"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Held || reply.Mail.State != jobstore.MailPending {
		t.Fatalf("reply = %+v", reply)
	}
	if reply.Mail.ThreadID != first.Mail.ThreadID || reply.Mail.ReplyTo != first.Mail.ID {
		t.Fatalf("reply is not in the same thread: %+v", reply.Mail)
	}
	if reply.Mail.ToKind != jobstore.MailKindSession || reply.Mail.ToID != "sess-b" || reply.Mail.ToAddress != "session:sess-b" {
		t.Fatalf("the reply does not go back to the sender: %+v", reply.Mail)
	}
	orig, _, err := f.store.GetMail(first.Mail.ID)
	if err != nil || orig.State != jobstore.MailAnswered {
		t.Fatalf("the original is not answered: err=%v mail=%+v", err, orig)
	}
	if n, err := f.store.CountThreadMail(orig.ThreadID); err != nil || n != 2 {
		t.Fatalf("thread count n=%d err=%v", n, err)
	}
	if _, err := f.svc.Reply(ReplyInput{ID: 9999, Body: "hi", Origin: sender()}); !errors.Is(err, ErrMailNotFound) {
		t.Fatalf("reply to an unknown letter err=%v, want ErrMailNotFound", err)
	}
}

func TestReplyThreadCeilingParksInTheCaptureBox(t *testing.T) {
	f := threeSessionMailbox(t)
	f.svc.Limits.MaxThreadLetters = 2
	first, err := f.svc.Send(SendInput{To: "session:sess-a", Body: "one", Origin: sender()})
	if err != nil {
		t.Fatal(err)
	}
	one, err := f.svc.Reply(ReplyInput{ID: first.Mail.ID, Body: "two", Origin: Origin{
		Kind: jobstore.MailKindSession, ID: "sess-a", Caller: "alice",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if one.Held {
		t.Fatalf("the reply under the ceiling was parked: %+v", one)
	}
	two, err := f.svc.Reply(ReplyInput{ID: first.Mail.ID, Body: "three", Origin: sender()})
	if err != nil {
		t.Fatal(err)
	}
	if !two.Held || two.Mail.State != jobstore.MailHeld || two.CaptureItemID == "" {
		t.Fatalf("the reply over the ceiling = %+v", two)
	}
	if !strings.Contains(two.Mail.LastError, "ceiling") {
		t.Fatalf("the hold does not say why: %q", two.Mail.LastError)
	}
	if two.Mail.ThreadID != first.Mail.ThreadID || two.Mail.ReplyTo != first.Mail.ID {
		t.Fatalf("a parked reply left its thread: %+v", two.Mail)
	}
	item := f.item(t, two.CaptureItemID)
	if !strings.Contains(item.Title, "three") && !strings.Contains(item.Title, "邮件 #") {
		t.Fatalf("item %q does not point at the letter", item.Title)
	}
}

// TestReplyToACallerLandsInTheCaptureBox: a caller has no session or job to be delivered into,
// so an answer to it is parked exactly like the `caller` address itself.
func TestReplyToACallerLandsInTheCaptureBox(t *testing.T) {
	f := threeSessionMailbox(t)
	from := Origin{Kind: jobstore.MailKindCaller, Caller: "alice"}
	first, err := f.svc.Send(SendInput{To: "session:sess-a", Body: "随手记：目录没锁定", Origin: from})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := f.svc.Reply(ReplyInput{ID: first.Mail.ID, Body: "已记 issue", Origin: Origin{
		Kind: jobstore.MailKindSession, ID: "sess-a", Caller: "alice",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Held || reply.Mail.ToKind != jobstore.MailKindCaller || reply.Mail.ToCaller != "alice" {
		t.Fatalf("reply to a caller = %+v", reply)
	}
	if reply.CaptureItemID == "" {
		t.Fatal("the caller's reply was not recorded in the capture box")
	}
}

// TestCaptureItemKeepsOnlyAReferenceAndASummary: the item never copies the body, only the
// letter's id and as much of its head as the item's own text budget leaves.
func TestCaptureItemKeepsOnlyAReferenceAndASummary(t *testing.T) {
	f := threeSessionMailbox(t)
	body := strings.Repeat("秘密", 300) // 600 runes
	res, err := f.svc.Send(SendInput{To: "session:sess-ended", Body: body, Origin: sender()})
	if err != nil {
		t.Fatal(err)
	}
	if res.CaptureItemID == "" {
		t.Fatal("no capture item")
	}
	item := f.item(t, res.CaptureItemID)
	if len(item.Title) > 200 {
		t.Fatalf("the item text is %d bytes, over the work item's own cap", len(item.Title))
	}
	if !strings.Contains(item.Title, "邮件 #"+itoa(res.Mail.ID)) || !strings.Contains(item.Title, "session:sess-ended") {
		t.Fatalf("the item does not reference the letter: %q", item.Title)
	}
	if !strings.Contains(item.Title, bodyLabel) {
		t.Fatalf("the item carries no body preview: %q", item.Title)
	}
	got := strings.Count(item.Title, "秘密")
	if got == 0 || got > MailSummaryRunes {
		t.Fatalf("the preview is %d runes, want 1..%d: %q", got, MailSummaryRunes, item.Title)
	}
	if got == 300 {
		t.Fatal("the item copied the whole body")
	}
}

// TestSummarizeCutIsRuneSafe covers the boundary the extraction sits on.
func TestSummarizeCutIsRuneSafe(t *testing.T) {
	if got := Summarize("あいうえお", 2); got != "あい…" {
		t.Fatalf("Summarize=%q", got)
	}
	if got := Summarize("abc", 5); got != "abc" {
		t.Fatalf("Summarize short=%q", got)
	}
	if got := Summarize("abc", 0); got != "abc" {
		t.Fatalf("Summarize with 0=%q", got)
	}
}

// itoa is a tiny local shim so the tests do not import strconv for one call.
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// mailThreadIDOf mirrors the store's thread naming for a root letter.
func mailThreadIDOf(id int64) string { return "m" + itoa(id) }

// session: addresses use the same prefix rule as --session: a unique prefix delivers to the full
// id, several matches are ErrSessionAmbiguous naming full ids, none is ErrSessionNotFound.
func TestSessionAddressPrefixResolution(t *testing.T) {
	f := newFixture(t)
	f.addSession(t, "sess_261020_aaaa1111", "suga", "", jobstore.SessionIdle)
	f.addSession(t, "sess_261020_aaaa2222", "suga", "", jobstore.SessionIdle)
	f.addSession(t, "cf0bb835-1111-2222", "claude", "", jobstore.SessionIdle)
	send := func(to string) (SendResult, error) {
		return f.svc.Send(SendInput{To: to, Body: "hi", Origin: Origin{Kind: jobstore.MailKindCaller, Caller: "alice"}})
	}

	res, err := send("session:cf0bb835")
	if err != nil || res.Held || res.Mail.ToID != "cf0bb835-1111-2222" {
		t.Fatalf("unique prefix: %+v %v", res, err)
	}
	if res, err = send("session:sess_261020_aaaa2"); err != nil || res.Mail.ToID != "sess_261020_aaaa2222" {
		t.Fatalf("longer prefix: %+v %v", res, err)
	}
	if res, err = send("session:sess_261020_aaaa1111"); err != nil || res.Mail.ToID != "sess_261020_aaaa1111" {
		t.Fatalf("exact id: %+v %v", res, err)
	}

	_, err = send("session:sess_261")
	if !errors.Is(err, ErrSessionAmbiguous) || !strings.Contains(err.Error(), "sess_261020_aaaa1111") || !strings.Contains(err.Error(), "sess_261020_aaaa2222") {
		t.Fatalf("ambiguous: %v", err)
	}
	_, err = send("session:zzzz")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("not found: %v", err)
	}
	// An ended session still matches by prefix; the letter is held, not "not found".
	if res, err = send("session:sess-end"); err != nil || !res.Held {
		t.Fatalf("ended by prefix: %+v %v", res, err)
	}
	// A session the sender may not see does not count: unique prefix -> not found.
	_, err = f.svc.Send(SendInput{To: "session:cf0bb835", Body: "hi", Origin: Origin{Kind: jobstore.MailKindCaller, Caller: "alice"},
		Visible: func(a jobstore.AgentSession) bool { return a.SessionID != "cf0bb835-1111-2222" }})
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("invisible: %v", err)
	}
	// Many matches: at most maxAmbiguousIDs full ids are listed.
	for i := 0; i < 12; i++ {
		f.addSession(t, fmt.Sprintf("many_%02d", i), "suga", "", jobstore.SessionIdle)
	}
	_, err = send("session:many_")
	if !errors.Is(err, ErrSessionAmbiguous) || !strings.Contains(err.Error(), "(+4 more)") {
		t.Fatalf("capped list: %v", err)
	}
}

// Cancelling a held letter drops the capture-box item it was recorded as; a cancelled letter
// that was not held, or an item a person already triaged, is left alone.
func TestCancelHeldLetterDropsItsCaptureItem(t *testing.T) {
	f := newFixture(t)
	org := Origin{Kind: jobstore.MailKindCaller, Caller: "alice"}
	held, err := f.svc.Send(SendInput{To: "session:sess-ended", Body: "x", Origin: org})
	if err != nil || !held.Held || held.CaptureItemID == "" {
		t.Fatalf("setup: %+v %v", held, err)
	}
	if _, changed, err := f.svc.CancelMail(held.Mail.ID, "cancelled by alice"); err != nil || !changed {
		t.Fatalf("cancel: changed=%v err=%v", changed, err)
	}
	w, ok, err := f.store.GetWorkItem(held.CaptureItemID)
	if err != nil || !ok || w.Status != jobstore.WorkDropped {
		t.Fatalf("item after cancel = %+v ok=%v err=%v, want dropped", w, ok, err)
	}
	j, _ := f.store.ListWorkJournal(held.CaptureItemID, 10, 0)
	found := false
	for _, e := range j {
		found = found || strings.Contains(e.Text, "已撤回")
	}
	if !found {
		t.Fatalf("journal lacks the withdrawal line: %+v", j)
	}

	// A second cancel changes nothing.
	if _, changed, _ := f.svc.CancelMail(held.Mail.ID, "again"); changed {
		t.Fatal("cancelling twice must not change anything")
	}

	// An item already moved on by a person stays as they left it.
	held2, _ := f.svc.Send(SendInput{To: "session:sess-ended", Body: "y", Origin: org})
	parked := jobstore.WorkParked
	src := jobstore.WorkSourceHuman
	if _, _, err := f.store.UpdateWorkItem(held2.CaptureItemID, jobstore.WorkItemPatch{Status: &parked, StatusSource: &src, FromCapture: true}, 0, "me"); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := f.svc.CancelMail(held2.Mail.ID, "c"); err != nil || !changed {
		t.Fatal(err)
	}
	if w, _, _ := f.store.GetWorkItem(held2.CaptureItemID); w.Status != jobstore.WorkParked {
		t.Fatalf("triaged item status = %s, want parked", w.Status)
	}

	// Readdressing a held letter closes its item too.
	f.addSession(t, "sess-a", "claude", "", jobstore.SessionIdle)
	held3, _ := f.svc.Send(SendInput{To: "session:sess-ended", Body: "z", Origin: org})
	if _, err := f.svc.Readdress(ReaddressInput{ID: held3.Mail.ID, To: "session:sess-a", Origin: org}); err != nil {
		t.Fatal(err)
	}
	if w, _, _ := f.store.GetWorkItem(held3.CaptureItemID); w.Status != jobstore.WorkDropped {
		t.Fatalf("readdressed item status = %s, want dropped", w.Status)
	}
}

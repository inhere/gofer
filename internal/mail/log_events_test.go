package mail

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
)

// The mailbox's log events (log.go / wake_wait.go): every test drives the real call path
// (Send / Tick / the deliverer's own sweep) and only looks at what reached the log, so a
// silent call site fails here.

type logRec struct {
	msg   string
	attrs map[string]string
}

func (r logRec) attr(k string) string { return r.attrs[k] }

// logRecorder captures slog records (message + attributes).
type logRecorder struct {
	mu      sync.Mutex
	records []logRec
}

func newLogRecorder(t *testing.T) *logRecorder {
	t.Helper()
	rec := &logRecorder{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return rec
}

func (r *logRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *logRecorder) Handle(_ context.Context, rec slog.Record) error {
	attrs := make(map[string]string, rec.NumAttrs())
	rec.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	r.mu.Lock()
	r.records = append(r.records, logRec{msg: rec.Message, attrs: attrs})
	r.mu.Unlock()
	return nil
}

func (r *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *logRecorder) WithGroup(string) slog.Handler      { return r }

// find returns the records of one event.
func (r *logRecorder) find(event string) []logRec {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []logRec
	for _, rec := range r.records {
		if rec.msg == event {
			out = append(out, rec)
		}
	}
	return out
}

// text is everything the logger was given, for "a body never reaches the log" assertions.
func (r *logRecorder) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, rec := range r.records {
		b.WriteString(rec.msg)
		for k, v := range rec.attrs {
			b.WriteString(" " + k + "=" + v)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (r *logRecorder) reset() {
	r.mu.Lock()
	r.records = nil
	r.mu.Unlock()
}

// TestSendLogsSentWithoutTheBody: a letter's first line answers 「何时发出、发给谁」 and the
// body itself never reaches the log (SR1301).
func TestSendLogsSentWithoutTheBody(t *testing.T) {
	f := newFixture(t)
	f.addSession(t, "sess-a", "claude", "", jobstore.SessionRunning)
	rec := newLogRecorder(t)
	const body = "private-payload-9f3"
	res, err := f.svc.Send(SendInput{To: "session:sess-a", Body: body, Urgent: true, Origin: callerOrigin()})
	if err != nil || res.Held {
		t.Fatalf("send = %+v err=%v", res, err)
	}
	sent := rec.find(EventSent)
	if len(sent) != 1 {
		t.Fatalf("mail.sent lines = %d, want 1: %s", len(sent), rec.text())
	}
	got := sent[0]
	if got.attr("mail_id") != strconv.FormatInt(res.Mail.ID, 10) || got.attr("to_kind") != jobstore.MailKindSession ||
		got.attr("to_id") != "sess-a" || got.attr("from_kind") != jobstore.MailKindCaller ||
		got.attr("urgent") != "true" || got.attr("thread") != res.Mail.ThreadID {
		t.Fatalf("mail.sent = %+v", got.attrs)
	}
	if strings.Contains(rec.text(), body) {
		t.Fatalf("the letter body reached the log: %s", rec.text())
	}
}

// TestHeldLetterLogsTheReason: a letter nobody can be handed to says why it is parked.
func TestHeldLetterLogsTheReason(t *testing.T) {
	f := newFixture(t)
	rec := newLogRecorder(t)
	res, err := f.svc.Send(SendInput{To: "session:sess-ended", Body: "x", Origin: callerOrigin()})
	if err != nil || !res.Held {
		t.Fatalf("send = %+v err=%v, want held", res, err)
	}
	held := rec.find(EventHeld)
	if len(held) != 1 || held[0].attr("mail_id") != strconv.FormatInt(res.Mail.ID, 10) ||
		held[0].attr("reason") == "" || held[0].attr("to_address") != "session:sess-ended" {
		t.Fatalf("mail.held = %+v (%d lines)", held, len(held))
	}
}

// TestSugaDeliveryLogsPathAndLatency: the body handed to a suga session reports the path that
// carried it and how long the letter waited.
func TestSugaDeliveryLogsPathAndLatency(t *testing.T) {
	f := newFixture(t)
	f.sessionSeenAt(t, "sess-suga", "suga", jobstore.SessionRunning, time.Minute)
	const body = "build-private-4a1"
	m := f.send(t, "session:sess-suga", body, false)
	f.now = f.now.Add(90 * time.Second)
	rec := newLogRecorder(t)
	d, _, _ := newDeliverer(f)

	d.Tick(context.Background())
	got := rec.find(EventDelivered)
	if len(got) != 1 {
		t.Fatalf("mail.delivered lines = %d: %s", len(got), rec.text())
	}
	if got[0].attr("mail_id") != strconv.FormatInt(m.ID, 10) || got[0].attr("path") != PathSugaSend ||
		got[0].attr("session") != "sess-suga" || got[0].attr("latency_sec") != "90" {
		t.Fatalf("mail.delivered = %+v", got[0].attrs)
	}
	if strings.Contains(rec.text(), body) {
		t.Fatalf("the letter body reached the log: %s", rec.text())
	}
}

// TestSugaFailureLogsTheReleasedClaim: a send that did not land hands the claim back and says
// why, so "why is the letter still pending" is answerable.
func TestSugaFailureLogsTheReleasedClaim(t *testing.T) {
	f := newFixture(t)
	f.sessionSeenAt(t, "sess-suga", "suga", jobstore.SessionIdle, time.Minute)
	f.send(t, "session:sess-suga", "hi", false)
	rec := newLogRecorder(t)
	d, snd, _ := newDeliverer(f)
	snd.sendErr = &sessionrelay.UndeliverableError{Reason: sessionrelay.ReasonNotRunning}

	d.Tick(context.Background())
	released := rec.find(EventClaimReleased)
	if len(released) != 1 || released[0].attr("reason") != "session not online" {
		t.Fatalf("mail.claim_released = %+v (%d)", released, len(released))
	}
	if len(rec.find(EventDelivered)) != 0 {
		t.Fatalf("a failed send was logged as delivered: %s", rec.text())
	}
}

// TestJobSayAndEndedJobLogs: the ACP job path reports where the letter went, and a job that
// ended while it waited parks the letter and bounces it.
func TestJobSayAndEndedJobLogs(t *testing.T) {
	f := newFixture(t)
	f.setJob(t, "job-acp", job.StatusAwaitingInput, false)
	m := f.send(t, "job:job-acp", "hi", false)
	rec := newLogRecorder(t)
	d, _, _ := newDeliverer(f)
	d.Tick(context.Background())
	got := rec.find(EventDelivered)
	if len(got) != 1 || got[0].attr("path") != PathJobSay || got[0].attr("job_id") != "job-acp" {
		t.Fatalf("job say delivery = %+v (%d)", got, len(got))
	}
	if got[0].attr("mail_id") != strconv.FormatInt(m.ID, 10) {
		t.Fatalf("delivered letter id = %q, want %d", got[0].attr("mail_id"), m.ID)
	}

	// A letter stored for a job that ended meanwhile: parked + returned to its sender.
	f.setJob(t, "job-gone", job.StatusDone, false)
	if _, err := f.store.InsertMail(jobstore.MailInput{
		FromKind: jobstore.MailKindCaller, FromCaller: "alice",
		ToAddress: "job:job-gone", ToKind: jobstore.MailKindJob, ToID: "job-gone", Body: "late",
	}); err != nil {
		t.Fatal(err)
	}
	rec.reset()
	d.Tick(context.Background())
	if held := rec.find(EventHeld); len(held) != 1 || !strings.Contains(held[0].attr("reason"), job.StatusDone) {
		t.Fatalf("mail.held = %+v (%d): %s", held, len(held), rec.text())
	}
	if bounced := rec.find(EventBounced); len(bounced) != 1 || bounced[0].attr("to_kind") != jobstore.MailKindCaller ||
		bounced[0].attr("original_mail_id") == "" {
		t.Fatalf("mail.bounced = %+v (%d): %s", bounced, len(bounced), rec.text())
	}
}

// TestWakeWaitingOncePerReasonAndCleanup: a session with pending letters that is NOT woken is
// reported when the wait starts and when the reason changes — never once per sweep — and the
// memory drops the session as soon as its letters are gone.
func TestWakeWaitingOncePerReasonAndCleanup(t *testing.T) {
	f := newFixture(t)
	f.sessionSeenAt(t, "sess-idle", "claude", jobstore.SessionIdle, time.Minute)
	m := f.send(t, "session:sess-idle", "hi", false)
	rec := newLogRecorder(t)
	d, snd, _ := newDeliverer(f)
	ctx := context.Background()

	d.Tick(ctx)
	waiting := rec.find(EventWakeWaiting)
	if len(waiting) != 1 || waiting[0].attr("reason") != WaitNotIdleLongEnough ||
		waiting[0].attr("session") != "sess-idle" || waiting[0].attr("pending") != "1" {
		t.Fatalf("mail.wake_waiting = %+v (%d): %s", waiting, len(waiting), rec.text())
	}
	if waiting[0].attr("earliest_wake_at") == "" || waiting[0].attr("earliest_wake_at") == "0" {
		t.Fatalf("the wait has no expected wake time: %+v", waiting[0].attrs)
	}

	// The sweep runs every tick: the same wait is not news.
	d.Tick(ctx)
	if n := len(rec.find(EventWakeWaiting)); n != 1 {
		t.Fatalf("mail.wake_waiting repeated on an unchanged wait: %d", n)
	}

	// Long enough idle: the wake goes out instead.
	f.now = f.now.Add(10 * time.Minute)
	d.Tick(ctx)
	if w, _ := snd.counts(); w != 1 {
		t.Fatalf("wakes = %d, want 1", w)
	}
	if n := len(rec.find(EventWakeWaiting)); n != 1 {
		t.Fatalf("a delivered wake added a wait line: %d", n)
	}

	// The letters are still pending and the wake interval has not passed: a NEW reason.
	d.Tick(ctx)
	waiting = rec.find(EventWakeWaiting)
	if len(waiting) != 2 || waiting[1].attr("reason") != WaitWakeInterval {
		t.Fatalf("mail.wake_waiting = %+v (%d): %s", waiting, len(waiting), rec.text())
	}

	// Delivered / withdrawn: the next sweep forgets the session.
	if _, _, err := f.svc.CancelMail(m.ID, "test"); err != nil {
		t.Fatal(err)
	}
	d.Tick(ctx)
	d.mu.Lock()
	left := len(d.wakeWaits)
	d.mu.Unlock()
	if left != 0 {
		t.Fatalf("wake-waiting memory kept %d sessions after the letters were gone", left)
	}
}

// TestWakeWaitingPaused: the pause switch is a reason of its own (the letters wait for a
// person, not for the idle clock).
func TestWakeWaitingPaused(t *testing.T) {
	f := newFixture(t)
	f.sessionSeenAt(t, "sess-idle", "claude", jobstore.SessionIdle, time.Hour)
	f.send(t, "session:sess-idle", "hi", false)
	if _, changed, err := f.store.SetMailPaused("sess-idle", true); err != nil || !changed {
		t.Fatalf("pause: changed=%v err=%v", changed, err)
	}
	rec := newLogRecorder(t)
	d, snd, _ := newDeliverer(f)
	d.Tick(context.Background())
	waiting := rec.find(EventWakeWaiting)
	if len(waiting) != 1 || waiting[0].attr("reason") != WaitPaused {
		t.Fatalf("mail.wake_waiting = %+v (%d): %s", waiting, len(waiting), rec.text())
	}
	if w, _ := snd.counts(); w != 0 {
		t.Fatalf("a paused session was woken %d times", w)
	}
}

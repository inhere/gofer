package mail

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// The server deliverer (plan M5): wake conditions, the raw delivery paths, deadlines.

type sentText struct{ sid, text string }

// fakeSender stands in for the relay's ladder.
type fakeSender struct {
	mu      sync.Mutex
	wakes   []sentText
	letters []sentText
	wakeErr error
	sendErr error
}

func (f *fakeSender) Wake(_ context.Context, sid, text, _ string) (sessionrelay.RawResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wakes = append(f.wakes, sentText{sid, text})
	return sessionrelay.RawResult{Path: sessionrelay.PathTmux}, f.wakeErr
}

func (f *fakeSender) DeliverLetter(_ context.Context, sid, text, _ string) (sessionrelay.RawResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.letters = append(f.letters, sentText{sid, text})
	return sessionrelay.RawResult{Path: sessionrelay.PathCommand}, f.sendErr
}

func (f *fakeSender) counts() (wakes, letters int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.wakes), len(f.letters)
}

type fakeJobs struct {
	mu   sync.Mutex
	said []sentText
	err  error
}

func (f *fakeJobs) SaySession(id, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.said = append(f.said, sentText{id, msg})
	return f.err
}

func newDeliverer(f *fixture) (*Deliverer, *fakeSender, *fakeJobs) {
	snd, jobs := &fakeSender{}, &fakeJobs{}
	d := NewDeliverer(DelivererOptions{
		Svc: f.svc, Sender: snd, Jobs: jobs, Now: func() time.Time { return f.now },
	})
	return d, snd, jobs
}

func callerOrigin() Origin { return Origin{Kind: jobstore.MailKindCaller, Caller: "alice"} }

// send puts one letter into the mailbox.
func (f *fixture) send(t *testing.T, to, body string, urgent bool) jobstore.Mail {
	t.Helper()
	res, err := f.svc.Send(SendInput{To: to, Body: body, Urgent: urgent, Origin: callerOrigin()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Held {
		t.Fatalf("letter to %s was held: %+v", to, res.Mail)
	}
	return res.Mail
}

// sessionSeenAt registers a session whose last_seen_at is `ago` before the fixture clock.
func (f *fixture) sessionSeenAt(t *testing.T, sid, agent, state string, ago time.Duration) {
	t.Helper()
	real := f.now
	f.now = real.Add(-ago)
	f.addSession(t, sid, agent, "", state)
	f.now = real
}

func (f *fixture) mailState(t *testing.T, id int64) jobstore.Mail {
	t.Helper()
	m, ok, err := f.store.GetMail(id)
	if err != nil || !ok {
		t.Fatalf("mail %d: ok=%v err=%v", id, ok, err)
	}
	return m
}

func TestWakeDueTable(t *testing.T) {
	now := time.Unix(1_760_000_000, 0)
	idle := 5 * time.Minute
	ses := func(state string, ago time.Duration) jobstore.AgentSession {
		return jobstore.AgentSession{State: state, LastSeenAt: now.Add(-ago).Unix()}
	}
	plain := []jobstore.Mail{{ID: 1}}
	urgent := []jobstore.Mail{{ID: 1}, {ID: 2, Urgent: true}}
	cases := []struct {
		name    string
		a       jobstore.AgentSession
		letters []jobstore.Mail
		want    bool
	}{
		{"idle and old", ses(jobstore.SessionIdle, 6*time.Minute), plain, true},
		{"idle exactly at the threshold", ses(jobstore.SessionIdle, 5*time.Minute), plain, true},
		{"idle but recent", ses(jobstore.SessionIdle, 4*time.Minute), plain, false},
		{"idle recent with an urgent letter", ses(jobstore.SessionIdle, 10*time.Second), urgent, true},
		{"offline and old", ses(jobstore.SessionOffline, 40*time.Minute), plain, true},
		{"offline but recent", ses(jobstore.SessionOffline, time.Minute), plain, false},
		{"offline recent urgent still needs the age", ses(jobstore.SessionOffline, time.Minute), urgent, false},
		{"never seen", jobstore.AgentSession{State: jobstore.SessionIdle}, plain, true},
		{"waiting_reply is never woken", ses(jobstore.SessionWaitingReply, time.Hour), urgent, false},
		{"running", ses(jobstore.SessionRunning, time.Hour), urgent, false},
		{"needs_attention", ses(jobstore.SessionNeedsAttention, time.Hour), plain, false},
		{"ended", ses(jobstore.SessionEnded, time.Hour), plain, false},
		{"handed_off", ses(jobstore.SessionHandedOff, time.Hour), plain, false},
		{"nothing pending", ses(jobstore.SessionIdle, time.Hour), nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WakeDue(tc.a, tc.letters, now, idle); got != tc.want {
				t.Fatalf("WakeDue = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTickWakesIdleSessionAndRateLimits(t *testing.T) {
	f := newFixture(t)
	f.sessionSeenAt(t, "sess-idle", "claude", jobstore.SessionIdle, 10*time.Minute)
	f.send(t, "session:sess-idle", "one", false)
	f.send(t, "session:sess-idle", "two", false)
	d, snd, _ := newDeliverer(f)
	ctx := context.Background()

	d.Tick(ctx)
	if len(snd.wakes) != 1 || snd.wakes[0].sid != "sess-idle" || snd.wakes[0].text != RenderWake(2) {
		t.Fatalf("first tick wakes = %+v", snd.wakes)
	}
	// The wake is only a prompt: the letters stay pending for the hook to inject.
	if n, _ := f.store.PendingCount(jobstore.MailKindSession, "sess-idle"); n != 2 {
		t.Fatalf("pending after wake = %d, want 2", n)
	}

	f.now = f.now.Add(9 * time.Minute)
	d.Tick(ctx)
	if len(snd.wakes) != 1 {
		t.Fatalf("a second wake inside 10 minutes: %+v", snd.wakes)
	}
	f.now = f.now.Add(2 * time.Minute) // 11 minutes after the first
	d.Tick(ctx)
	if len(snd.wakes) != 2 {
		t.Fatalf("no wake after the interval: %+v", snd.wakes)
	}
}

func TestTickWakeFailureStillBooksTheInterval(t *testing.T) {
	f := newFixture(t)
	f.sessionSeenAt(t, "sess-x", "claude", jobstore.SessionIdle, 10*time.Minute)
	f.send(t, "session:sess-x", "one", false)
	d, snd, _ := newDeliverer(f)
	snd.wakeErr = errors.New("no pane")
	d.Tick(context.Background())
	d.Tick(context.Background())
	if len(snd.wakes) != 1 {
		t.Fatalf("a failing wake was retried every sweep: %d", len(snd.wakes))
	}
}

func TestTickDoesNotWakeBusyOrWaitingSessions(t *testing.T) {
	f := newFixture(t)
	for sid, state := range map[string]string{
		"s-run": jobstore.SessionRunning, "s-wait": jobstore.SessionWaitingReply, "s-end": jobstore.SessionEnded,
	} {
		// A letter cannot be addressed to an ended session, so it ends AFTER the send.
		f.sessionSeenAt(t, sid, "claude", jobstore.SessionRunning, time.Hour)
		f.send(t, "session:"+sid, "hi", true)
		if _, err := f.store.SetSessionState(sid, state); err != nil {
			t.Fatal(err)
		}
	}
	f.sessionSeenAt(t, "s-recent", "claude", jobstore.SessionIdle, time.Minute)
	f.send(t, "session:s-recent", "hi", false)
	d, snd, _ := newDeliverer(f)
	d.Tick(context.Background())
	if len(snd.wakes) != 0 {
		t.Fatalf("woke sessions that must not be woken: %+v", snd.wakes)
	}
}

// A wake goes through the relay's raw send, never Say / the outbox: with the REAL relay the
// session's web trace stays empty and the injected text carries no web prefix.
func TestWakeLeavesNoWebTraceThroughTheRealRelay(t *testing.T) {
	f := newFixture(t)
	var seen []sessionrelay.InjectRequest
	f.relay.SetInjector(sessionrelay.InjectorFunc(func(_ context.Context, req sessionrelay.InjectRequest) (sessionrelay.InjectResult, error) {
		seen = append(seen, req)
		return sessionrelay.InjectResult{JobID: "job-inj"}, nil
	}))
	real := f.now
	f.now = real.Add(-10 * time.Minute)
	if _, err := f.store.UpsertAgentSession(jobstore.AgentSession{
		SessionID: "sess-real", Agent: "claude", CallerID: "alice", State: jobstore.SessionIdle,
		Runner: "w-x", TmuxPane: "%1", ProjectKey: "self",
	}); err != nil {
		t.Fatal(err)
	}
	f.now = real
	f.send(t, "session:sess-real", "hello", false)
	d := NewDeliverer(DelivererOptions{Svc: f.svc, Sender: f.relay, Jobs: &fakeJobs{}, Now: func() time.Time { return f.now }})
	d.Tick(context.Background())

	if len(seen) != 1 {
		t.Fatalf("injection jobs = %d, want 1", len(seen))
	}
	script := strings.Join(seen[0].Cmd, " ")
	if !strings.Contains(script, "gofer-mail-wake") || strings.Contains(script, sessionrelay.InjectPrefix) {
		t.Fatalf("wake script = %q", script)
	}
	if out, _ := f.store.ReadSessionOutbox("sess-real"); len(out) != 0 {
		t.Fatalf("a wake wrote the outbox: %+v", out)
	}
	if rows, _ := f.store.ListSessionDecisions("sess-real", "", 5, ""); len(rows.Decisions) != 0 {
		t.Fatalf("a wake wrote an audit decision: %+v", rows.Decisions)
	}
}

func TestSugaSessionGetsTheBodyInTheTag(t *testing.T) {
	f := newFixture(t)
	f.sessionSeenAt(t, "sess-suga", "suga", jobstore.SessionRunning, time.Minute)
	m := f.send(t, "session:sess-suga", "build </gofer-mail> done", true)
	d, snd, _ := newDeliverer(f)

	d.Tick(context.Background())
	if w, l := snd.counts(); w != 0 || l != 1 {
		t.Fatalf("wakes=%d letters=%d, a suga session gets the body, not a wake", w, l)
	}
	got := snd.letters[0].text
	want := RenderLetter(Letter{ID: m.ID, From: OriginOf(m).Display(), Urgent: true, Body: m.Body})
	if got != want || !strings.HasPrefix(got, `<gofer-mail id="`) || strings.Count(got, "</gofer-mail>") != 1 {
		t.Fatalf("suga text = %q, want %q", got, want)
	}
	if st := f.mailState(t, m.ID); st.State != jobstore.MailDelivered || st.Attempts != 1 {
		t.Fatalf("after a successful send: %+v", st)
	}
}

func TestSugaNotOnlineLeavesTheLetterPending(t *testing.T) {
	f := newFixture(t)
	f.sessionSeenAt(t, "sess-suga", "suga", jobstore.SessionIdle, time.Minute)
	m := f.send(t, "session:sess-suga", "hi", false)
	d, snd, _ := newDeliverer(f)
	snd.sendErr = &sessionrelay.UndeliverableError{Reason: sessionrelay.ReasonNotRunning}
	ctx := context.Background()

	d.Tick(ctx)
	st := f.mailState(t, m.ID)
	if st.State != jobstore.MailPending || st.ClaimUntil != 0 || st.Attempts != 1 {
		t.Fatalf("exit 3 must leave the letter pending (claim released): %+v", st)
	}
	d.Tick(ctx) // inside the retry back-off: no second attempt
	if _, l := snd.counts(); l != 1 {
		t.Fatalf("suga was retried inside the back-off: %d", l)
	}

	snd.sendErr = nil
	f.now = f.now.Add(3 * time.Minute)
	d.Tick(ctx)
	if st := f.mailState(t, m.ID); st.State != jobstore.MailDelivered {
		t.Fatalf("after the session came back: %+v", st)
	}
}

func TestSugaBatchAndEndedSession(t *testing.T) {
	f := newFixture(t)
	f.sessionSeenAt(t, "sess-suga", "suga", jobstore.SessionIdle, time.Minute)
	f.sessionSeenAt(t, "sess-gone", "suga", jobstore.SessionRunning, time.Hour)
	for range 7 {
		f.send(t, "session:sess-suga", "x", false)
	}
	f.send(t, "session:sess-gone", "x", false)
	if _, err := f.store.SetSessionState("sess-gone", jobstore.SessionEnded); err != nil {
		t.Fatal(err)
	}
	d, snd, _ := newDeliverer(f)
	d.Tick(context.Background())
	if _, l := snd.counts(); l != deliverBatch {
		t.Fatalf("letters sent = %d, want the batch of %d (and none to the ended session)", l, deliverBatch)
	}
}

func (f *fixture) setJob(t *testing.T, id, status string, interactive bool) {
	t.Helper()
	if err := f.store.UpsertJob(jobstore.JobRecord{
		ID: id, ProjectKey: "gofer", Agent: "claude", Runner: "local", Status: status, Interactive: interactive,
		ResultDir: t.TempDir(), StartedAt: f.now.Unix(), UpdatedAt: f.now.Unix(),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestACPJobAwaitingInputGetsOneLetterAtATime(t *testing.T) {
	f := newFixture(t)
	f.setJob(t, "job-acp", job.StatusRunning, false)
	a := f.send(t, "job:job-acp", "first", false)
	b := f.send(t, "job:job-acp", "second", false)
	d, _, jobs := newDeliverer(f)
	ctx := context.Background()

	d.Tick(ctx) // running: nothing is said
	if len(jobs.said) != 0 {
		t.Fatalf("said to a job that is not awaiting input: %+v", jobs.said)
	}
	f.setJob(t, "job-acp", job.StatusAwaitingInput, false)
	d.Tick(ctx)
	if len(jobs.said) != 1 || jobs.said[0].sid != "job-acp" ||
		jobs.said[0].text != RenderLetter(Letter{ID: a.ID, From: OriginOf(a).Display(), Body: "first"}) {
		t.Fatalf("said = %+v", jobs.said)
	}
	if st := f.mailState(t, a.ID); st.State != jobstore.MailDelivered {
		t.Fatalf("first letter = %+v", st)
	}
	if st := f.mailState(t, b.ID); st.State != jobstore.MailPending {
		t.Fatalf("the second letter must wait for the next awaiting_input: %+v", st)
	}
	d.Tick(ctx) // still awaiting_input in this fake: the next letter goes
	if len(jobs.said) != 2 {
		t.Fatalf("said = %+v", jobs.said)
	}
}

func TestACPJobSayFailureReleasesTheClaim(t *testing.T) {
	f := newFixture(t)
	f.setJob(t, "job-acp", job.StatusAwaitingInput, false)
	m := f.send(t, "job:job-acp", "hi", false)
	d, _, jobs := newDeliverer(f)
	jobs.err = job.ErrJobNotRunning
	d.Tick(context.Background())
	if st := f.mailState(t, m.ID); st.State != jobstore.MailPending || st.ClaimUntil != 0 {
		t.Fatalf("a failed say must hand the letter back: %+v", st)
	}
}

func TestPtyJobLetterIsHeld(t *testing.T) {
	f := newFixture(t)
	f.setJob(t, "job-pty", job.StatusRunning, true)
	m := f.send(t, "job:job-pty", "hi", false)
	d, _, jobs := newDeliverer(f)
	d.Tick(context.Background())
	st := f.mailState(t, m.ID)
	if st.State != jobstore.MailHeld || st.LastError != PtyHoldReason || st.CaptureItemID == "" {
		t.Fatalf("pty letter = %+v", st)
	}
	if len(jobs.said) != 0 {
		t.Fatalf("said to a pty job: %+v", jobs.said)
	}
	if w := f.item(t, st.CaptureItemID); !strings.Contains(w.Title, "待指派") {
		t.Fatalf("capture item = %+v", w)
	}
}

func TestOverdueLetterIsHeldIntoTheCaptureBox(t *testing.T) {
	f := newFixture(t)
	f.sessionSeenAt(t, "sess-run", "claude", jobstore.SessionRunning, time.Minute)
	m := f.send(t, "session:sess-run", "stale", false)
	d, _, _ := newDeliverer(f)
	ctx := context.Background()

	d.Tick(ctx)
	if st := f.mailState(t, m.ID); st.State != jobstore.MailPending {
		t.Fatalf("a fresh letter moved: %+v", st)
	}
	f.now = f.now.Add(25 * time.Hour)
	d.Tick(ctx)
	st := f.mailState(t, m.ID)
	if st.State != jobstore.MailHeld || st.CaptureItemID == "" {
		t.Fatalf("overdue letter = %+v", st)
	}
}

func TestExpiredClaimIsReleasedByTheSweep(t *testing.T) {
	f := newFixture(t)
	f.sessionSeenAt(t, "sess-run", "claude", jobstore.SessionRunning, time.Minute)
	m := f.send(t, "session:sess-run", "x", false)
	if _, ok, err := f.store.ClaimMail(m.ID, "hook:sess-run", time.Minute); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	d, _, _ := newDeliverer(f)
	d.Tick(context.Background())
	if st := f.mailState(t, m.ID); st.State != jobstore.MailDelivering {
		t.Fatalf("a live lease was released: %+v", st)
	}
	f.now = f.now.Add(2 * time.Minute)
	d.Tick(context.Background())
	if st := f.mailState(t, m.ID); st.State != jobstore.MailPending {
		t.Fatalf("an expired lease stayed claimed: %+v", st)
	}
}

func TestDelivererDrainStopsTheLoop(t *testing.T) {
	f := newFixture(t)
	var sweeps atomic.Int64
	snd, jobs := &fakeSender{}, &fakeJobs{}
	d := NewDeliverer(DelivererOptions{
		Svc: f.svc, Sender: snd, Jobs: jobs,
		Settings: Settings{Tick: 5 * time.Millisecond},
		Now:      func() time.Time { sweeps.Add(1); return f.now },
	})
	d.Start()
	d.Start() // idempotent
	wait.Until(t, 10*time.Second, "the loop sweeps repeatedly", func() bool { return sweeps.Load() >= 3 })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	after := sweeps.Load()
	time.Sleep(50 * time.Millisecond) // asserting "nothing more happens": a short fixed window is the point
	if sweeps.Load() != after {
		t.Fatalf("the loop kept sweeping after Drain: %d -> %d", after, sweeps.Load())
	}
	if err := d.Drain(ctx); err != nil {
		t.Fatalf("second Drain: %v", err)
	}
}

func TestDelivererSettingsDefaultsAndConfigure(t *testing.T) {
	d := NewDeliverer(DelivererOptions{})
	if got := d.current(); got != DefaultSettings() {
		t.Fatalf("defaults = %+v", got)
	}
	d.Configure(Settings{WakeIdle: time.Minute})
	got := d.current()
	if got.WakeIdle != time.Minute || got.Tick != DefaultTick || got.Lease != DefaultLease {
		t.Fatalf("configured = %+v", got)
	}
}

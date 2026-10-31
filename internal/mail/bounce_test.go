package mail

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/msgprefix"
)

// A letter that reaches nobody is no longer silent (gofer-0n6r): a job: address that can never
// take a letter is refused at the send, a letter the sweep cannot deliver is parked as soon as
// that is known, and either way the sender gets gofer's 退信 saying which letter, why, and where.

// bouncesOf lists the gofer notices written to one recipient.
func (f *fixture) bouncesOf(t *testing.T, toKind, toID string) []jobstore.Mail {
	t.Helper()
	list, err := f.store.ListMail(jobstore.ListMailOpts{
		FromKind: jobstore.MailKindGofer, ToKind: toKind, ToID: toID, Limit: 20, OldestFirst: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// sendAs puts one letter into the mailbox from a named sender (a session, so the notice of a
// failed delivery has an inbox to land in).
func (f *fixture) sendAs(t *testing.T, from, to, body string, urgent bool) jobstore.Mail {
	t.Helper()
	res, err := f.svc.Send(SendInput{
		To: to, Body: body, Urgent: urgent,
		Origin: Origin{Kind: jobstore.MailKindSession, ID: from, Caller: "alice"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Held {
		t.Fatalf("letter to %s was held: %+v", to, res.Mail)
	}
	return res.Mail
}

func TestSendToAJobThatCannotReceiveIsRefused(t *testing.T) {
	f := threeSessionMailbox(t)
	// The job service's word: job-sess is an ACP session job, job-oneoff is a one-off run.
	f.svc.SetSessionJobCheck(func(id string) (bool, bool) { return id == "job-sess", true })
	f.addJob(t, "job-sess", job.StatusRunning)
	f.addJob(t, "job-oneoff", job.StatusRunning)

	if _, err := f.svc.Send(SendInput{To: "job:job-sess", Body: "hello", Origin: sender()}); err != nil {
		t.Fatalf("a session job must take a letter: %v", err)
	}
	_, err := f.svc.Send(SendInput{To: "job:job-oneoff", Body: "hello", Origin: sender()})
	if !errors.Is(err, ErrJobCannotReceive) {
		t.Fatalf("a one-off job err = %v, want ErrJobCannotReceive", err)
	}
	// The refusal says what to do instead, and nothing was stored.
	for _, want := range []string{"one-off", "session:…", "gofer job comment"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not mention %q", err, want)
		}
	}
	if n, err := f.store.PendingCount(jobstore.MailKindJob, "job-oneoff"); err != nil || n != 0 {
		t.Fatalf("a refused letter was stored: pending=%d err=%v", n, err)
	}
	// Without a job service the mailbox cannot tell, and a live job is accepted as before.
	f.svc.SetSessionJobCheck(nil)
	if _, err := f.svc.Send(SendInput{To: "job:job-oneoff", Body: "hello", Origin: sender()}); err != nil {
		t.Fatalf("without the job seam: %v", err)
	}
}

func TestEndedJobLetterIsReturnedAtOnce(t *testing.T) {
	f := threeSessionMailbox(t)
	f.setJob(t, "job-acp", job.StatusRunning, false)
	m := f.sendAs(t, "sess-b", "job:job-acp", "the answer you asked for", true)
	d, _, _ := newDeliverer(f)
	ctx := context.Background()

	d.Tick(ctx) // running: a session job may still reach awaiting_input
	if st := f.mailState(t, m.ID); st.State != jobstore.MailPending {
		t.Fatalf("a live session job's letter moved: %+v", st)
	}
	f.setJob(t, "job-acp", job.StatusDone, false)
	d.Tick(ctx)
	st := f.mailState(t, m.ID)
	if st.State != jobstore.MailHeld || st.CaptureItemID == "" || !strings.Contains(st.LastError, "已结束") {
		t.Fatalf("letter to an ended job = %+v", st)
	}
	bounces := f.bouncesOf(t, jobstore.MailKindSession, "sess-b")
	if len(bounces) != 1 {
		t.Fatalf("bounces to the sender = %+v, want one", bounces)
	}
	b := bounces[0]
	if !strings.HasPrefix(b.Body, msgprefix.BounceOpen) || !b.Urgent || b.State != jobstore.MailPending {
		t.Fatalf("bounce = %+v", b)
	}
	for _, want := range []string{"#", "job:job-acp", "已结束", st.CaptureItemID, "the answer"} {
		if !strings.Contains(b.Body, want) {
			t.Fatalf("bounce body %q does not mention %q", b.Body, want)
		}
	}
	if b.FromKind != jobstore.MailKindGofer || b.FromID != "" || b.FromCaller != "" {
		t.Fatalf("bounce sender = %+v, want gofer itself", b)
	}
	if b.ToAddress != "session:sess-b" || b.ToKind != jobstore.MailKindSession || b.ToID != "sess-b" {
		t.Fatalf("bounce recipient = %+v", b)
	}
	// A second sweep of the same job adds nothing: the letter is no longer pending.
	d.Tick(ctx)
	if got := f.bouncesOf(t, jobstore.MailKindSession, "sess-b"); len(got) != 1 {
		t.Fatalf("the bounce was repeated: %+v", got)
	}
}

func TestOneOffJobLettersAreParkedAndReturned(t *testing.T) {
	f := threeSessionMailbox(t)
	f.svc.SetSessionJobCheck(func(id string) (bool, bool) { return id == "job-sess", true })
	f.setJob(t, "job-oneoff", job.StatusRunning, false)
	// A letter stored before the send-time rule existed (the state the sweep has to clean up).
	m, err := f.store.InsertMail(jobstore.MailInput{
		FromKind: jobstore.MailKindSession, FromID: "sess-b", FromCaller: "alice",
		ToAddress: "job:job-oneoff", ToKind: jobstore.MailKindJob, ToID: "job-oneoff", Body: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	d, _, jobs := newDeliverer(f)
	d.Tick(context.Background())
	st := f.mailState(t, m.ID)
	if st.State != jobstore.MailHeld || !strings.Contains(st.LastError, "一次性作业") {
		t.Fatalf("one-off job letter = %+v", st)
	}
	if len(jobs.said) != 0 {
		t.Fatalf("said into a one-off job: %+v", jobs.said)
	}
	bounces := f.bouncesOf(t, jobstore.MailKindSession, "sess-b")
	if len(bounces) != 1 || !strings.Contains(bounces[0].Body, "一次性作业") {
		t.Fatalf("bounces = %+v", bounces)
	}
}

func TestPtyAndOverdueLettersAreReturned(t *testing.T) {
	f := threeSessionMailbox(t)
	f.setJob(t, "job-pty", job.StatusRunning, true)
	pty := f.sendAs(t, "sess-b", "job:job-pty", "terminal?", false)
	overdue := f.sendAs(t, "sess-b", "session:sess-a", "stale", false)
	d, _, _ := newDeliverer(f)
	ctx := context.Background()

	d.Tick(ctx)
	if st := f.mailState(t, pty.ID); st.State != jobstore.MailHeld {
		t.Fatalf("pty letter = %+v", st)
	}
	ptyBounce := f.bouncesOf(t, jobstore.MailKindSession, "sess-b")
	if len(ptyBounce) != 1 || !strings.Contains(ptyBounce[0].Body, "pty") {
		t.Fatalf("pty bounces = %+v", ptyBounce)
	}

	f.now = f.now.Add(25 * time.Hour)
	d.Tick(ctx)
	if st := f.mailState(t, overdue.ID); st.State != jobstore.MailHeld {
		t.Fatalf("overdue letter = %+v", st)
	}
	overdueBounce, err := f.store.ListMail(jobstore.ListMailOpts{
		FromKind: jobstore.MailKindGofer, ToKind: jobstore.MailKindSession, ToID: "sess-b", Limit: 20, OldestFirst: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range overdueBounce {
		if strings.Contains(b.Body, "送达期限") && strings.Contains(b.Body, "#") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no deadline bounce: %+v", overdueBounce)
	}
}

func TestBounceOfABounceAndOfBroadcastCopiesIsSilent(t *testing.T) {
	f := threeSessionMailbox(t)
	notice := jobstore.Mail{ID: 5, FromKind: jobstore.MailKindGofer, ToKind: jobstore.MailKindSession, ToID: "sess-b", Body: "x"}
	if _, ok, err := f.svc.Bounce(notice, "why", "nowhere"); err != nil || ok {
		t.Fatalf("bouncing gofer's own notice: ok=%v err=%v", ok, err)
	}
	copy1 := jobstore.Mail{ID: 6, FromKind: jobstore.MailKindCaller, FromCaller: "alice", BroadcastID: 5, Body: "x"}
	if _, ok, err := f.svc.Bounce(copy1, "why", "nowhere"); err != nil || ok {
		t.Fatalf("bouncing a broadcast copy: ok=%v err=%v", ok, err)
	}
	if n, _ := f.store.PendingCount(jobstore.MailKindSession, "sess-b"); n != 0 {
		t.Fatalf("a silent bounce still stored %d letters", n)
	}
}

func TestBounceToAJobSenderIsParked(t *testing.T) {
	f := threeSessionMailbox(t)
	f.setJob(t, "job-pty", job.StatusRunning, true)
	// A job's own letter to a pty job: neither end has an inbox, so the notice is only recorded.
	m, err := f.store.InsertMail(jobstore.MailInput{
		FromKind: jobstore.MailKindJob, FromID: "job-pty", FromCaller: "alice",
		ToAddress: "job:job-pty", ToKind: jobstore.MailKindJob, ToID: "job-pty", Body: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, ok, err := f.svc.Bounce(m, "why", captureWhere(i18n.Default, ""))
	if err != nil || !ok {
		t.Fatalf("Bounce ok=%v err=%v", ok, err)
	}
	if b.State != jobstore.MailHeld || b.LastError != noInboxReason(i18n.Default) || b.ToKind != jobstore.MailKindJob || b.JobID != "job-pty" {
		t.Fatalf("a job's bounce = %+v", b)
	}
	if b.CaptureItemID != "" {
		t.Fatalf("a parked notice opened a capture item: %+v", b)
	}
}

func TestReplyToABounceIsRefused(t *testing.T) {
	f := threeSessionMailbox(t)
	notice, ok, err := f.svc.Bounce(jobstore.Mail{
		ID: 9, FromKind: jobstore.MailKindSession, FromID: "sess-b", FromCaller: "alice",
		ToAddress: "session:sess-a", ToKind: jobstore.MailKindSession, ToID: "sess-a", Body: "hi",
	}, "收件 job 已结束", captureWhere(i18n.Default, "wi-1"))
	if err != nil || !ok || notice.State != jobstore.MailPending {
		t.Fatalf("Bounce = %+v ok=%v err=%v", notice, ok, err)
	}
	_, err = f.svc.Reply(ReplyInput{ID: notice.ID, Body: "sorry, wrong address", Origin: Origin{Kind: jobstore.MailKindSession, ID: "sess-b", Caller: "alice"}})
	if !errors.Is(err, ErrNotAnswerable) {
		t.Fatalf("reply to a gofer notice err = %v, want ErrNotAnswerable", err)
	}
}

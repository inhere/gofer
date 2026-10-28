package jobstore

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// M1 of the mailbox (design 2026-10-16 §「信箱」): the letter table and its state machine.

// mailFixture is a store with a frozen clock, so every timestamp in these tests is exact.
type mailFixture struct {
	store *Store
	now   time.Time
}

func newMailFixture(t *testing.T) *mailFixture {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	f := &mailFixture{store: s, now: time.Unix(1_760_000_000, 0)}
	s.SetClock(func() time.Time { return f.now })
	return f
}

func (f *mailFixture) advance(d time.Duration) { f.now = f.now.Add(d) }

// sampleMail is a valid letter from sessionA (caller alice) to sessionB.
func sampleMail(over ...func(*MailInput)) MailInput {
	in := MailInput{
		FromKind: MailKindSession, FromID: "sess-a", FromCaller: "alice",
		ToAddress: "session:sess-b", ToKind: MailKindSession, ToID: "sess-b", Body: "hello",
	}
	for _, fn := range over {
		fn(&in)
	}
	return in
}

func TestMailAckUpgrade(t *testing.T) {
	f := newMailFixture(t)
	m, err := f.store.InsertMail(sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	got, changed, err := f.store.SetMailAck(m.ID, "received", "session:sess-b")
	if err != nil || !changed || got.AckKind != "received" || got.AckBy != "session:sess-b" || got.AckAt == 0 {
		t.Fatalf("received=%+v changed=%v err=%v", got, changed, err)
	}
	got, changed, err = f.store.SetMailAck(m.ID, "done", "session:sess-b")
	if err != nil || !changed || got.AckKind != "done" {
		t.Fatalf("done=%+v changed=%v err=%v", got, changed, err)
	}
	got, changed, err = f.store.SetMailAck(m.ID, "received", "session:sess-b")
	if err != nil || changed || got.AckKind != "done" {
		t.Fatalf("downgrade=%+v changed=%v err=%v", got, changed, err)
	}
}

func TestMailInsertDefaultsAndReadBack(t *testing.T) {
	f := newMailFixture(t)
	m, err := f.store.InsertMail(sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	if m.ID <= 0 || m.State != MailPending {
		t.Fatalf("inserted mail=%+v, want a pending letter with an id", m)
	}
	if m.ThreadID != mailThreadID(m.ID) {
		t.Fatalf("thread_id=%q, want the letter's own thread %q", m.ThreadID, mailThreadID(m.ID))
	}
	want := f.now.Unix() + int64(DefaultMailDeliverBy/time.Second)
	if m.DeliverBy != want {
		t.Fatalf("deliver_by=%d, want created_at + 24h = %d", m.DeliverBy, want)
	}
	if m.CreatedAt != f.now.Unix() || m.Attempts != 0 || m.ClaimedBy != "" || m.CaptureItemID != "" {
		t.Fatalf("fresh letter=%+v", m)
	}
	got, ok, err := f.store.GetMail(m.ID)
	if err != nil || !ok || got.Body != "hello" || got.FromKind != MailKindSession || got.ToID != "sess-b" {
		t.Fatalf("get mail ok=%v err=%v got=%+v", ok, err, got)
	}
	if _, ok, err := f.store.GetMail(m.ID + 999); err != nil || ok {
		t.Fatalf("unknown mail ok=%v err=%v", ok, err)
	}
}

func TestMailInsertValidation(t *testing.T) {
	f := newMailFixture(t)
	cases := []struct {
		name string
		in   MailInput
	}{
		{"unknown from kind", sampleMail(func(in *MailInput) { in.FromKind = "ghost" })},
		{"unknown to kind", sampleMail(func(in *MailInput) { in.ToKind = "ghost" })},
		{"session sender without id", sampleMail(func(in *MailInput) { in.FromID = " " })},
		{"job sender without id", sampleMail(func(in *MailInput) { in.FromKind, in.FromID = MailKindJob, "" })},
		{"session recipient without id", sampleMail(func(in *MailInput) { in.ToID = "" })},
		{"empty body", sampleMail(func(in *MailInput) { in.Body = "  \n " })},
	}
	for _, tc := range cases {
		if _, err := f.store.InsertMail(tc.in); !errors.Is(err, ErrMailInvalid) {
			t.Fatalf("%s: err=%v, want ErrMailInvalid", tc.name, err)
		}
	}
}

// TestMailStateMachineHappyPath walks the delivery path a letter actually takes, plus the
// answered end state.
func TestMailStateMachineHappyPath(t *testing.T) {
	f := newMailFixture(t)
	m, err := f.store.InsertMail(sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := f.store.ClaimMail(m.ID, "hook:sess-b", DefaultMailClaimTTL)
	if err != nil || !ok || claimed.State != MailDelivering {
		t.Fatalf("claim ok=%v err=%v mail=%+v", ok, err, claimed)
	}
	if claimed.ClaimedBy != "hook:sess-b" || claimed.ClaimUntil != f.now.Unix()+60 || claimed.Attempts != 1 {
		t.Fatalf("claim did not stamp holder/lease/attempts: %+v", claimed)
	}
	confirmed, ok, err := f.store.ConfirmMail(m.ID)
	if err != nil || !ok || confirmed.State != MailDelivered {
		t.Fatalf("confirm ok=%v err=%v mail=%+v", ok, err, confirmed)
	}
	if confirmed.DeliveredAt != f.now.Unix() || confirmed.ClaimUntil != 0 {
		t.Fatalf("confirm did not stamp delivery time / release the lease: %+v", confirmed)
	}
	answered, ok, err := f.store.MarkMailAnswered(m.ID)
	if err != nil || !ok || answered.State != MailAnswered {
		t.Fatalf("answer ok=%v err=%v mail=%+v", ok, err, answered)
	}
	if answered.DeliveredAt != f.now.Unix() {
		t.Fatalf("answer changed delivered_at: %+v", answered)
	}
}

// TestMailIllegalTransitions: every move the state machine does not list is refused with
// ok=false and leaves the row untouched.
func TestMailIllegalTransitions(t *testing.T) {
	f0 := newMailFixture(t)
	m, err := f0.store.InsertMail(sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	// From pending only claim / read / hold / expire / cancel are legal (read IS delivery).
	for _, st := range []struct {
		name string
		run  func() (Mail, bool, error)
	}{
		{"confirm", func() (Mail, bool, error) { return f0.store.ConfirmMail(m.ID) }},
		{"answer", func() (Mail, bool, error) { return f0.store.MarkMailAnswered(m.ID) }},
		{"fail", func() (Mail, bool, error) { return f0.store.FailMail(m.ID, "nope") }},
	} {
		if _, ok, err := st.run(); err != nil || ok {
			t.Fatalf("pending → %s: ok=%v err=%v, want refused", st.name, ok, err)
		}
	}
	if again, ok, _ := f0.store.MarkMailAnswered(m.ID); ok || again.ID != 0 {
		t.Fatal("refused transition returned a letter")
	}
	// Now walk pending → delivering → delivered → answered and check that the answered end
	// state refuses every move.
	if _, ok, err := f0.store.ClaimMail(m.ID, "hook", 0); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if _, ok, err := f0.store.ConfirmMail(m.ID); err != nil || !ok {
		t.Fatalf("confirm: ok=%v err=%v", ok, err)
	}
	if _, ok, err := f0.store.MarkMailAnswered(m.ID); err != nil || !ok {
		t.Fatalf("answer: ok=%v err=%v", ok, err)
	}
	for _, st := range []struct {
		name string
		run  func() (Mail, bool, error)
	}{
		{"claim", func() (Mail, bool, error) { return f0.store.ClaimMail(m.ID, "x", 0) }},
		{"hold", func() (Mail, bool, error) { return f0.store.HoldMail(m.ID, "why") }},
		{"cancel", func() (Mail, bool, error) { return f0.store.CancelMail(m.ID, "why") }},
		{"expire", func() (Mail, bool, error) { return f0.store.ExpireMail(m.ID, "why") }},
		{"answer twice", func() (Mail, bool, error) { return f0.store.MarkMailAnswered(m.ID) }},
		{"fail", func() (Mail, bool, error) { return f0.store.FailMail(m.ID, "why") }},
	} {
		if _, ok, err := st.run(); err != nil || ok {
			t.Fatalf("answered → %s: ok=%v err=%v, want refused", st.name, ok, err)
		}
	}
	if got, _, _ := f0.store.GetMail(m.ID); got.State != MailAnswered {
		t.Fatalf("refused transitions changed the state: %+v", got)
	}
	// A cancelled letter is final too.
	c, err := f0.store.InsertMail(sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f0.store.CancelMail(c.ID, "changed my mind"); err != nil || !ok {
		t.Fatalf("cancel: ok=%v err=%v", ok, err)
	}
	if _, ok, err := f0.store.ClaimMail(c.ID, "hook", 0); err != nil || ok {
		t.Fatalf("cancelled → delivering: ok=%v err=%v, want refused", ok, err)
	}
	// An unknown state is not a target at all.
	if _, _, err := f0.store.moveMail(c.ID, "someday", "", nil); !errors.Is(err, ErrMailState) {
		t.Fatalf("unknown target state err=%v, want ErrMailState", err)
	}
	if MailValidState("someday") || !MailValidState(MailHeld) {
		t.Fatal("MailValidState disagrees with the transition table")
	}
}

// TestMailUnresolvedRecipientIsAllowed: a letter whose address resolved to nobody has no
// recipient columns at all — the mailbox parks it as held and its raw address is the only
// thing that says where it was meant to go.
func TestMailUnresolvedRecipientIsAllowed(t *testing.T) {
	f := newMailFixture(t)
	m, err := f.store.InsertMail(sampleMail(func(in *MailInput) {
		in.ToKind, in.ToID, in.ToAddress = "", "", "topic:gofer"
	}))
	if err != nil {
		t.Fatalf("a letter with no resolved recipient: %v", err)
	}
	if m.ToKind != "" || m.ToID != "" || m.ToAddress != "topic:gofer" {
		t.Fatalf("unresolved letter = %+v", m)
	}
	if _, ok, err := f.store.HoldMail(m.ID, "topic:gofer matched 2 sessions"); err != nil || !ok {
		t.Fatalf("hold ok=%v err=%v", ok, err)
	}
}

func TestMailSideBranches(t *testing.T) {
	f := newMailFixture(t)

	held, err := f.store.InsertMail(sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	h, ok, err := f.store.HoldMail(held.ID, "ambiguous: 2 candidates")
	if err != nil || !ok || h.State != MailHeld || h.LastError != "ambiguous: 2 candidates" {
		t.Fatalf("hold ok=%v err=%v mail=%+v", ok, err, h)
	}
	if ok, err := f.store.SetMailCaptureItem(held.ID, "wi-1"); err != nil || !ok {
		t.Fatalf("set capture item ok=%v err=%v", ok, err)
	}
	re, ok, err := f.store.ClaimMail(held.ID, "hook", DefaultMailClaimTTL)
	if err != nil || ok || re.ID != 0 {
		t.Fatalf("held letter is not claimable directly: ok=%v err=%v mail=%+v", ok, err, re)
	}
	if _, err := f.store.PruneMail(0); err != nil { // 0 = nothing is old enough
		t.Fatal(err)
	}
	back, _, err := f.store.GetMail(held.ID)
	if err != nil || back.State != MailHeld || back.CaptureItemID != "wi-1" {
		t.Fatalf("held letter changed: err=%v mail=%+v", err, back)
	}

	failed, err := f.store.InsertMail(sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.store.ClaimMail(failed.ID, "deliverer", 0); err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	fm, ok, err := f.store.FailMail(failed.ID, "recipient rejected")
	if err != nil || !ok || fm.State != MailFailed || fm.LastError != "recipient rejected" {
		t.Fatalf("fail ok=%v err=%v mail=%+v", ok, err, fm)
	}
	if _, ok, err := f.store.ConfirmMail(failed.ID); err != nil || ok {
		t.Fatalf("failed → delivered: ok=%v err=%v, want refused", ok, err)
	}

	exp, err := f.store.InsertMail(sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	em, ok, err := f.store.ExpireMail(exp.ID, "deadline passed")
	if err != nil || !ok || em.State != MailExpired {
		t.Fatalf("expire ok=%v err=%v mail=%+v", ok, err, em)
	}
	h2, ok, err := f.store.HoldMail(exp.ID, "into the capture box")
	if err != nil || !ok || h2.State != MailHeld {
		t.Fatalf("expired → held ok=%v err=%v mail=%+v", ok, err, h2)
	}
}

// TestMailMarkReadDelivers: reading is delivery — a pending letter read by its recipient is
// delivered, an already delivered one only gets read_at stamped.
func TestMailMarkReadDelivers(t *testing.T) {
	f := newMailFixture(t)
	pending, err := f.store.InsertMail(sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	f.advance(5 * time.Second)
	read, ok, err := f.store.MarkMailRead(pending.ID)
	if err != nil || !ok || read.State != MailDelivered {
		t.Fatalf("read pending ok=%v err=%v mail=%+v", ok, err, read)
	}
	if read.ReadAt != f.now.Unix() || read.DeliveredAt != f.now.Unix() {
		t.Fatalf("read did not stamp read_at / delivered_at: %+v", read)
	}

	delivered, err := f.store.InsertMail(sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.store.ClaimMail(delivered.ID, "hook", 0); err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	f.advance(time.Minute)
	if _, ok, err := f.store.ConfirmMail(delivered.ID); err != nil || !ok {
		t.Fatalf("confirm ok=%v err=%v", ok, err)
	}
	deliveredAt := f.now.Unix()
	f.advance(time.Minute)
	read2, ok, err := f.store.MarkMailRead(delivered.ID)
	if err != nil || !ok || read2.State != MailDelivered || read2.ReadAt != f.now.Unix() {
		t.Fatalf("read delivered ok=%v err=%v mail=%+v", ok, err, read2)
	}
	if read2.DeliveredAt != deliveredAt {
		t.Fatalf("read moved delivered_at: %+v", read2)
	}

	held, err := f.store.InsertMail(sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.store.HoldMail(held.ID, "nobody to send it to"); err != nil || !ok {
		t.Fatalf("hold ok=%v err=%v", ok, err)
	}
	if _, ok, err := f.store.MarkMailRead(held.ID); err != nil || ok {
		t.Fatalf("read a held letter: ok=%v err=%v, want refused", ok, err)
	}
}

// TestMailClaimIsExclusive: only one of several concurrent claims wins; the others see
// ok=false (the CAS is the only writer).
func TestMailClaimIsExclusive(t *testing.T) {
	f := newMailFixture(t)
	m, err := f.store.InsertMail(sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	const racers = 8
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		win   int
		errCh = make(chan error, racers)
	)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, ok, err := f.store.ClaimMail(m.ID, "path-"+string(rune('a'+i)), DefaultMailClaimTTL)
			if err != nil {
				errCh <- err
				return
			}
			if ok {
				mu.Lock()
				win++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	if win != 1 {
		t.Fatalf("%d of %d claims won, want exactly 1", win, racers)
	}
	got, _, err := f.store.GetMail(m.ID)
	if err != nil || got.State != MailDelivering || got.Attempts != 1 {
		t.Fatalf("after the race: err=%v mail=%+v, want one attempt", err, got)
	}
}

// TestMailReleaseExpiredClaims: a dead delivery path's letter goes back to pending once the
// lease passes, while a live lease is left alone.
func TestMailReleaseExpiredClaims(t *testing.T) {
	f := newMailFixture(t)
	stale, err := f.store.InsertMail(sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	live, err := f.store.InsertMail(sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.store.ClaimMail(stale.ID, "dead-path", DefaultMailClaimTTL); err != nil || !ok {
		t.Fatalf("claim stale ok=%v err=%v", ok, err)
	}
	f.advance(30 * time.Second)
	if _, ok, err := f.store.ClaimMail(live.ID, "live-path", 10*time.Minute); err != nil || !ok {
		t.Fatalf("claim live ok=%v err=%v", ok, err)
	}

	if n, err := f.store.ReleaseExpiredClaims(f.now.Unix()); err != nil || n != 0 {
		t.Fatalf("release before any lease expired n=%d err=%v", n, err)
	}
	f.advance(31 * time.Second) // stale's 60s lease (started 61s ago) has passed, live's has not
	n, err := f.store.ReleaseExpiredClaims(f.now.Unix())
	if err != nil || n != 1 {
		t.Fatalf("release n=%d err=%v, want 1", n, err)
	}
	back, _, _ := f.store.GetMail(stale.ID)
	if back.State != MailPending || back.ClaimUntil != 0 {
		t.Fatalf("released letter=%+v, want pending with no lease", back)
	}
	if back.ClaimedBy != "dead-path" || back.Attempts != 1 {
		t.Fatalf("release erased the audit of the dead claim: %+v", back)
	}
	if again, ok, err := f.store.ClaimMail(stale.ID, "next-path", DefaultMailClaimTTL); err != nil || !ok || again.Attempts != 2 {
		t.Fatalf("re-claim after release ok=%v err=%v mail=%+v", ok, err, again)
	}
	still, _, _ := f.store.GetMail(live.ID)
	if still.State != MailDelivering {
		t.Fatalf("live claim was released: %+v", still)
	}
}

// TestMailListFiltersAndPaging: recipient / sender / thread / state / time filters, the two
// orders and the page window.
func TestMailListFiltersAndPaging(t *testing.T) {
	f := newMailFixture(t)
	first, err := f.store.InsertMail(sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	rootCreated := f.now.Unix()
	f.advance(time.Minute)
	reply, err := f.store.InsertMail(sampleMail(func(in *MailInput) {
		in.ThreadID, in.ReplyTo = first.ThreadID, first.ID
		in.FromKind, in.FromID, in.FromCaller = MailKindSession, "sess-b", "alice"
		in.ToAddress, in.ToKind, in.ToID = "session:sess-a", MailKindSession, "sess-a"
		in.Body = "re: hello"
	}))
	if err != nil {
		t.Fatal(err)
	}
	f.advance(time.Minute)
	other, err := f.store.InsertMail(sampleMail(func(in *MailInput) {
		in.FromKind, in.FromID, in.FromCaller = MailKindCaller, "", "bob"
		in.ToKind, in.ToID, in.ToAddress = MailKindJob, "job-7", "job:job-7"
		in.JobID = "job-7"
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.store.HoldMail(other.ID, "pty job"); err != nil || !ok {
		t.Fatalf("hold ok=%v err=%v", ok, err)
	}

	all, err := f.store.ListMail(ListMailOpts{})
	if err != nil || len(all) != 3 {
		t.Fatalf("list all err=%v n=%d", err, len(all))
	}
	if all[0].ID != other.ID {
		t.Fatalf("default order is not newest first: %+v", all)
	}
	oldest, err := f.store.ListMail(ListMailOpts{OldestFirst: true})
	if err != nil || oldest[0].ID != first.ID || oldest[2].ID != other.ID {
		t.Fatalf("oldest first err=%v list=%+v", err, oldest)
	}
	byRecipient, err := f.store.ListMail(ListMailOpts{ToKind: MailKindSession, ToID: "sess-b"})
	if err != nil || len(byRecipient) != 1 || byRecipient[0].ID != first.ID {
		t.Fatalf("by recipient err=%v list=%+v", err, byRecipient)
	}
	bySender, err := f.store.ListMail(ListMailOpts{FromKind: MailKindCaller, FromCaller: "bob"})
	if err != nil || len(bySender) != 1 || bySender[0].ID != other.ID {
		t.Fatalf("by sender err=%v list=%+v", err, bySender)
	}
	byThread, err := f.store.ListMail(ListMailOpts{ThreadID: first.ThreadID, OldestFirst: true})
	if err != nil || len(byThread) != 2 || byThread[0].ID != first.ID || byThread[1].ID != reply.ID {
		t.Fatalf("by thread err=%v list=%+v", err, byThread)
	}
	byState, err := f.store.ListMail(ListMailOpts{States: []string{MailPending, MailHeld}})
	if err != nil || len(byState) != 3 {
		t.Fatalf("by state err=%v list=%+v", err, byState)
	}
	byTime, err := f.store.ListMail(ListMailOpts{Since: rootCreated + 1, Until: other.CreatedAt, OldestFirst: true})
	if err != nil || len(byTime) != 1 || byTime[0].ID != reply.ID {
		t.Fatalf("by time err=%v list=%+v", err, byTime)
	}
	// BeforeID is the "older than this one" cursor of a paged conversation.
	byCursor, err := f.store.ListMail(ListMailOpts{BeforeID: other.ID, OldestFirst: true})
	if err != nil || len(byCursor) != 2 || byCursor[0].ID != first.ID || byCursor[1].ID != reply.ID {
		t.Fatalf("before cursor err=%v list=%+v", err, byCursor)
	}
	if none, err := f.store.ListMail(ListMailOpts{BeforeID: first.ID}); err != nil || len(none) != 0 {
		t.Fatalf("before the first letter err=%v list=%+v", err, none)
	}
	page, err := f.store.ListMail(ListMailOpts{Limit: 1, Offset: 1})
	if err != nil || len(page) != 1 || page[0].ID != reply.ID {
		t.Fatalf("page err=%v list=%+v", err, page)
	}
	if _, err := f.store.ListMail(ListMailOpts{States: []string{"someday"}}); !errors.Is(err, ErrMailInvalid) {
		t.Fatalf("unknown state filter err=%v, want ErrMailInvalid", err)
	}
}

func TestMailPendingCountAndThreadCount(t *testing.T) {
	f := newMailFixture(t)
	a, err := f.store.InsertMail(sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.InsertMail(sampleMail()); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.store.ClaimMail(a.ID, "hook", 0); err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	// A letter in flight is still unread.
	if n, err := f.store.PendingCount(MailKindSession, "sess-b"); err != nil || n != 2 {
		t.Fatalf("pending count n=%d err=%v, want 2", n, err)
	}
	if _, ok, err := f.store.ConfirmMail(a.ID); err != nil || !ok {
		t.Fatalf("confirm ok=%v err=%v", ok, err)
	}
	if n, err := f.store.PendingCount(MailKindSession, "sess-b"); err != nil || n != 1 {
		t.Fatalf("pending count after delivery n=%d err=%v, want 1", n, err)
	}
	if n, err := f.store.PendingCount(MailKindSession, "nobody"); err != nil || n != 0 {
		t.Fatalf("pending count for an unknown session n=%d err=%v", n, err)
	}
	if n, err := f.store.CountThreadMail(a.ThreadID); err != nil || n != 1 {
		t.Fatalf("thread count n=%d err=%v, want 1", n, err)
	}
	if n, err := f.store.CountMailFrom(MailKindSession, "sess-a", "alice", 0); err != nil || n != 2 {
		t.Fatalf("count from sender n=%d err=%v, want 2", n, err)
	}
	if n, err := f.store.CountMailFrom(MailKindCaller, "", "alice", f.now.Unix()+1); err != nil || n != 0 {
		t.Fatalf("count from sender after now n=%d err=%v, want 0", n, err)
	}
}

// TestMailExpireOverdue: a letter past its delivery deadline is parked as held (设计
// 「过期转待指派」), one still inside its deadline is not.
func TestMailExpireOverdue(t *testing.T) {
	f := newMailFixture(t)
	due, err := f.store.InsertMail(sampleMail(func(in *MailInput) { in.DeliverBy = f.now.Unix() + 10 }))
	if err != nil {
		t.Fatal(err)
	}
	inflight, err := f.store.InsertMail(sampleMail(func(in *MailInput) { in.DeliverBy = f.now.Unix() + 10 }))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.store.ClaimMail(inflight.ID, "hook", DefaultMailClaimTTL); err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	later, err := f.store.InsertMail(sampleMail(func(in *MailInput) { in.DeliverBy = f.now.Unix() + 3600 }))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.store.HoldMail(later.ID, "already parked"); err != nil || !ok {
		t.Fatalf("hold ok=%v err=%v", ok, err)
	}

	f.advance(11 * time.Second)
	overdue, err := f.store.ExpireOverdue(f.now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(overdue) != 2 {
		t.Fatalf("expired %d letters (%+v), want the 2 overdue ones", len(overdue), overdue)
	}
	for _, m := range overdue {
		if m.State != MailHeld || m.LastError != mailOverdueReason || m.ClaimUntil != 0 {
			t.Fatalf("overdue letter=%+v, want held with the deadline reason", m)
		}
	}
	for _, id := range []int64{due.ID, inflight.ID} {
		if m, _, _ := f.store.GetMail(id); m.State != MailHeld {
			t.Fatalf("letter %d = %+v, want held", id, m)
		}
	}
	// The sweep is idempotent and leaves the letter in its deadline's state.
	if again, err := f.store.ExpireOverdue(f.now.Unix()); err != nil || len(again) != 0 {
		t.Fatalf("second sweep expired %d err=%v", len(again), err)
	}
	if m, _, _ := f.store.GetMail(later.ID); m.State != MailHeld || m.LastError != "already parked" {
		t.Fatalf("an unrelated held letter was touched: %+v", m)
	}
}

func TestMailPruneRemovesOldLetters(t *testing.T) {
	f := newMailFixture(t)
	old, err := f.store.InsertMail(sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	// The retention sweep deletes TERMINAL letters (M8): this one was read, which is delivery.
	if _, _, err := f.store.ClaimMail(old.ID, "hook", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.ConfirmMail(old.ID); err != nil {
		t.Fatal(err)
	}
	f.advance(91 * 24 * time.Hour)
	fresh, err := f.store.InsertMail(sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	cut := f.now.Add(-90 * 24 * time.Hour).Unix()
	n, err := f.store.PruneMail(cut)
	if err != nil || n != 1 {
		t.Fatalf("prune n=%d err=%v, want 1", n, err)
	}
	if _, ok, _ := f.store.GetMail(old.ID); ok {
		t.Fatal("the 91-day-old letter survived the 90-day retention")
	}
	if _, ok, _ := f.store.GetMail(fresh.ID); !ok {
		t.Fatal("the fresh letter was pruned")
	}
	// The capture-box reference is a foreign id: pruning letters never travels into it.
	if _, err := f.store.PruneMail(cut); err != nil {
		t.Fatal(err)
	}
}

// TestMailTableIsCreatedOnAnExistingDatabase is the migration case: a database written
// before the mailbox existed gains the table (and its indexes) on the next Open, and keeps
// letters written after it.
func TestMailTableIsCreatedOnAnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE mail`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	m, err := reopened.InsertMail(sampleMail())
	if err != nil {
		t.Fatalf("insert into the migrated table: %v", err)
	}
	cols, err := reopened.tableColumns("mail")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"thread_id", "reply_to", "from_kind", "from_id", "from_caller",
		"to_address", "to_kind", "to_id", "to_caller", "job_id", "urgent", "body", "state",
		"claimed_by", "claim_until", "attempts", "last_error", "created_at", "delivered_at",
		"read_at", "deliver_by", "capture_item_id"} {
		if _, ok := cols[c]; !ok {
			t.Fatalf("column %q missing after the migration: %v", c, cols)
		}
	}
	got, ok, err := reopened.GetMail(m.ID)
	if err != nil || !ok || got.State != MailPending {
		t.Fatalf("letter after the migration ok=%v err=%v mail=%+v", ok, err, got)
	}
}

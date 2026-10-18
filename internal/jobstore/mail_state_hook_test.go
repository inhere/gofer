package jobstore

import (
	"testing"
	"time"
)

// The letter transition observer (SetMailStateHook): the capture box's writeback hangs on it, so
// the store has to report every REAL transition with both ends and nothing else.

func TestMailStateHookReportsEveryRealTransition(t *testing.T) {
	f := newMailFixture(t)
	type move struct {
		from, to string
	}
	var got []move
	f.store.SetMailStateHook(func(prev, next Mail) {
		got = append(got, move{from: prev.State, to: next.State})
	})
	m, err := f.store.InsertMail(sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("an insert is not a transition: %+v", got)
	}
	if _, ok, err := f.store.ClaimMail(m.ID, "hook:sess-b", DefaultMailClaimTTL); err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if _, ok, err := f.store.ConfirmMail(m.ID); err != nil || !ok {
		t.Fatalf("confirm ok=%v err=%v", ok, err)
	}
	// A read stamp on an already delivered letter changes read_at, not the state: no report.
	if _, ok, err := f.store.MarkMailRead(m.ID); err != nil || !ok {
		t.Fatalf("read ok=%v err=%v", ok, err)
	}
	if _, ok, err := f.store.MarkMailAnswered(m.ID); err != nil || !ok {
		t.Fatalf("answer ok=%v err=%v", ok, err)
	}
	// A refused transition reports nothing either.
	if _, ok, err := f.store.ConfirmMail(m.ID); err != nil || ok {
		t.Fatalf("second confirm ok=%v err=%v", ok, err)
	}
	want := []move{{MailPending, MailDelivering}, {MailDelivering, MailDelivered}, {MailDelivered, MailAnswered}}
	if len(got) != len(want) {
		t.Fatalf("transitions = %+v, want %+v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("transition %d = %+v, want %+v", i, got[i], w)
		}
	}
	// The hook sees the letter AFTER the move: its carried item and state are the new ones.
	f.store.SetMailStateHook(nil)
	var last Mail
	f.store.SetMailStateHook(func(_, next Mail) { last = next })
	if _, ok, err := f.store.HoldMail(m.ID, "why"); err != nil || ok {
		t.Fatalf("hold from answered ok=%v err=%v", ok, err)
	}
	if last.ID != 0 {
		t.Fatalf("an illegal move reported %+v", last)
	}
}

func TestMailStateHookSeesTheCaptureItem(t *testing.T) {
	f := newMailFixture(t)
	m, err := f.store.InsertMail(sampleMail(func(in *MailInput) { in.CaptureItemID = "wi-1" }))
	if err != nil {
		t.Fatal(err)
	}
	if m.CaptureItemID != "wi-1" {
		t.Fatalf("inserted letter = %+v, want the capture item on it", m)
	}
	var seen []Mail
	f.store.SetMailStateHook(func(_, next Mail) { seen = append(seen, next) })
	if _, ok, err := f.store.ClaimMail(m.ID, "deliverer", time.Minute); err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if _, ok, err := f.store.ConfirmMail(m.ID); err != nil || !ok {
		t.Fatalf("confirm ok=%v err=%v", ok, err)
	}
	if len(seen) != 2 || seen[0].CaptureItemID != "wi-1" || seen[1].CaptureItemID != "wi-1" || seen[1].State != MailDelivered {
		t.Fatalf("transitions = %+v, want claim + confirm of the item's letter", seen)
	}

	// A refused move reports nothing: a held letter cannot be read.
	held, err := f.store.InsertMail(sampleMail(func(in *MailInput) {
		in.ToKind, in.ToID, in.ToAddress = "", "", "topic:nobody"
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.store.HoldMail(held.ID, "no candidate"); err != nil || !ok {
		t.Fatalf("hold ok=%v err=%v", ok, err)
	}
	seen = nil
	if _, ok, err := f.store.MarkMailRead(held.ID); err != nil || ok {
		t.Fatalf("reading a held letter ok=%v err=%v", ok, err)
	}
	if len(seen) != 0 {
		t.Fatalf("a refused move reported %+v", seen)
	}
}

package jobstore

import (
	"testing"
	"time"
)

func TestClaimSessionMailOrderLeaseAndConfirm(t *testing.T) {
	f := newMailFixture(t)
	plain, _ := f.store.InsertMail(sampleMail())
	hot, _ := f.store.InsertMail(sampleMail(func(in *MailInput) { in.Urgent = true }))
	other, _ := f.store.InsertMail(sampleMail(func(in *MailInput) { in.ToID, in.ToAddress = "sess-x", "session:sess-x" }))

	taken, rem, err := f.store.ClaimSessionMail("sess-b", TakeMailOpts{Limit: 1, TTL: time.Minute})
	if err != nil || len(taken) != 1 || taken[0].ID != hot.ID || rem != 1 {
		t.Fatalf("taken=%+v rem=%d err=%v, want the urgent letter first and one left", taken, rem, err)
	}
	if got, _, _ := f.store.ClaimSessionMail("sess-b", TakeMailOpts{Limit: 5, UrgentOnly: true}); len(got) != 0 {
		t.Fatalf("urgent-only take found %v, the only urgent letter is claimed", got)
	}
	// The lease runs out: the next take sweeps the stale claim first and gets both letters.
	f.advance(2 * time.Minute)
	taken, rem, err = f.store.ClaimSessionMail("sess-b", TakeMailOpts{Limit: 5})
	if err != nil || len(taken) != 2 || taken[0].ID != hot.ID || taken[1].ID != plain.ID || rem != 0 {
		t.Fatalf("after the lease taken=%+v rem=%d err=%v", taken, rem, err)
	}
	if taken[0].Attempts != 2 {
		t.Fatalf("attempts=%d, want 2 (every claim counts)", taken[0].Attempts)
	}
	// Only the holder session confirms; another session's letter is never touched.
	if got, _ := f.store.ConfirmSessionMail("sess-other", []int64{hot.ID}); len(got) != 0 {
		t.Fatalf("confirmed by a stranger: %v", got)
	}
	got, err := f.store.ConfirmSessionMail("sess-b", []int64{hot.ID, plain.ID, other.ID})
	if err != nil || len(got) != 2 {
		t.Fatalf("confirm=%v err=%v", got, err)
	}
	if m, _, _ := f.store.GetMail(other.ID); m.State != MailPending {
		t.Fatalf("other session's letter=%+v, want untouched", m)
	}
	// Release hands a claim back; confirmed letters cannot be released.
	more, _ := f.store.InsertMail(sampleMail())
	if tk, _, _ := f.store.ClaimSessionMail("sess-b", TakeMailOpts{}); len(tk) != 1 {
		t.Fatal("claim failed")
	}
	if err := f.store.ReleaseSessionMail("sess-b", []int64{more.ID, hot.ID}); err != nil {
		t.Fatal(err)
	}
	if m, _, _ := f.store.GetMail(more.ID); m.State != MailPending || m.ClaimedBy != "" {
		t.Fatalf("released=%+v", m)
	}
	if m, _, _ := f.store.GetMail(hot.ID); m.State != MailDelivered {
		t.Fatalf("a delivered letter must stay delivered: %+v", m)
	}
}

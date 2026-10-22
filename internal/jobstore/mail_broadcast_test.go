package jobstore

import (
	"testing"
	"time"
)

// A broadcast copy past its delivery deadline only expires: the sweep must NOT hand a dozen
// 「待指派」 items to a person for one undelivered notice (设计 2026-10-22「D3」).
func TestMailExpireOverdueBroadcastCopyExpires(t *testing.T) {
	f := newMailFixture(t)

	m, err := f.store.InsertMail(MailInput{
		FromKind: MailKindSession, FromID: "sess-a",
		ToAddress: "each:project:p1", ToKind: MailKindSession, ToID: "sess-b",
		Body: "群发", DeliverBy: f.now.Unix() + 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := f.store.SetMailBroadcastID(m.ID, m.ID); err != nil || !ok {
		t.Fatalf("stamp broadcast: ok=%v err=%v", ok, err)
	}
	f.now = f.now.Add(time.Hour)
	held, err := f.store.ExpireOverdue(f.now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 0 {
		t.Fatalf("the sweep parked %d broadcast copies in the capture box, want none", len(held))
	}
	got, ok, err := f.store.GetMail(m.ID)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.State != MailExpired {
		t.Fatalf("state = %s, want expired", got.State)
	}
	// A single letter keeps the sweep's 「待指派」 answer.
	single, err := f.store.InsertMail(MailInput{
		FromKind: MailKindSession, FromID: "sess-a", ToKind: MailKindSession, ToID: "sess-c",
		Body: "单发", DeliverBy: f.now.Unix() + 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Hour)
	held, err = f.store.ExpireOverdue(f.now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 || held[0].ID != single.ID || held[0].State != MailHeld {
		t.Fatalf("single letter sweep = %+v, want it held", held)
	}
}

// The broadcast helpers: the group count and the per-broadcast rate counting.
func TestMailBroadcastCounters(t *testing.T) {
	f := newMailFixture(t)
	first, err := f.store.InsertMail(MailInput{FromKind: MailKindCaller, FromCaller: "alice",
		ToAddress: "each:topic:x", ToKind: MailKindSession, ToID: "sess-a", Body: "群发 1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.store.InsertMail(MailInput{FromKind: MailKindCaller, FromCaller: "alice",
		ToAddress: "each:topic:x", ToKind: MailKindSession, ToID: "sess-b", Body: "群发 1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{first.ID, second.ID} {
		if ok, err := f.store.SetMailBroadcastID(id, first.ID); err != nil || !ok {
			t.Fatalf("stamp %d: ok=%v err=%v", id, ok, err)
		}
	}
	if n, err := f.store.CountBroadcastMail(first.ID); err != nil || n != 2 {
		t.Fatalf("count = %d, %v; want 2", n, err)
	}
	if n, err := f.store.CountBroadcastFrom(MailKindCaller, "", "alice", 0); err != nil || n != 1 {
		t.Fatalf("broadcasts from alice = %d, %v; want 1", n, err)
	}
	rows, err := f.store.ListMail(ListMailOpts{BroadcastID: first.ID, Limit: 10})
	if err != nil || len(rows) != 2 {
		t.Fatalf("list by broadcast = %d, %v; want 2", len(rows), err)
	}
}

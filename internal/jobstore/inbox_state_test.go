package jobstore

import (
	"testing"
	"time"
)

func TestInboxItemStateIsCallerIsolatedAndIdempotent(t *testing.T) {
	s := openTest(t)
	now := time.Unix(100, 0)
	if err := s.SetInboxItemState("alice", "review:j1", "action", "rev-1", "seen", now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetInboxItemState("alice", "review:j1", "action", "rev-1", "clear", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	alice, err := s.ListInboxItemStates("alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.ListInboxItemStates("bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(alice) != 1 || alice["review:j1"].ClearedAt == nil || alice["review:j1"].SeenAt == nil {
		t.Fatalf("alice state = %+v", alice)
	}
	if len(bob) != 0 {
		t.Fatalf("bob saw alice state: %+v", bob)
	}
	if err := s.SetInboxItemState("alice", "review:j1", "action", "rev-2", "restore", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	alice, _ = s.ListInboxItemStates("alice")
	if alice["review:j1"].SourceRevision != "rev-2" || alice["review:j1"].ClearedAt != nil {
		t.Fatalf("restored state = %+v", alice["review:j1"])
	}
}

package jobstore

import (
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/tracker"
)

// TestTrackerSyncClients: each (tracker, host, caller) keeps one row with its latest
// sync; the list is per tracker, newest first, and an empty host is "unknown".
func TestTrackerSyncClients(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g := tracker.GlobalTrackerID
	for _, c := range []TrackerSyncClient{
		{TrackerID: g, ClientHost: "host-a", Caller: "alice", LastSyncAt: 100, Summary: "first"},
		{TrackerID: g, ClientHost: "host-b", Caller: "alice", LastSyncAt: 200, Summary: "b"},
		{TrackerID: g, ClientHost: "host-a", Caller: "alice", LastSyncAt: 300, Summary: "again"},
		{TrackerID: g, ClientHost: "", Caller: "bob", LastSyncAt: 150, Summary: "no host"},
		{TrackerID: "repo-x", ClientHost: "host-a", Caller: "alice", LastSyncAt: 400, Summary: "repo"},
	} {
		if err := s.RecordTrackerSyncClient(c); err != nil {
			t.Fatalf("record %+v: %v", c, err)
		}
	}
	got, err := s.ListTrackerSyncClients(g)
	if err != nil {
		t.Fatal(err)
	}
	want := []TrackerSyncClient{
		{TrackerID: g, ClientHost: "host-a", Caller: "alice", LastSyncAt: 300, Summary: "again"},
		{TrackerID: g, ClientHost: "host-b", Caller: "alice", LastSyncAt: 200, Summary: "b"},
		{TrackerID: g, ClientHost: UnknownSyncClientHost, Caller: "bob", LastSyncAt: 150, Summary: "no host"},
	}
	if len(got) != len(want) {
		t.Fatalf("clients = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("clients[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if none, err := s.ListTrackerSyncClients("never-synced"); err != nil || none == nil || len(none) != 0 {
		t.Fatalf("unknown tracker = %#v, %v; want empty non-nil list", none, err)
	}
	if err := s.RecordTrackerSyncClient(TrackerSyncClient{ClientHost: "h"}); err == nil {
		t.Fatal("record without tracker_id must fail")
	}
}

// TestTrackerSyncClientsFollowRenameAndDelete: the per-machine rows move with a
// renamed repository and go with a deleted one.
func TestTrackerSyncClientsFollowRenameAndDelete(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedTracker(t, s, "old")
	if err := s.RecordTrackerSyncClient(TrackerSyncClient{TrackerID: "old", ClientHost: "h", Caller: "c", LastSyncAt: 1}); err != nil {
		t.Fatal(err)
	}
	if renamed, err := s.RenameTracker("old", "new"); err != nil || !renamed {
		t.Fatalf("rename = %v, %v", renamed, err)
	}
	if old, _ := s.ListTrackerSyncClients("old"); len(old) != 0 {
		t.Fatalf("old id keeps clients: %+v", old)
	}
	if moved, _ := s.ListTrackerSyncClients("new"); len(moved) != 1 || moved[0].ClientHost != "h" {
		t.Fatalf("new id clients = %+v", moved)
	}
	if _, err := s.DeleteTracker("new"); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.ListTrackerSyncClients("new"); len(left) != 0 {
		t.Fatalf("deleted tracker keeps clients: %+v", left)
	}
}

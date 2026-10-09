package jobstore

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// gofer-dgsm: a pushed record carries the server rev the client last saw; a
// stale rev is not written and the current record comes back as a conflict.
func TestSyncTrackerRecordRevSemantics(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rec := func(title string) TrackerRecord {
		b, _ := json.Marshal(map[string]any{"title": title})
		return TrackerRecord{TrackerID: "t", ID: "a", Body: b, UpdatedAt: "2026-01-01T00:00:00Z"}
	}
	res, err := s.SyncTrackerIssue(rec("v1"), 0)
	if err != nil || res.Conflict || res.Rec.Rev != 1 {
		t.Fatalf("insert: %+v %v", res, err)
	}
	res, err = s.SyncTrackerIssue(rec("v2"), 1)
	if err != nil || res.Conflict || res.Rec.Rev != 2 {
		t.Fatalf("fresh write: %+v %v", res, err)
	}
	// An old-style client pushes rev 1 again: conflict, nothing written.
	res, err = s.SyncTrackerIssue(rec("stale"), 1)
	if err != nil || !res.Conflict || res.Rec.Rev != 2 || string(res.Rec.Body) != `{"title":"v2"}` {
		t.Fatalf("stale write: %+v %v", res, err)
	}
	// Client ahead of the server (server reset): accepted as stored rev + 1.
	res, err = s.SyncTrackerIssue(rec("ahead"), 9)
	if err != nil || res.Conflict || !res.Ahead || res.Rec.Rev != 3 {
		t.Fatalf("ahead write: %+v %v", res, err)
	}
	items, _ := s.ListTrackerIssues("t", 0)
	if len(items) != 1 || items[0].Rev != 3 || string(items[0].Body) != `{"title":"ahead"}` {
		t.Fatalf("stored=%+v", items)
	}
	// Memory tombstone conflicts carry the tombstone fields.
	m := TrackerRecord{TrackerID: "t", ID: "k", Body: json.RawMessage(`{}`), Deleted: true, DeletedAt: "2026-10-07T00:00:00Z", DeletedBy: "web"}
	if res, err = s.SyncTrackerMemory(m, 0); err != nil || res.Rec.Rev != 1 {
		t.Fatalf("tombstone insert: %+v %v", res, err)
	}
	m.Deleted, m.Body = false, json.RawMessage(`{"key":"k"}`)
	res, err = s.SyncTrackerMemory(m, 0)
	if err != nil || !res.Conflict || !res.Rec.Deleted || res.Rec.DeletedAt != "2026-10-07T00:00:00Z" {
		t.Fatalf("tombstone conflict: %+v %v", res, err)
	}
}

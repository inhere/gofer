package jobstore

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestTrackerCursorDoesNotSkipLowerRevUpdates(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertTrackerRepo(TrackerRepo{TrackerID: "t"}); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"title": "a"})
	if err := s.UpsertTrackerIssue(TrackerRecord{TrackerID: "t", ID: "a", Body: b, Rev: 5, UpdatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(map[string]any{"title": "b"})
	if err := s.UpsertTrackerIssue(TrackerRecord{TrackerID: "t", ID: "b", Body: b, Rev: 2, UpdatedAt: "2026-01-01T00:00:01Z"}); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListTrackerIssues("t", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "b" || items[0].ChangedSeq != 2 {
		t.Fatalf("items=%+v", items)
	}
}

package jobstore

import (
	"errors"
	"path/filepath"
	"testing"
)

func seedTracker(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := s.UpsertTrackerRepo(TrackerRepo{TrackerID: id, Prefix: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTrackerIssue(TrackerRecord{TrackerID: id, ID: "i1", Body: []byte(`{}`), Rev: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTrackerMemory(TrackerRecord{TrackerID: id, ID: "m1", Body: []byte(`{}`), Rev: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestRenameTracker(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedTracker(t, s, "old")
	renamed, err := s.RenameTracker("old", "new")
	if err != nil || !renamed {
		t.Fatalf("renamed=%v err=%v", renamed, err)
	}
	repos, _ := s.ListTrackerRepos()
	if len(repos) != 1 || repos[0].TrackerID != "new" || repos[0].Prefix != "p" {
		t.Fatalf("repos=%+v", repos)
	}
	if is, _ := s.ListTrackerIssues("new", 0); len(is) != 1 {
		t.Fatalf("issues=%v", is)
	}
	if ms, _ := s.ListTrackerMemories("new", 0); len(ms) != 1 {
		t.Fatalf("memories=%v", ms)
	}
	if is, _ := s.ListTrackerIssues("old", 0); len(is) != 0 {
		t.Fatal("old issues remain")
	}
	// Idempotent: old absent -> no-op; neither -> no-op.
	if renamed, err = s.RenameTracker("old", "new"); err != nil || renamed {
		t.Fatalf("repeat renamed=%v err=%v", renamed, err)
	}
	if renamed, err = s.RenameTracker("x", "y"); err != nil || renamed {
		t.Fatalf("neither renamed=%v err=%v", renamed, err)
	}
	// Both exist -> conflict, nothing changes.
	seedTracker(t, s, "old2")
	if _, err = s.RenameTracker("old2", "new"); !errors.Is(err, ErrTrackerRenameConflict) {
		t.Fatalf("err=%v, want conflict", err)
	}
	if is, _ := s.ListTrackerIssues("old2", 0); len(is) != 1 {
		t.Fatal("conflict must not modify old")
	}
}

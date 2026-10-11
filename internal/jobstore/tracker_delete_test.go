package jobstore

import (
	"path/filepath"
	"testing"
)

// TestDeleteTracker: a mirror can be removed from the server (gofer-gtom) — its
// repo row, issues, memories and memory suggestions go; other mirrors stay.
func TestDeleteTracker(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedTracker(t, s, "gone")
	seedTracker(t, s, "kept")
	if _, _, err := s.AddMemorySuggestion(MemorySuggestion{TrackerID: "gone", MemoryKey: "m1", Action: "update", PayloadJSON: `{}`, By: "steward"}); err != nil {
		t.Fatal(err)
	}
	res, err := s.DeleteTracker("gone")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Repo || res.Issues != 1 || res.Memories != 1 || res.Suggestions != 1 {
		t.Fatalf("result = %+v", res)
	}
	repos, _ := s.ListTrackerRepos()
	if len(repos) != 1 || repos[0].TrackerID != "kept" {
		t.Fatalf("repos after delete = %+v", repos)
	}
	if is, _ := s.ListTrackerIssues("gone", 0); len(is) != 0 {
		t.Fatalf("issues left = %v", is)
	}
	if ms, _ := s.ListTrackerMemories("kept", 0); len(ms) != 1 {
		t.Fatal("other mirror lost its memories")
	}
	res, err = s.DeleteTracker("gone")
	if err != nil || !res.Empty() {
		t.Fatalf("repeat delete = %+v, %v", res, err)
	}
}

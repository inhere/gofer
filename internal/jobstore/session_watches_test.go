package jobstore

import (
	"path/filepath"
	"testing"
)

func TestSessionJobWatchesRoundTripAndCleanup(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.UpsertAgentSession(AgentSession{SessionID: "sid-watch", Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	first, err := s.AddSessionJobWatch("sid-watch", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.AddSessionJobWatch("sid-watch", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Fatalf("idempotent add changed row: first=%+v again=%+v", first, again)
	}
	if _, err := s.AddSessionJobWatch("sid-watch", "job-2"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListSessionJobWatches("sid-watch")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].JobID != "job-1" || got[1].JobID != "job-2" {
		t.Fatalf("watches=%+v", got)
	}
	if ok, err := s.DeleteAgentSession("sid-watch"); err != nil || !ok {
		t.Fatalf("delete session ok=%v err=%v", ok, err)
	}
	left, err := s.ListSessionJobWatches("sid-watch")
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("watches after session delete=%+v", left)
	}
}

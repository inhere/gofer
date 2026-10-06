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

func TestClaimUnwatchedCallerJobs(t *testing.T) {
	s := openTest(t)
	const now = int64(1_700_000_000)
	mk := func(id, caller, status string, startedAt int64) {
		j := sampleJob(id, "proj", startedAt)
		j.CallerID, j.Status = caller, status
		if err := s.UpsertJob(j); err != nil {
			t.Fatal(err)
		}
	}
	mk("live", "alice", "running", now-10)
	mk("watched", "alice", "running", now-10)
	mk("done", "alice", "done", now-10)
	mk("old", "alice", "running", now-9000)
	mk("bobs", "bob", "running", now-10)
	for _, sid := range []string{"s1", "s2"} {
		if _, err := s.UpsertAgentSession(AgentSession{SessionID: sid, Agent: "claude"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddSessionJobWatch("s2", "watched"); err != nil {
		t.Fatal(err)
	}
	n, err := s.ClaimUnwatchedCallerJobs("s1", "alice", now-7200)
	if err != nil || n != 1 {
		t.Fatalf("claimed n=%d err=%v", n, err)
	}
	got, _ := s.ListSessionJobWatches("s1")
	if len(got) != 1 || got[0].JobID != "live" {
		t.Fatalf("s1 watches=%+v", got)
	}
	if n, _ = s.ClaimUnwatchedCallerJobs("s1", "alice", now-7200); n != 0 {
		t.Fatalf("second claim n=%d", n)
	}
	if n, _ = s.ClaimUnwatchedCallerJobs("s1", "", 0); n != 0 {
		t.Fatalf("empty caller claimed %d", n)
	}
}

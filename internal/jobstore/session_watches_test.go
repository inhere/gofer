package jobstore

import (
	"path/filepath"
	"sync"
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

func TestClaimUnwatchedSourceJobsWindowAndExistingWatch(t *testing.T) {
	s := openTest(t)
	const now = int64(1_700_000_000)
	cwd := filepath.Clean(t.TempDir())
	mk := func(id, caller, status string, startedAt int64) {
		j := sampleJob(id, "proj", startedAt)
		j.CallerID, j.Status, j.SourceSessionID, j.Cwd = caller, status, "s1", cwd
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
		if _, err := s.UpsertAgentSession(AgentSession{SessionID: sid, Agent: "claude", ProjectKey: "proj", Runner: "local", Cwd: cwd, CallerID: "alice"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddSessionJobWatch("s2", "watched"); err != nil {
		t.Fatal(err)
	}
	n, err := s.ClaimUnwatchedSourceJobs("s1", now-7200)
	if err != nil || n != 1 {
		t.Fatalf("claimed n=%d err=%v", n, err)
	}
	got, _ := s.ListSessionJobWatches("s1")
	if len(got) != 1 || got[0].JobID != "live" {
		t.Fatalf("s1 watches=%+v", got)
	}
	if n, _ = s.ClaimUnwatchedSourceJobs("s1", now-7200); n != 0 {
		t.Fatalf("second claim n=%d", n)
	}
	if n, _ = s.ClaimUnwatchedSourceJobs("", 0); n != 0 {
		t.Fatalf("empty session claimed %d", n)
	}
}

func TestClaimUnwatchedSourceJobsExactSessionAndContext(t *testing.T) {
	s := openTest(t)
	root := filepath.Clean(t.TempDir())
	const now = int64(1_700_000_000)
	for _, sess := range []AgentSession{
		{SessionID: "source-a", Agent: "suag", ProjectKey: "project-a", Runner: "server", Cwd: root, CallerID: "alice"},
		{SessionID: "source-b", Agent: "suag", ProjectKey: "project-a", Runner: "local", Cwd: root, CallerID: "alice"},
		{SessionID: "source-ownerless", Agent: "suag", ProjectKey: "project-a", Runner: "local", Cwd: root},
	} {
		if _, err := s.UpsertAgentSession(sess); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(id, source, caller, project, runner, cwd, status string, started int64) {
		j := sampleJob(id, project, started)
		j.SourceSessionID, j.CallerID, j.Runner, j.Cwd, j.Status = source, caller, runner, cwd, status
		if err := s.UpsertJob(j); err != nil {
			t.Fatal(err)
		}
	}
	mk("job-a-match", "source-a", "alice", "project-a", "local", root, "running", now-10)
	mk("job-b-match", "source-b", "alice", "project-a", "local", root, "awaiting_input", now-10)
	mk("job-legacy-no-source", "", "alice", "project-a", "local", root, "running", now-10)
	mk("job-agent-target-only", "", "alice", "project-a", "local", root, "running", now-10)
	agentTarget, ok, err := s.GetJob("job-agent-target-only")
	if err != nil || !ok {
		t.Fatalf("load agent target job: ok=%v err=%v", ok, err)
	}
	agentTarget.SessionID = "source-a" // agent target SID never proves submit-source ownership
	if err := s.UpsertJob(agentTarget); err != nil {
		t.Fatal(err)
	}
	mk("job-foreign-caller", "source-a", "bob", "project-a", "local", root, "running", now-10)
	mk("job-wrong-project", "source-a", "alice", "project-b", "local", root, "running", now-10)
	mk("job-wrong-runner", "source-a", "alice", "project-a", "worker-x", root, "running", now-10)
	mk("job-wrong-cwd", "source-a", "alice", "project-a", "local", filepath.Join(root, "sub"), "running", now-10)
	mk("job-relative-cwd", "source-a", "alice", "project-a", "local", ".", "running", now-10)
	mk("job-terminal", "source-a", "alice", "project-a", "local", root, "done", now-10)
	mk("job-too-old", "source-a", "alice", "project-a", "local", root, "running", now-9000)
	if _, err := s.AddSessionJobWatch("source-b", "job-b-match"); err != nil {
		t.Fatal(err)
	}

	claimed, err := s.ClaimUnwatchedSourceJobs("source-a", now-7200)
	if err != nil || claimed != 1 {
		t.Fatalf("source-a claimed=%d err=%v", claimed, err)
	}
	watches, err := s.ListSessionJobWatches("source-a")
	if err != nil || len(watches) != 1 || watches[0].JobID != "job-a-match" {
		t.Fatalf("source-a watches=%+v err=%v", watches, err)
	}
	claimed, err = s.ClaimUnwatchedSourceJobs("source-b", now-7200)
	if err != nil || claimed != 0 {
		t.Fatalf("source-b claimed already-watched job=%d err=%v", claimed, err)
	}
	for _, missing := range []string{"missing-source", "source-ownerless", ""} {
		if n, err := s.ClaimUnwatchedSourceJobs(missing, 0); err != nil || n != 0 {
			t.Fatalf("session %q claimed=%d err=%v, want fail-closed zero", missing, n, err)
		}
	}
}

func TestClaimUnwatchedSourceJobsConcurrentSessionsStayDisjoint(t *testing.T) {
	s := openTest(t)
	root := filepath.Clean(t.TempDir())
	const now = int64(1_700_000_000)
	for _, sid := range []string{"parallel-a", "parallel-b"} {
		if _, err := s.UpsertAgentSession(AgentSession{SessionID: sid, Agent: "suag", ProjectKey: "project-a", Runner: "local", Cwd: root, CallerID: "same-caller"}); err != nil {
			t.Fatal(err)
		}
		j := sampleJob("job-"+sid, "project-a", now-1)
		j.CallerID, j.Runner, j.Cwd, j.SourceSessionID, j.Status = "same-caller", "local", root, sid, "running"
		if err := s.UpsertJob(j); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	counts := make(map[string]int64)
	var mu sync.Mutex
	errs := make(chan error, 2)
	for _, sid := range []string{"parallel-a", "parallel-b"} {
		wg.Add(1)
		go func(sid string) {
			defer wg.Done()
			n, err := s.ClaimUnwatchedSourceJobs(sid, now-100)
			if err != nil {
				errs <- err
				return
			}
			mu.Lock()
			counts[sid] = n
			mu.Unlock()
		}(sid)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for _, sid := range []string{"parallel-a", "parallel-b"} {
		if counts[sid] != 1 {
			t.Fatalf("%s claimed %d, want its single source job", sid, counts[sid])
		}
		watches, err := s.ListSessionJobWatches(sid)
		if err != nil || len(watches) != 1 || watches[0].JobID != "job-"+sid {
			t.Fatalf("%s watches=%+v err=%v", sid, watches, err)
		}
	}
}

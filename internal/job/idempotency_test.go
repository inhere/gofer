package job

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestSubmitIdempotentReuse: two Submits with the SAME request_id return the
// SAME job id (the second reuses the first) and only one result dir exists.
func TestSubmitIdempotentReuse(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	req := JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		RequestID: "idem-1",
	}
	first, err := s.Submit(req)
	if err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	second, err := s.Submit(req)
	if err != nil {
		t.Fatalf("second Submit: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("idempotent submit returned different ids: %s != %s", first.ID, second.ID)
	}
	if second.RequestID != "idem-1" {
		t.Fatalf("reused job missing request_id: %+v", second)
	}

	// Exactly one job dir for this project.
	dirs, err := os.ReadDir(filepath.Join(root, "self"))
	if err != nil {
		t.Fatalf("read project dir: %v", err)
	}
	if len(dirs) != 1 {
		t.Fatalf("expected exactly 1 job dir, got %d: %v", len(dirs), dirs)
	}

	// Exactly one DB row.
	all, err := s.ListJobs(ListOpts{Project: "self"})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 persisted job, got %d", len(all))
	}
	s.Wait(first.ID)
}

// TestSubmitNoRequestIDDistinct: two Submits without a request_id create two
// distinct jobs (no dedup).
func TestSubmitNoRequestIDDistinct(t *testing.T) {
	s := newTestService(t, t.TempDir())
	req := JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	}
	a, err := s.Submit(req)
	if err != nil {
		t.Fatalf("Submit a: %v", err)
	}
	b, err := s.Submit(req)
	if err != nil {
		t.Fatalf("Submit b: %v", err)
	}
	if a.ID == b.ID {
		t.Fatalf("expected distinct ids without request_id, got %s == %s", a.ID, b.ID)
	}
	s.Wait(a.ID)
	s.Wait(b.ID)
}

// TestSubmitConcurrentSameRequestID: many goroutines submitting the SAME
// request_id must converge on exactly ONE surviving job id (unique-index race
// recovery). Run under -race.
func TestSubmitConcurrentSameRequestID(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	const n = 16
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all goroutines together to maximise contention
			res, err := s.Submit(JobRequest{
				ProjectKey: "self", Agent: "exec", Runner: "local",
				Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
				RequestID: "race-key",
			})
			ids[i] = res.ID
			errs[i] = err
		}(i)
	}
	close(start)
	wg.Wait()

	distinct := map[string]struct{}{}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d Submit error: %v", i, errs[i])
		}
		if ids[i] == "" {
			t.Fatalf("goroutine %d got empty job id", i)
		}
		distinct[ids[i]] = struct{}{}
	}
	if len(distinct) != 1 {
		t.Fatalf("expected exactly 1 surviving job id under concurrent same request_id, got %d: %v", len(distinct), distinct)
	}

	// Only one job dir survived (losers cleaned up their dirs).
	dirs, err := os.ReadDir(filepath.Join(root, "self"))
	if err != nil {
		t.Fatalf("read project dir: %v", err)
	}
	if len(dirs) != 1 {
		t.Fatalf("expected exactly 1 job dir after race, got %d: %v", len(dirs), dirs)
	}

	for id := range distinct {
		s.Wait(id)
	}
}

// TestCallerIDPersisted: a request's CallerID flows into the persisted record
// (visible via Get and ListJobs).
func TestCallerIDPersisted(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		CallerID: "alice",
	})
	if final.CallerID != "alice" {
		t.Fatalf("final caller_id=%q, want alice", final.CallerID)
	}
	// Re-read from the metadata store (the in-memory entry is evicted on finish).
	rec, ok, err := s.meta.GetJob(final.ID)
	if err != nil || !ok {
		t.Fatalf("meta.GetJob: ok=%v err=%v", ok, err)
	}
	if rec.CallerID != "alice" {
		t.Fatalf("persisted caller_id=%q, want alice", rec.CallerID)
	}
	// Visible via the caller filter.
	got, err := s.ListJobs(ListOpts{Caller: "alice"})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(got) != 1 || got[0].ID != final.ID {
		t.Fatalf("caller filter returned %+v, want only %s", got, final.ID)
	}
}

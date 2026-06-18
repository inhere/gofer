package job

import (
	"path/filepath"
	"sync"
	"testing"
)

// TestMetadataPersistAndAfterRestart verifies a completed exec job's terminal
// snapshot is persisted into the metadata store, and that a fresh Service opened
// over the SAME db (a simulated restart, empty in-memory map) can still Get the
// job — Get falls back to the DB. This replaces the old jobs.jsonl index test:
// create + terminal are now two upserts on one row (deduplicated), not two
// appended lines.
func TestMetadataPersistAndAfterRestart(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "agent-bridge.db")
	s := newTestServiceWithDB(t, root, dbPath)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: expected done, got %s (err=%s)", final.Status, final.Error)
	}

	// The terminal snapshot is in the DB with the same id, terminal status.
	rec, ok, err := s.meta.GetJob(final.ID)
	if err != nil {
		t.Fatalf("meta.GetJob: %v", err)
	}
	if !ok || rec.ID != final.ID || rec.Status != StatusDone {
		t.Fatalf("expected persisted terminal record, got ok=%v rec=%+v", ok, rec)
	}

	// Fresh Service over the same db: in-memory map is empty, but Get falls back
	// to the metadata store (after-restart semantics).
	fresh := newTestServiceWithDB(t, root, dbPath)
	if len(fresh.jobs) != 0 {
		t.Fatalf("fresh service should start with no in-memory jobs")
	}
	got, found := fresh.Get(final.ID)
	if !found {
		t.Fatalf("fresh service could not Get job %q from db", final.ID)
	}
	if got.Status != StatusDone || got.ExitCode != 0 {
		t.Fatalf("recovered job mismatch: %+v", got)
	}
}

// TestMetadataConcurrentUpsertNoLoss submits N jobs concurrently and asserts the
// metadata store ends with exactly N rows (one per job id), proving the WAL +
// busy_timeout write path tolerates concurrent upserts without losing a job. The
// old jobs.jsonl test asserted 2N appended lines; the DB deduplicates create +
// terminal onto one row per id, so the invariant is N rows.
func TestMetadataConcurrentUpsertNoLoss(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	const n = 12
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			res, err := s.Submit(JobRequest{
				ProjectKey: "self", Agent: "exec", Runner: "local",
				Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
			})
			if err != nil {
				t.Errorf("Submit: %v", err)
				return
			}
			s.Wait(res.ID)
		}()
	}
	wg.Wait()

	// Every job must be present and terminal (done). ListJobs reads the DB.
	list, err := s.ListJobs(ListOpts{Project: "self", Limit: 1000})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(list) != n {
		t.Fatalf("expected %d persisted jobs (one row per id), got %d", n, len(list))
	}
	for _, r := range list {
		if r.Status != StatusDone {
			t.Fatalf("job %s not terminal: %s", r.ID, r.Status)
		}
	}
}

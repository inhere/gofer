package job

import (
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestReconcileOrphanCoversDefaultWorkerJobs: after a serve restart, a runner=worker
// job whose row has an EMPTY worker_id must still be held in `recovering` instead of
// being failed like a local job. That row shape is what the D4 default-worker fallback
// produces — the target worker is resolved by the runner at dispatch, so a request that
// names no worker_id used to leave the column empty and the job was misclassified as a
// purely local one (nothing could ever have finished it, so it was failed at once, and
// a worker still running it was orphaned). A genuinely local job is still failed.
func TestReconcileOrphanCoversDefaultWorkerJobs(t *testing.T) {
	root := t.TempDir()
	// remote-w1 is type=worker with no request-side worker_id in these rows; the
	// classification must come from the RUNNER, not from the column.
	s := newWorkerTestServiceSel(t, root, &stubWorkerRunner{}, map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}}, nil)

	now := s.nowFn().Unix()
	seed := func(id, runner, workerID, status string) {
		t.Helper()
		rec := jobstore.JobRecord{
			ID: id, ProjectKey: "self", Agent: "exec", Runner: runner, WorkerID: workerID,
			Status: status, Cwd: ".", ResultDir: filepath.Join(root, "self", id),
			StartedAt: now - 10, UpdatedAt: now,
		}
		if err := s.meta.UpsertJob(rec); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("j-recovering", "remote-w1", "", StatusRecovering)
	seed("j-running", "remote-w1", "", StatusRunning)
	seed("j-local", "local", "", StatusRunning)

	n, err := s.ReconcileOrphanJobs()
	if err != nil {
		t.Fatalf("ReconcileOrphanJobs: %v", err)
	}
	if n != 3 {
		t.Fatalf("resolved rows = %d, want 3 (2 worker jobs held + 1 local failed)", n)
	}

	for _, id := range []string{"j-recovering", "j-running"} {
		rec, ok, err := s.meta.GetJob(id)
		if err != nil || !ok {
			t.Fatalf("GetJob(%s): ok=%v err=%v", id, ok, err)
		}
		if rec.Status != StatusRecovering {
			t.Fatalf("%s status = %s, want %s (a worker job is held for its worker's window)", id, rec.Status, StatusRecovering)
		}
		if rec.RecoveringSince == 0 {
			t.Fatalf("%s has no recovering_since stamp", id)
		}
	}
	local, _, _ := s.meta.GetJob("j-local")
	if local.Status != StatusFailed {
		t.Fatalf("local job status = %s, want failed (no in-process state survived)", local.Status)
	}
}

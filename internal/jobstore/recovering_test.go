package jobstore

import (
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestReconcileOrphanWorkerJobsRecovering: RECOV-01. A serve that restarted
// mid-flight must NOT fail a worker job outright — the worker process may still be
// running it (its job.Service is process-scoped), so the row is HELD in `recovering`
// with recovering_since stamped and an explanatory error naming the worker, and the
// re-armed recovery window then ends it via FailRecoveringJobs if the worker never
// proves itself. A local (no worker_id) non-terminal job still fails at once: its
// in-process state really is gone, so nothing could ever finish it. Terminal rows
// (worker or not) are untouched.
func TestReconcileOrphanWorkerJobsRecovering(t *testing.T) {
	s := openTest(t)
	const ts = int64(1_700_000_000)

	workerRunning := sampleJob("w-run", "proj", 100)
	workerRunning.Status, workerRunning.WorkerID, workerRunning.UpdatedAt = "running", "w1", 100
	workerQueued := sampleJob("w-queued", "proj", 100)
	workerQueued.Status, workerQueued.WorkerID, workerQueued.UpdatedAt = "queued", "w2", 100
	localRunning := sampleJob("l-run", "proj", 100)
	localRunning.Status, localRunning.UpdatedAt = "running", 100 // worker_id stays ''
	workerDone := sampleJob("w-done", "proj", 100)
	workerDone.Status, workerDone.WorkerID = "done", "w1"
	workerDone.EndedAt, workerDone.UpdatedAt = 200, 200
	for _, j := range []JobRecord{workerRunning, workerQueued, localRunning, workerDone} {
		assert.NoErr(t, s.UpsertJob(j))
	}

	// Total rows touched: the 2 worker jobs (held) + the 1 local job (failed).
	n, err := s.ReconcileOrphanJobs(ts, "orphaned-test")
	assert.NoErr(t, err)
	assert.Eq(t, 3, n)

	// Worker jobs are HELD, not failed: recovering + recovering_since + updated_at
	// stamped, ended_at still unset, and the error says which worker is awaited.
	for _, tc := range []struct{ id, workerID string }{{"w-run", "w1"}, {"w-queued", "w2"}} {
		rec, ok, gerr := s.GetJob(tc.id)
		assert.NoErr(t, gerr)
		assert.True(t, ok)
		assert.Eq(t, "recovering", rec.Status)
		assert.Eq(t, ts, rec.RecoveringSince)
		assert.Eq(t, ts, rec.UpdatedAt)
		assert.Eq(t, int64(0), rec.EndedAt)
		if !strings.Contains(rec.Error, "awaiting worker "+tc.workerID) {
			t.Fatalf("%s error = %q, want it to name the awaited worker %q", tc.id, rec.Error, tc.workerID)
		}
	}

	// The local job keeps the pre-RECOV-01 behaviour: failed, reason + ts stamped.
	l, ok, gerr := s.GetJob("l-run")
	assert.NoErr(t, gerr)
	assert.True(t, ok)
	assert.Eq(t, "failed", l.Status)
	assert.Eq(t, "orphaned-test", l.Error)
	assert.Eq(t, ts, l.EndedAt)
	assert.Eq(t, ts, l.UpdatedAt)

	// A terminal worker job is untouched.
	d, _, _ := s.GetJob("w-done")
	assert.Eq(t, "done", d.Status)
	assert.Eq(t, int64(200), d.EndedAt)

	// A second reconcile (another serve restart before the worker reconnected) still
	// touches only the 2 held rows: they stay recovering, window re-armed.
	n2, err := s.ReconcileOrphanJobs(ts+5, "orphaned-test-2")
	assert.NoErr(t, err)
	assert.Eq(t, 2, n2)
	held, _, _ := s.GetJob("w-run")
	assert.Eq(t, "recovering", held.Status)
	assert.Eq(t, ts+5, held.RecoveringSince)

	// Window elapsed: FailRecoveringJobs fails exactly the still-recovering rows.
	const lostReason = "worker lost: recovery window elapsed"
	n3, err := s.FailRecoveringJobs(ts+120, lostReason)
	assert.NoErr(t, err)
	assert.Eq(t, 2, n3)
	for _, id := range []string{"w-run", "w-queued"} {
		rec, _, gerr := s.GetJob(id)
		assert.NoErr(t, gerr)
		assert.Eq(t, "failed", rec.Status)
		assert.Eq(t, lostReason, rec.Error)
		assert.Eq(t, ts+120, rec.EndedAt)
		assert.Eq(t, ts+120, rec.UpdatedAt)
	}

	// Idempotent: nothing is recovering any more.
	n4, err := s.FailRecoveringJobs(ts+121, lostReason)
	assert.NoErr(t, err)
	assert.Eq(t, 0, n4)
}

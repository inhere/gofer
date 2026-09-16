package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// TestDispatchPersistsWorkerIdentity: a runner=worker job's row must name the RESOLVED
// worker AND the process instance that owns it — for an explicit worker_id and equally
// for the D4 default-worker fallback, whose worker_id is empty on the request (and used
// to be empty in the row, which made such a job look like a local orphan after a
// restart and unadoptable).
func TestDispatchPersistsWorkerIdentity(t *testing.T) {
	hub := buildHubSide(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cl, _ := buildWorkerSideJobs(t, hub.ts.URL)
	go func() { _ = cl.Run(ctx) }()
	waitWorkerOnline(t, hub.hub)

	// Every job of this worker must record the SAME process instance (one worker
	// process, one instance id) — the invariant the adoption decision rests on.
	firstInstance := ""
	for _, tc := range []struct {
		name     string
		workerID string
	}{
		{name: "explicit-worker-id", workerID: e2eWorkerID},
		{name: "default-worker-fallback", workerID: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			created := createJob(t, hub.ts, job.JobRequest{
				ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: tc.workerID,
				Cmd: testcmd.Cmd(t, "stdout-lines", "ID", "1", "10ms"), Cwd: ".", TimeoutSec: 60,
			})
			final, ok := hub.jobs.Wait(created.ID)
			if !ok {
				t.Fatalf("job %s not found", created.ID)
			}
			if final.Status != job.StatusDone {
				t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
			}
			if final.WorkerID != e2eWorkerID {
				t.Fatalf("job worker_id = %q, want %q (the RESOLVED target)", final.WorkerID, e2eWorkerID)
			}
			if final.WorkerInstanceID == "" {
				t.Fatal("job worker_instance_id is empty: the owning worker process was not recorded")
			}
			rec, ok, err := hub.store.GetJob(created.ID)
			if err != nil || !ok {
				t.Fatalf("GetJob persisted: ok=%v err=%v", ok, err)
			}
			if rec.WorkerID != e2eWorkerID || rec.WorkerInstanceID != final.WorkerInstanceID {
				t.Fatalf("persisted row = (worker_id %q, worker_instance_id %q), want (%q, %q)",
					rec.WorkerID, rec.WorkerInstanceID, e2eWorkerID, final.WorkerInstanceID)
			}
			if firstInstance == "" {
				firstInstance = rec.WorkerInstanceID
			} else if rec.WorkerInstanceID != firstInstance {
				t.Fatalf("worker_instance_id = %q, want the process instance %q recorded by the other job",
					rec.WorkerInstanceID, firstInstance)
			}
		})
	}
}

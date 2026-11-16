package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/worker"
)

// TestE2EJobEventsReplayedOnceAfterBlip is the protocol v30 full-stack acceptance: of
// two events the worker raises, one goes out on the healthy connection and one while
// the connection is down. After the reconnect the host job's timeline holds each
// exactly once — the second was buffered and replayed, the first was acked away and
// never re-inserted.
func TestE2EJobEventsReplayedOnceAfterBlip(t *testing.T) {
	hub := buildHubSide(t)
	hub.hub.SetRecoverWindow(30 * time.Second)
	stop := make(chan struct{})
	hub.hub.SetStop(stop)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const reconnectCap = 2 * time.Second
	cl, localJobs := buildWorkerSideJobsOpts(t, hub.ts.URL, workerSideOpts{
		InitialBackoff: reconnectCap,
		MaxBackoff:     reconnectCap,
		Rng:            pinSlowReconnect(t, reconnectCap),
	})
	worker.StartClient(t, ctx, cl)
	waitWorkerOnline(t, hub.hub)

	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Cmd: testcmd.Cmd(t, "stdout-lines", "LINE", "12", "400ms"), Cwd: ".", TimeoutSec: 120,
	})
	waitForLogContains(t, hub.ts, created.ID, "LINE2", 15*time.Second)

	var localID string
	list, err := localJobs.ListJobs(job.ListOpts{Limit: 10})
	if err != nil || len(list) == 0 {
		t.Fatalf("worker-local jobs = %v (%v)", list, err)
	}
	localID = list[0].ID

	localJobs.RecordJobEvent(localID, job.EventJobVerifyStarted, map[string]any{"command": "before-blip"})
	waitHubEvent(t, hub.store, created.ID, job.EventJobVerifyStarted, 1, 10*time.Second)

	close(stop) // blip the connection, not the worker
	waitHostStatus(t, hub.jobs, created.ID, job.StatusRecovering, 10*time.Second)
	localJobs.RecordJobEvent(localID, job.EventJobVerifyFinished, map[string]any{"status": "passed", "note": "during-blip"})
	waitHostStatus(t, hub.jobs, created.ID, job.StatusRunning, 20*time.Second)

	if final, ok := hub.jobs.Wait(created.ID); !ok || final.Status != job.StatusDone {
		t.Fatalf("final = %+v (ok=%v), want done", final, ok)
	}
	if got := hubEventDetail(t, hub.store, created.ID, job.EventJobVerifyFinished); len(got) != 1 || got[0]["note"] != "during-blip" {
		t.Fatalf("verify_finished rows on the host = %v, want exactly the one raised during the blip", got)
	}
	if got := hubEventDetail(t, hub.store, created.ID, job.EventJobVerifyStarted); len(got) != 1 {
		t.Fatalf("verify_started rows on the host = %v, want exactly 1 (no replay duplicate)", got)
	}
}

package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	workerpkg "github.com/inhere/gofer/internal/worker"
)

type remoteQueueFixture struct {
	hub       *hubSide
	cl        *workerpkg.Client
	ctx       context.Context
	cancel    context.CancelFunc
	clientErr <-chan error
}

func startRemoteQueueFixture(t *testing.T) remoteQueueFixture {
	t.Helper()
	hub := buildHubSide(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	cl, localJobs := buildWorkerSideJobsOpts(t, hub.ts.URL, workerSideOpts{ExecMaxConcurrent: 1})
	t.Cleanup(cancel)
	errCh := workerpkg.StartClient(t, ctx, cl)
	waitWorkerOnline(t, hub.hub)
	_ = localJobs
	return remoteQueueFixture{hub: hub, cl: cl, ctx: ctx, cancel: cancel, clientErr: errCh}
}

func waitRemoteSnapshot(t *testing.T, s *job.Service, id string, want func(job.JobResult) bool) job.JobResult {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if got, ok := s.Get(id); ok && want(got) {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, _ := s.Get(id)
	t.Fatalf("job %s did not reach expected state, got status=%s started_at=%d error=%s", id, got.Status, got.StartedAt, got.Error)
	return got
}

func remoteSleepRequest(t *testing.T, seconds string, timeout int) job.JobRequest {
	t.Helper()
	return job.JobRequest{
		ProjectKey: "alpha", Agent: "slow", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Cmd: testcmd.Cmd(t, "sleep", seconds), Cwd: ".", TimeoutSec: timeout,
	}
}

func TestRemoteJobQueueTimeNotCounted(t *testing.T) {
	f := startRemoteQueueFixture(t)
	holder := createJob(t, f.hub.ts, remoteSleepRequest(t, "2s", 10))
	waitRemoteSnapshot(t, f.hub.jobs, holder.ID, func(g job.JobResult) bool { return g.Status == job.StatusRunning })
	queued := createJob(t, f.hub.ts, remoteSleepRequest(t, "500ms", 1))
	waitRemoteSnapshot(t, f.hub.jobs, queued.ID, func(g job.JobResult) bool { return g.Status == job.StatusQueued })
	final, ok := f.hub.jobs.Wait(queued.ID)
	if !ok || final.Status != job.StatusDone {
		t.Fatalf("queued remote job = %s (err=%s), want done after worker queue", final.Status, final.Error)
	}
}

func TestRemoteJobShowsQueuedUntilStarted(t *testing.T) {
	f := startRemoteQueueFixture(t)
	holder := createJob(t, f.hub.ts, remoteSleepRequest(t, "2s", 10))
	waitRemoteSnapshot(t, f.hub.jobs, holder.ID, func(g job.JobResult) bool { return g.Status == job.StatusRunning })
	dispatchedAt := time.Now().Unix()
	queued := createJob(t, f.hub.ts, remoteSleepRequest(t, "1s", 10))
	waitRemoteSnapshot(t, f.hub.jobs, queued.ID, func(g job.JobResult) bool {
		return g.Status == job.StatusQueued && g.StartedAt == 0
	})
	started := waitRemoteSnapshot(t, f.hub.jobs, queued.ID, func(g job.JobResult) bool {
		return g.Status == job.StatusRunning && g.StartedAt > 0
	})
	if started.StartedAt < dispatchedAt {
		t.Fatalf("started_at predates dispatch: dispatch=%d started=%d", dispatchedAt, started.StartedAt)
	}
}

func TestRemoteJobTimeoutStillEnforcedAfterStart(t *testing.T) {
	f := startRemoteQueueFixture(t)
	created := createJob(t, f.hub.ts, remoteSleepRequest(t, "3s", 1))
	waitRemoteSnapshot(t, f.hub.jobs, created.ID, func(g job.JobResult) bool { return g.Status == job.StatusRunning })
	final, ok := f.hub.jobs.Wait(created.ID)
	if !ok || final.Status != job.StatusTimeout {
		t.Fatalf("started remote job = %s (err=%s), want timeout", final.Status, final.Error)
	}
}

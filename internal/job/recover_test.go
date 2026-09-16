package job

import (
	"context"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/runner"
)

// recoverStubRunner models the RECOV-01 lifecycle at the runner boundary: the
// connection drops (OnSuspend → the host job goes `recovering`), then the same worker
// process comes back (OnResume → back to `running`). It parks on channels so the test
// can observe each intermediate state deterministically — the states a real run only
// passes through.
type recoverStubRunner struct {
	suspended chan struct{} // closed once the job is in `recovering`
	resume    chan struct{} // test → runner: the worker came back
	resumed   chan struct{} // runner → test: the job is `running` again
	finish    chan struct{} // test → runner: end the job cleanly
}

func (r *recoverStubRunner) Name() string { return "remote-w1" }

func (r *recoverStubRunner) Run(_ context.Context, req runner.Request) runner.Result {
	req.OnSuspend("worker disconnected")
	close(r.suspended)
	<-r.resume
	req.OnResume()
	close(r.resumed)
	<-r.finish
	return runner.Result{ExitCode: 0}
}

// TestJobRecoveringStatusTransitions is the job-layer half of RECOV-01: a worker
// disconnect must move the job to the NON-terminal `recovering` state with
// recovering_since stamped (in memory AND in the metadata store, so CLI/web/another
// process see it), and a successful reconnect must return it to `running` with the
// holding state cleared — the job then finishes normally on the worker's result.
func TestJobRecoveringStatusTransitions(t *testing.T) {
	root := t.TempDir()
	stub := &recoverStubRunner{
		suspended: make(chan struct{}),
		resume:    make(chan struct{}),
		resumed:   make(chan struct{}),
		finish:    make(chan struct{}),
	}
	s := newWorkerTestService(t, root, stub)

	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1", WorkerID: "w1",
		Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 60,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	<-stub.suspended

	got, ok := s.Get(res.ID)
	if !ok {
		t.Fatalf("job %s not found", res.ID)
	}
	if got.Status != StatusRecovering {
		t.Fatalf("status = %q, want %q", got.Status, StatusRecovering)
	}
	if got.RecoveringSince == 0 {
		t.Fatal("recovering_since was not stamped")
	}
	if IsTerminal(StatusRecovering) {
		t.Fatal("recovering must NOT be a terminal status")
	}
	// The holding state must survive as a PERSISTED fact (the hub can outlive this
	// process's in-memory entry, and CLI/web read the DB).
	rec, ok, err := s.Meta().GetJob(res.ID)
	if err != nil || !ok {
		t.Fatalf("jobstore read: ok=%v err=%v", ok, err)
	}
	if rec.Status != StatusRecovering || rec.RecoveringSince == 0 {
		t.Fatalf("persisted row = status %q recovering_since %d, want %q and >0",
			rec.Status, rec.RecoveringSince, StatusRecovering)
	}
	if rec.WorkerID != "w1" {
		t.Fatalf("persisted worker_id = %q, want w1", rec.WorkerID)
	}

	// The worker process comes back with the job: back to `running`, holding cleared.
	close(stub.resume)
	<-stub.resumed
	waitForStatus(t, s, res.ID, StatusRunning, 2*time.Second)
	got, _ = s.Get(res.ID)
	if got.RecoveringSince != 0 {
		t.Fatalf("recovering_since = %d after resume, want 0", got.RecoveringSince)
	}
	if got.Error != "" {
		t.Fatalf("error = %q after resume, want empty", got.Error)
	}

	// And the job still completes on its worker's result.
	close(stub.finish)
	final, ok := s.Wait(res.ID)
	if !ok || final.Status != StatusDone {
		t.Fatalf("final = %+v (ok=%v), want done", final, ok)
	}
}

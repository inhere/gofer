package job

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

func shutdownCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, d))
	t.Cleanup(cancel)
	return ctx
}

// Shutdown stops the jobs this process runs, waits until their execute goroutine has
// returned, leaves their rows as an exiting process would (the next serve's reconcile
// settles them) and refuses new work afterwards.
func TestShutdownStopsLocalJobsAndClosesAdmission(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	res, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{testcmd.Path(t), "sleep", "60s"}, Cwd: ".", TimeoutSec: 120})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForStatus(t, s, res.ID, StatusRunning, wait.Timeout(t, 30*time.Second))
	entry := s.entry(res.ID)

	if err := s.Shutdown(shutdownCtx(t, 30*time.Second)); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-entry.done:
	default:
		t.Fatal("Shutdown returned before the job's execute goroutine did")
	}
	rec, ok, err := s.meta.GetJob(res.ID)
	if err != nil || !ok || rec.Status != StatusRunning {
		t.Fatalf("row after Shutdown = %q (ok=%v err=%v), want it left running for the restart reconcile", rec.Status, ok, err)
	}
	if _, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{testcmd.Path(t), "exit", "0"}, Cwd: "."}); !errors.Is(err, ErrUpgradeDraining) {
		t.Fatalf("Submit after Shutdown = %v, want admission refused", err)
	}
	if err := s.Shutdown(shutdownCtx(t, 5*time.Second)); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}

// blockingRemoteRunner stands in for a worker runner whose job keeps running until
// the host cancels it (cancelling sends the worker a cancel frame).
type blockingRemoteRunner struct {
	started   chan struct{}
	cancelled atomic.Bool
}

func (r *blockingRemoteRunner) Name() string { return "remote-w1" }
func (r *blockingRemoteRunner) Run(ctx context.Context, _ runner.Request) runner.Result {
	close(r.started)
	<-ctx.Done()
	r.cancelled.Store(true)
	return runner.Result{ExitCode: -1, Err: ctx.Err()}
}

// A job a worker executes survives a serve restart (RECOV-01): Shutdown must neither
// cancel it (a cancel frame would end it on the worker) nor wait for it. Drain, the
// teardown variant, ends it too.
func TestShutdownLeavesWorkerJobsDrainEndsThem(t *testing.T) {
	t.Parallel()
	r := &blockingRemoteRunner{started: make(chan struct{})}
	s := newWorkerTestService(t, t.TempDir(), r)
	res, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "remote-w1", WorkerID: "w1",
		Cmd: []string{"echo", "hi"}, Cwd: "."})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	select {
	case <-r.started:
	case <-time.After(wait.Timeout(t, 30*time.Second)):
		t.Fatal("worker runner never ran")
	}

	if err := s.Shutdown(shutdownCtx(t, 30*time.Second)); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if r.cancelled.Load() {
		t.Fatal("Shutdown cancelled a job a worker runs")
	}
	if got, _ := s.Get(res.ID); IsFinished(got.Status) {
		t.Fatalf("worker job after Shutdown = %s, want it left live", got.Status)
	}
	if err := s.Drain(shutdownCtx(t, 30*time.Second)); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if !r.cancelled.Load() {
		t.Fatal("Drain left the worker job running")
	}
}

// Shutdown is not over while a terminal hook still runs: the hook may write to the
// store the caller is about to close.
func TestShutdownWaitsForTerminalHooks(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	entered, release := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	s.OnTerminal(func(JobResult) {
		if once.CompareAndSwap(false, true) {
			close(entered)
		}
		<-release
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	if _, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{testcmd.Path(t), "exit", "0"}, Cwd: ".", TimeoutSec: 30}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(wait.Timeout(t, 30*time.Second)):
		t.Fatal("terminal hook never ran")
	}
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := s.Shutdown(short); err == nil {
		t.Fatal("Shutdown returned while a terminal hook was still running")
	}
	close(release)
	if err := s.Shutdown(shutdownCtx(t, 10*time.Second)); err != nil {
		t.Fatalf("Shutdown after the hook returned: %v", err)
	}
}

// The background group waits for work that work it is waiting for adds, and refuses
// deferred work once closed.
func TestBackgroundGroupWaitsForNestedWorkAndRefusesAfterClose(t *testing.T) {
	t.Parallel()
	var g bgGroup
	var ran atomic.Int32
	inner := make(chan struct{})
	g.add()
	go func() {
		defer g.done()
		g.add()
		go func() {
			defer g.done()
			<-inner
			ran.Add(1)
		}()
	}()
	g.close()
	if g.tryAdd() {
		t.Fatal("tryAdd accepted deferred work after close")
	}
	time.AfterFunc(20*time.Millisecond, func() { close(inner) })
	if err := g.wait(shutdownCtx(t, 10*time.Second)); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if ran.Load() != 1 {
		t.Fatal("wait returned before the nested work finished")
	}
	select {
	case <-g.closingCh():
	default:
		t.Fatal("closingCh not closed after close")
	}
}

// An adopted job (RECOV-01 R4) has no execute goroutine: its entry.done must still
// close once its terminal path ran, or Wait on it — and a Drain — block forever.
func TestAdoptedJobClosesDoneOnFinish(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newWorkerTestService(t, root, &stubWorkerRunner{})
	rec := jobstore.JobRecord{ID: "adopted-done", ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
		WorkerID: "w1", WorkerInstanceID: "inst-1", Status: StatusRecovering, Cwd: ".",
		ResultDir: filepath.Join(root, "adopted-done"), RequestJSON: `{}`, StartedAt: 100, UpdatedAt: 100}
	if err := os.MkdirAll(rec.ResultDir, 0o755); err != nil { // the previous serve's result dir
		t.Fatal(err)
	}
	if err := s.meta.UpsertJob(rec); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	adopted, lost := s.ReconcileAdoption("w1", "inst-1", []WorkerInflightJob{{JobID: rec.ID, Status: StatusRunning}}, AdoptBackend{})
	aj := adopted[rec.ID]
	if aj == nil {
		t.Fatalf("job not adopted (lost=%v)", lost)
	}
	entry := s.entry(rec.ID)
	aj.Finish(0, nil)
	select {
	case <-entry.done:
	case <-time.After(wait.Timeout(t, 10*time.Second)):
		t.Fatal("adopted job finished but its entry.done never closed")
	}
	if got, ok := s.Get(rec.ID); !ok || got.Status != StatusDone {
		t.Fatalf("adopted job after Finish = %+v (ok=%v), want done", got, ok)
	}
}

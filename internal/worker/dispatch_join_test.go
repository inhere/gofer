package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/wsproto"
)

// joinProbeJobs is a Jobs double for the dispatch-lifetime tests below. Submit
// starts a local job that stays running; Get reports it (and counts the calls);
// Wait — what handleDispatch calls after its log tail — is held until the first
// Cancel arrives, and Cancel itself asks the client whether it is idle while it is
// still inside the job service.
type joinProbeJobs struct {
	*stubJobs
	cl *Client

	gets      atomic.Int64
	afterIdle atomic.Bool   // set by the test once WaitIdle reported the client idle
	lateGet   chan struct{} // closed by the first Get after that

	cancelOnce    sync.Once
	cancelEntered chan struct{}
	idleInCancel  chan bool // what WaitIdle said while Cancel was in flight
	waitHold      time.Duration
	lateOnce      sync.Once
}

func newJoinProbeJobs(t *testing.T, status string) *joinProbeJobs {
	return &joinProbeJobs{
		stubJobs: &stubJobs{
			submitID:  "local-1",
			submitDir: t.TempDir(),
			getResult: job.JobResult{ID: "local-1", Status: status},
			getOK:     true,
		},
		lateGet:       make(chan struct{}),
		cancelEntered: make(chan struct{}),
		idleInCancel:  make(chan bool, 1),
		waitHold:      wait.Timeout(t, 10*time.Second),
	}
}

func (j *joinProbeJobs) Get(id string) (job.JobResult, bool) {
	j.gets.Add(1)
	if j.afterIdle.Load() {
		j.lateOnce.Do(func() { close(j.lateGet) })
	}
	return j.stubJobs.Get(id)
}

func (j *joinProbeJobs) Wait(id string) (job.JobResult, bool) {
	select {
	case <-j.cancelEntered:
	case <-time.After(j.waitHold):
	}
	return job.JobResult{ID: id, Status: job.StatusCancelled, ExitCode: -1}, true
}

func (j *joinProbeJobs) Cancel(id string) error {
	j.cancelOnce.Do(func() {
		close(j.cancelEntered)
		// The dispatch may now finish. It must not count as idle while this call —
		// made by a goroutine the dispatch started — is still in the job service.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		j.idleInCancel <- j.cl.WaitIdle(ctx)
	})
	return j.stubJobs.Cancel(id)
}

// runProbeDispatch runs handleDispatch the way recvLoop does (tracked by
// dispatchWG) on a client with no hub connection: every frame write fails and the
// terminal Result is cached, which is all these tests need.
func runProbeDispatch(t *testing.T, jobs *joinProbeJobs) (*Client, context.CancelFunc) {
	t.Helper()
	cl := New(Config{WorkerID: "w1", URLs: []string{"ws://127.0.0.1:1/v1/workers/connect"}, Token: "t"}, jobs)
	cl.pollInterval = 10 * time.Millisecond
	jobs.cl = cl
	ctx, cancel := context.WithCancel(context.Background())
	cl.dispatchWG.Add(1)
	go func() {
		defer cl.dispatchWG.Done()
		cl.handleDispatch(ctx, "ws://127.0.0.1:1/v1/workers/connect", wsproto.Dispatch{
			JobID: "d1", ProjectKey: "alpha", Agent: "exec", Runner: "local", Cmd: []string{"echo", "hi"},
		})
	}()
	t.Cleanup(func() {
		cancel()
		idleCtx, idleCancel := context.WithTimeout(context.Background(), wait.Timeout(t, clientStopBudget))
		defer idleCancel()
		if !cl.WaitIdle(idleCtx) {
			t.Errorf("dispatch did not unwind")
		}
	})
	return cl, cancel
}

// TestDispatchJoinsCancelWatcherBeforeIdle: when the process ctx ends, the dispatch's
// watcher goroutine cancels the local job. That call reaches the job service and its
// store, so WaitIdle must not report the client idle until it has returned. It used
// to: the watcher was not part of the dispatch, a test tore its store and TempDir
// down while the watcher was still in Cancel, and the stray sqlite connection left
// files behind (`TempDir RemoveAll cleanup: directory not empty`).
func TestDispatchJoinsCancelWatcherBeforeIdle(t *testing.T) {
	jobs := newJoinProbeJobs(t, job.StatusRunning)
	_, cancel := runProbeDispatch(t, jobs)

	// The log tail polls Get once the dispatch is past the watcher's start.
	wait.Until(t, 10*time.Second, "dispatch tails its local job", func() bool { return jobs.gets.Load() > 0 })
	cancel() // process shutdown: the watcher cancels the local job

	select {
	case idle := <-jobs.idleInCancel:
		if idle {
			t.Fatal("WaitIdle reported the client idle while the dispatch's cancel watcher was still inside the job service")
		}
	case <-time.After(wait.Timeout(t, 10*time.Second)):
		t.Fatal("the watcher never cancelled the local job")
	}
}

// TestDispatchStopsRenderedCommandPollerBeforeIdle: the early rendered-command
// report polls the job service until the command shows up. A job that ends without
// one (it never ran) used to leave that poller calling Get for up to 15s after its
// dispatch had returned and WaitIdle had reported the client idle.
func TestDispatchStopsRenderedCommandPollerBeforeIdle(t *testing.T) {
	jobs := newJoinProbeJobs(t, job.StatusFailed) // terminal, never rendered
	jobs.waitHold = 0                             // Wait returns at once: nothing to cancel
	cl, _ := runProbeDispatch(t, jobs)

	idleCtx, idleCancel := context.WithTimeout(context.Background(), wait.Timeout(t, 10*time.Second))
	defer idleCancel()
	if !cl.WaitIdle(idleCtx) {
		t.Fatal("dispatch of a finished job did not return")
	}
	jobs.afterIdle.Store(true)
	// The poller ticks every 100ms; give it several ticks to show up.
	select {
	case <-jobs.lateGet:
		t.Fatal("a goroutine of the finished dispatch still polled the job service after WaitIdle")
	case <-time.After(500 * time.Millisecond):
	}
}

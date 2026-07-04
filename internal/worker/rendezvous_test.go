package worker

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	ptyrunner "github.com/inhere/gofer/internal/runner/pty"
)

// rendezvousJobs is a minimal worker.Jobs for the session-rendezvous / pendingCancel
// unit tests. Wait blocks until waitCh is closed (nil waitCh = returns immediately),
// letting a test decide whether the local job is "still running" or "terminal".
type rendezvousJobs struct {
	waitCh chan struct{}
}

func (r *rendezvousJobs) Submit(job.JobRequest) (job.JobResult, error) {
	return job.JobResult{ID: "local-1", Status: job.StatusRunning}, nil
}
func (r *rendezvousJobs) Get(string) (job.JobResult, bool) { return job.JobResult{}, false }
func (r *rendezvousJobs) Wait(string) (job.JobResult, bool) {
	if r.waitCh != nil {
		<-r.waitCh
	}
	return job.JobResult{Status: job.StatusDone, ExitCode: 0}, true
}
func (r *rendezvousJobs) Cancel(string) error { return nil }
func (r *rendezvousJobs) GetInteractions(string) ([]job.Interaction, error) {
	return nil, nil
}
func (r *rendezvousJobs) AnswerInteraction(string, string, string) (job.Interaction, error) {
	return job.Interaction{}, nil
}

// newRendezvousClient builds a Client whose local Wait blocks on a per-test chan
// (closed in cleanup so the waitSession goroutine never leaks).
func newRendezvousClient(t *testing.T) *Client {
	t.Helper()
	waitCh := make(chan struct{})
	t.Cleanup(func() { close(waitCh) })
	return New(Config{WorkerID: "w1", URLs: []string{"ws://x/y"}}, &rendezvousJobs{waitCh: waitCh})
}

// waitForWaiter blocks until waitSession has parked a waiter for localID (so the
// test can drive OnSessionStart AFTER the waiter is registered).
func waitForWaiter(t *testing.T, cl *Client, localID string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		cl.sessMu.Lock()
		_, ok := cl.sessWaiters[localID]
		cl.sessMu.Unlock()
		if ok {
			return
		}
		select {
		case <-deadline:
			t.Fatal("waiter was never parked")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestWaitSessionObserverBeforeWaiter(t *testing.T) {
	// OnSessionStart fires first → session buffered in sessReady → waitSession returns
	// it immediately without parking a waiter.
	cl := newRendezvousClient(t)
	sess := &ptyrunner.PtySession{}
	cl.OnSessionStart("local-1", sess)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got := cl.waitSession(ctx, "local-1")
	if got != sess {
		t.Fatalf("waitSession = %p, want buffered sess %p", got, sess)
	}
	// buffer consumed, no waiter parked.
	cl.sessMu.Lock()
	nReady, nWaiters := len(cl.sessReady), len(cl.sessWaiters)
	cl.sessMu.Unlock()
	if nReady != 0 || nWaiters != 0 {
		t.Fatalf("rendezvous not clean: ready=%d waiters=%d", nReady, nWaiters)
	}
}

func TestWaitSessionWaiterBeforeObserver(t *testing.T) {
	// waitSession parks a waiter first → OnSessionStart hands the session over the chan.
	cl := newRendezvousClient(t)
	sess := &ptyrunner.PtySession{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resCh := make(chan *ptyrunner.PtySession, 1)
	go func() { resCh <- cl.waitSession(ctx, "local-1") }()
	waitForWaiter(t, cl, "local-1")

	cl.OnSessionStart("local-1", sess)
	select {
	case got := <-resCh:
		if got != sess {
			t.Fatalf("waitSession = %p, want delivered sess %p", got, sess)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitSession did not return after OnSessionStart")
	}
	cl.sessMu.Lock()
	nWaiters := len(cl.sessWaiters)
	cl.sessMu.Unlock()
	if nWaiters != 0 {
		t.Fatalf("waiter not removed after delivery: %d", nWaiters)
	}
}

func TestWaitSessionJobTerminalReturnsNil(t *testing.T) {
	// The local job reaches terminal (Wait returns) before any session starts →
	// waitSession must return nil, not hang, and clean up the waiter.
	cl := New(Config{WorkerID: "w1", URLs: []string{"ws://x/y"}}, &rendezvousJobs{}) // nil waitCh → Wait returns now
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got := cl.waitSession(ctx, "local-1")
	if got != nil {
		t.Fatalf("waitSession = %p, want nil on terminal job", got)
	}
	cl.sessMu.Lock()
	nWaiters := len(cl.sessWaiters)
	cl.sessMu.Unlock()
	if nWaiters != 0 {
		t.Fatalf("waiter not cleaned up: %d", nWaiters)
	}
}

func TestWaitSessionCtxCancelReturnsNil(t *testing.T) {
	// The dispatch ctx is cancelled while parked (job still running) → nil + cleanup.
	cl := newRendezvousClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	resCh := make(chan *ptyrunner.PtySession, 1)
	go func() { resCh <- cl.waitSession(ctx, "local-1") }()
	waitForWaiter(t, cl, "local-1")

	cancel()
	select {
	case got := <-resCh:
		if got != nil {
			t.Fatalf("waitSession = %p, want nil on ctx cancel", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitSession did not return after ctx cancel")
	}
	cl.sessMu.Lock()
	nWaiters := len(cl.sessWaiters)
	cl.sessMu.Unlock()
	if nWaiters != 0 {
		t.Fatalf("waiter not cleaned up after cancel: %d", nWaiters)
	}
}

func TestPendingCancelRecordTake(t *testing.T) {
	cl := New(Config{WorkerID: "w1", URLs: []string{"ws://x/y"}}, &rendezvousJobs{})
	if cl.takePendingCancel("r1") {
		t.Fatal("takePendingCancel on unrecorded id = true, want false")
	}
	cl.recordPendingCancel("r1")
	if !cl.takePendingCancel("r1") {
		t.Fatal("takePendingCancel after record = false, want true")
	}
	if cl.takePendingCancel("r1") {
		t.Fatal("takePendingCancel after consume = true, want false (not idempotently true)")
	}
}

func TestPendingCancelSoftCap(t *testing.T) {
	// Recording far past the cap without ever taking must keep the map bounded.
	cl := New(Config{WorkerID: "w1", URLs: []string{"ws://x/y"}}, &rendezvousJobs{})
	for i := 0; i < pendingCancelCap*3; i++ {
		cl.recordPendingCancel(fmt.Sprintf("r%d", i))
	}
	cl.sessMu.Lock()
	n := len(cl.pendingCancel)
	cl.sessMu.Unlock()
	if n > pendingCancelCap {
		t.Fatalf("pendingCancel size = %d, want <= cap %d", n, pendingCancelCap)
	}
	// The most-recent id must survive the sweep (oldest are evicted first).
	last := fmt.Sprintf("r%d", pendingCancelCap*3-1)
	if !cl.takePendingCancel(last) {
		t.Fatalf("newest recorded id %q was evicted", last)
	}
}

func TestRendezvousRaceSafe(t *testing.T) {
	// -race: concurrent observer/waiter/pendingCancel access must be data-race free.
	cl := newRendezvousClient(t)
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("j%d", i)
		wg.Add(3)
		go func() { defer wg.Done(); cl.OnSessionStart(id, &ptyrunner.PtySession{}) }()
		go func() { defer wg.Done(); cl.waitSession(ctx, id) }()
		go func() {
			defer wg.Done()
			cl.recordPendingCancel(id)
			cl.takePendingCancel(id)
		}()
	}
	wg.Wait()
}

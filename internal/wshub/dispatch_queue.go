package wshub

import (
	"context"
	"errors"
	"sync"

	"github.com/inhere/gofer/internal/wsproto"
)

// Capacity queueing (gofer-y05q).
//
// A worker advertises max_concurrent; a dispatch beyond it used to be refused outright
// (ErrWorkerAtCapacity) and the caller failed the job without it ever running. DispatchWait
// instead PARKS the dispatch in a per-worker FIFO and re-dispatches it — in arrival order —
// as soon as a slot frees: a job's result frame (readLoop), a sink deregistration, a raised
// concurrency cap (caps / policy / reload re-report) or the worker reconnecting. A worker
// that goes away fails the whole queue (ErrWorkerOffline), exactly as a dispatch would.
//
// The hub never learns WHY a job waits (the runner records that as a job event on its own
// timeline): it only needs the worker id, the frame and a callback reporting the capacity
// it is waiting for.

// dispatchWaiter is one parked dispatch.
type dispatchWaiter struct {
	jobID string
	d     wsproto.Dispatch
	// ready carries the terminal dispatch outcome exactly once: nil = the frame went out
	// (the slot is reserved for it), else the error that ended the wait. Buffered so a
	// pump never blocks on a caller that has given up.
	ready chan error
}

func (w *dispatchWaiter) deliver(err error) { w.ready <- err }

// dispatchQueue is one worker's FIFO of parked dispatches plus the pump state that keeps
// a SINGLE goroutine re-dispatching them, so the FIFO order holds even when several
// releases race (two results in flight, a reconnect and a caps re-report).
type dispatchQueue struct {
	mu      sync.Mutex
	waiters []*dispatchWaiter
	pumping bool
	// runMu serializes the pop+dispatch step of the pump with failQueue, so two pumps can
	// never hand out two slots out of arrival order (a pump re-armed by a reconnect while
	// the previous one is still finishing) and a queue failure cannot interleave with a
	// dispatch that is already going out.
	runMu sync.Mutex
	// again is set when a release arrives while a pump is running: the pump re-checks
	// the capacity it just found full instead of parking the queue on a freed slot.
	again bool
}

// remove drops w from the queue, reporting whether it was still parked. false means a
// pump already took it: its slot is reserved and its outcome is imminent.
func (q *dispatchQueue) remove(w *dispatchWaiter) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, x := range q.waiters {
		if x == w {
			q.waiters = append(q.waiters[:i], q.waiters[i+1:]...)
			return true
		}
	}
	return false
}

// DispatchWait sends a dispatch frame, parking in workerID's capacity queue when the
// worker is at its advertised max_concurrent instead of failing the job. It returns nil
// once the frame went out, ErrWorkerOffline / ErrWorkerDraining when the worker is (or
// goes) away and ctx.Err() when the caller gives up while still queued. onWait (may be
// nil) is called ONCE, right after the job is parked, with the worker's current in-flight
// count and cap (0 = no cap) so the caller can record why the job is waiting.
//
// The wait has no hub-side cap: the caller owns the deadline (server.worker_queue_max_wait_sec
// via the worker runner), because only it can turn the expiry into "never started, still
// queued" instead of a generic context error.
func (h *Hub) DispatchWait(ctx context.Context, workerID string, d wsproto.Dispatch, onWait func(inFlight, max int)) error {
	err := h.Dispatch(workerID, d)
	if err == nil || !errors.Is(err, ErrWorkerAtCapacity) {
		return err
	}
	q := h.queueFor(workerID)
	w := &dispatchWaiter{jobID: d.JobID, d: d, ready: make(chan error, 1)}
	q.mu.Lock()
	q.waiters = append(q.waiters, w)
	q.mu.Unlock()
	if onWait != nil {
		if inFlight, max, ok := h.WorkerCapacity(workerID); ok {
			onWait(inFlight, max)
		}
	}
	// A slot may have freed between the refused Dispatch and the append above (a result
	// frame processed in between): pump so the queue is never parked on capacity that is
	// already there.
	h.kickDispatch(workerID)
	select {
	case err := <-w.ready:
		return err
	case <-ctx.Done():
		if q.remove(w) {
			return ctx.Err()
		}
		// A pump took w: its slot is reserved and the frame is (about to be) on the wire,
		// so the worker WILL run this job. Report that outcome — the caller still sees its
		// cancelled ctx and sends a cancel frame, which is what stops the job there.
		return <-w.ready
	}
}

// WorkerCapacity reports a live worker's current in-flight job count and its advertised
// max_concurrent (0 = no hub-side cap). ok is false when the worker is offline.
func (h *Hub) WorkerCapacity(workerID string) (inFlight, max int, ok bool) {
	wc, ok := h.reg.Get(workerID)
	if !ok {
		return 0, 0, false
	}
	wc.mu.Lock()
	defer wc.mu.Unlock()
	return len(wc.inflight), wc.maxConcurrent, true
}

// queueFor returns workerID's dispatch queue, creating it on first use.
func (h *Hub) queueFor(workerID string) *dispatchQueue {
	h.dqMu.Lock()
	defer h.dqMu.Unlock()
	if h.queues == nil {
		h.queues = map[string]*dispatchQueue{}
	}
	q, ok := h.queues[workerID]
	if !ok {
		q = &dispatchQueue{}
		h.queues[workerID] = q
	}
	return q
}

// queue returns workerID's dispatch queue, or nil when nothing was ever parked for it.
func (h *Hub) queue(workerID string) *dispatchQueue {
	h.dqMu.Lock()
	defer h.dqMu.Unlock()
	return h.queues[workerID]
}

// kickDispatch starts (or re-arms) the pump of one worker's queue. It runs OFF the
// caller's goroutine because the pump writes frames, and it is a no-op when nothing is
// parked or a pump is already running (that pump re-checks before it stops).
func (h *Hub) kickDispatch(workerID string) {
	q := h.queue(workerID)
	if q == nil {
		return
	}
	q.mu.Lock()
	if len(q.waiters) == 0 {
		q.mu.Unlock()
		return
	}
	if q.pumping {
		q.again = true
		q.mu.Unlock()
		return
	}
	q.pumping = true
	q.mu.Unlock()
	go h.pumpDispatch(workerID)
}

// pumpDispatch re-dispatches workerID's parked dispatches head-first until the worker is
// full again, the queue is empty or a dispatch fails (offline / draining / write error),
// which fails every remaining waiter with the same error.
func (h *Hub) pumpDispatch(workerID string) {
	q := h.queue(workerID)
	if q == nil {
		return
	}
	for {
		q.runMu.Lock()
		q.mu.Lock()
		q.again = false
		if len(q.waiters) == 0 {
			q.pumping = false
			q.mu.Unlock()
			q.runMu.Unlock()
			return
		}
		w := q.waiters[0]
		q.waiters = q.waiters[1:]
		q.mu.Unlock()

		err := h.Dispatch(workerID, w.d)
		if errors.Is(err, ErrWorkerAtCapacity) {
			// Still full after all: put the head back and stop, unless something released
			// capacity while we were checking.
			q.mu.Lock()
			q.waiters = append([]*dispatchWaiter{w}, q.waiters...)
			again := q.again
			if !again {
				q.pumping = false
			}
			q.mu.Unlock()
			q.runMu.Unlock()
			if !again {
				return
			}
			continue
		}
		w.deliver(err)
		q.runMu.Unlock()
		if err != nil {
			// The worker is gone / draining, or the frame could not be written: every
			// dispatch behind this one can only fail the same way.
			h.failQueue(q, err)
			return
		}
	}
}

// failQueue fails (and drops) every parked dispatch of q with err, ending the pump.
func (h *Hub) failQueue(q *dispatchQueue, err error) {
	q.runMu.Lock()
	q.mu.Lock()
	rest := q.waiters
	q.waiters = nil
	q.pumping = false
	q.mu.Unlock()
	q.runMu.Unlock()
	for _, w := range rest {
		w.deliver(err)
	}
}

// failDispatchQueue fails every dispatch parked on workerID (the disconnect path): a
// queued job must not sit until its own queue cap expires when the worker it waits for is
// already gone.
func (h *Hub) failDispatchQueue(workerID string, err error) {
	if q := h.queue(workerID); q != nil {
		h.failQueue(q, err)
	}
}

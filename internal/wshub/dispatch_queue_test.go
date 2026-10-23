package wshub

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/wsproto"
)

// readDispatchJob reads frames from the worker side until a dispatch arrives and returns
// its job id (heartbeat pings and other control frames are skipped).
func readDispatchJob(t *testing.T, ctx context.Context, conn *websocket.Conn) string {
	t.Helper()
	for {
		rctx, cancel := context.WithTimeout(ctx, wait.Timeout(t, 10*time.Second))
		env, err := readEnvelope(rctx, conn)
		cancel()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if env.Type == wsproto.TypeDispatch {
			return env.JobID
		}
	}
}

// waitQueued waits until workerID's capacity queue holds n parked dispatches (the queue
// is created only once something is parked, so 0 means "nothing parked yet").
func waitQueued(t *testing.T, hub *Hub, workerID string, n int) {
	t.Helper()
	wait.Until(t, wait.Timeout(t, 10*time.Second), "expected state", func() bool {
		q := hub.queue(workerID)
		if q == nil {
			return n == 0
		}
		q.mu.Lock()
		defer q.mu.Unlock()
		return len(q.waiters) == n
	})
}

// TestDispatchWaitQueuesAtCapacity is the acceptance test of gofer-y05q: a dispatch to a
// worker already at its max_concurrent is parked (not rejected), reports the capacity it
// waits for, and goes out — in submission order — as the worker's results free slots.
func TestDispatchWaitQueuesAtCapacity(t *testing.T) {
	hub := shortHeartbeat(map[string]string{"w1": "w1"}, time.Second, 3*time.Second)
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 20*time.Second))
	defer cancel()

	conn := dialAndRegisterFull(t, ctx, wsURL, wsproto.Register{
		WorkerID:        "w1",
		ProtocolVersion: wsproto.CurrentProtocolVersion,
		MaxConcurrent:   1,
	})
	defer conn.Close(websocket.StatusNormalClosure, "")
	wait.Until(t, wait.Timeout(t, 10*time.Second), "expected state", func() bool { return hub.IsOnline("w1") })

	// j1 fills the worker's single slot.
	_ = registerSink(t, hub, "w1", "j1", newFakeSink())
	if err := hub.Dispatch("w1", wsproto.Dispatch{JobID: "j1"}); err != nil {
		t.Fatalf("Dispatch j1: %v", err)
	}
	if got := readDispatchJob(t, ctx, conn); got != "j1" {
		t.Fatalf("first dispatch = %q, want j1", got)
	}

	// j2 and j3 arrive at capacity: they must QUEUE (Dispatch alone still refuses, so the
	// old hard failure stays available to callers that do not want to wait).
	if err := hub.Dispatch("w1", wsproto.Dispatch{JobID: "j3-probe"}); !errors.Is(err, ErrWorkerAtCapacity) {
		t.Fatalf("plain Dispatch at capacity = %v, want ErrWorkerAtCapacity", err)
	}
	waits := make(chan [2]int, 2)
	parked := make(chan error, 2)
	park := func(jobID string, wantQueued int) {
		_ = registerSink(t, hub, "w1", jobID, newFakeSink())
		go func() {
			parked <- hub.DispatchWait(ctx, "w1", wsproto.Dispatch{JobID: jobID}, func(inFlight, max int) {
				waits <- [2]int{inFlight, max}
			})
		}()
		// Park them one at a time so the FIFO order the pump preserves is the order the
		// submissions actually arrived in.
		waitQueued(t, hub, "w1", wantQueued)
		select {
		case got := <-waits:
			if got != [2]int{1, 1} {
				t.Fatalf("onWait reported in_flight/max = %v, want {1 1}", got)
			}
		case <-time.After(wait.Timeout(t, 10*time.Second)):
			t.Fatal("parked dispatch did not report its capacity")
		}
	}
	park("j2", 1)
	park("j3", 2)

	// j1 finishing frees the slot: the FIRST parked job (j2) goes out, not j3.
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type: wsproto.TypeResult, JobID: "j1",
		Payload: mustRaw(wsproto.Result{JobID: "j1", Status: "done"}),
	}); err != nil {
		t.Fatalf("write result j1: %v", err)
	}
	if got := readDispatchJob(t, ctx, conn); got != "j2" {
		t.Fatalf("dispatch after j1 finished = %q, want j2 (submission order)", got)
	}
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type: wsproto.TypeResult, JobID: "j2",
		Payload: mustRaw(wsproto.Result{JobID: "j2", Status: "done"}),
	}); err != nil {
		t.Fatalf("write result j2: %v", err)
	}
	if got := readDispatchJob(t, ctx, conn); got != "j3" {
		t.Fatalf("dispatch after j2 finished = %q, want j3", got)
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-parked:
			if err != nil {
				t.Fatalf("queued dispatch failed: %v", err)
			}
		case <-time.After(wait.Timeout(t, 10*time.Second)):
			t.Fatal("queued dispatch never returned")
		}
	}
}

// TestDispatchWaitFailsWhenWorkerLeaves: a worker that goes away fails the dispatches
// parked on its capacity right away (the same "worker offline" a dispatch would report)
// instead of leaving them until their own queue cap expires.
func TestDispatchWaitFailsWhenWorkerLeaves(t *testing.T) {
	hub := shortHeartbeat(map[string]string{"w1": "w1"}, 100*time.Millisecond, 250*time.Millisecond)
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 20*time.Second))
	defer cancel()

	conn := dialAndRegisterFull(t, ctx, wsURL, wsproto.Register{
		WorkerID:        "w1",
		ProtocolVersion: wsproto.CurrentProtocolVersion,
		MaxConcurrent:   1,
	})
	wait.Until(t, wait.Timeout(t, 10*time.Second), "expected state", func() bool { return hub.IsOnline("w1") })
	_ = registerSink(t, hub, "w1", "j1", newFakeSink())
	if err := hub.Dispatch("w1", wsproto.Dispatch{JobID: "j1"}); err != nil {
		t.Fatalf("Dispatch j1: %v", err)
	}
	if got := readDispatchJob(t, ctx, conn); got != "j1" {
		t.Fatalf("first dispatch = %q, want j1", got)
	}

	parked := make(chan error, 1)
	go func() {
		parked <- hub.DispatchWait(ctx, "w1", wsproto.Dispatch{JobID: "j2"}, nil)
	}()
	waitQueued(t, hub, "w1", 1)

	_ = conn.Close(websocket.StatusNormalClosure, "")
	select {
	case err := <-parked:
		if !errors.Is(err, ErrWorkerOffline) {
			t.Fatalf("queued dispatch error = %v, want ErrWorkerOffline", err)
		}
	case <-time.After(wait.Timeout(t, 10*time.Second)):
		t.Fatal("queued dispatch was not failed when the worker left")
	}
}

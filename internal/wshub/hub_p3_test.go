package wshub

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/wsproto"
)

// shortHeartbeat returns a hub with a tiny ping/read-deadline so half-open and
// heartbeat behaviour can be exercised in milliseconds rather than 45 s.
func shortHeartbeat(bindings map[string]string, ping, read time.Duration) *Hub {
	h := New(bindings)
	h.SetHeartbeat(HeartbeatConfig{PingInterval: ping, ReadDeadline: read})
	return h
}

// TestHeartbeatConfigDefaults verifies the default/invariant resolution: unset
// fields fall to the package defaults and a read deadline < 2× ping is bumped.
func TestHeartbeatConfigDefaults(t *testing.T) {
	d := HeartbeatConfig{}.withDefaults()
	if d.PingInterval != DefaultPingInterval || d.ReadDeadline != DefaultReadDeadline {
		t.Fatalf("defaults = %+v, want ping=%s read=%s", d, DefaultPingInterval, DefaultReadDeadline)
	}
	// read deadline too small relative to ping → bumped to 3× ping.
	bumped := HeartbeatConfig{PingInterval: 10 * time.Second, ReadDeadline: 11 * time.Second}.withDefaults()
	if bumped.ReadDeadline != 30*time.Second {
		t.Fatalf("read deadline not bumped: got %s, want 30s", bumped.ReadDeadline)
	}
	// a valid explicit config is preserved.
	keep := HeartbeatConfig{PingInterval: 5 * time.Second, ReadDeadline: 20 * time.Second}.withDefaults()
	if keep.PingInterval != 5*time.Second || keep.ReadDeadline != 20*time.Second {
		t.Fatalf("valid config altered: %+v", keep)
	}
}

// TestHalfOpenDetection (acceptance #1): a worker registers, the hub holds an
// in-flight job, then the worker goes silent (never sends another frame, never
// replies to ping — simulating a half-open TCP). Within the read-deadline window
// the hub must tear the connection down and fail the in-flight job via the sink's
// OnDisconnect path.
func TestHalfOpenDetection(t *testing.T) {
	hub := shortHeartbeat(map[string]string{"w1": "w1"}, 30*time.Millisecond, 120*time.Millisecond)
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, reg := dialAndRegister(t, ctx, wsURL, "w1")
	defer conn.Close(websocket.StatusNormalClosure, "")
	if !reg.Accepted {
		t.Fatal("register rejected")
	}
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })

	// Register a sink and reserve an in-flight job (mirrors what Dispatch does).
	sink := newFakeSink()
	if err := hub.RegisterSink("w1", "j1", sink); err != nil {
		t.Fatalf("RegisterSink: %v", err)
	}
	if err := hub.Dispatch("w1", wsproto.Dispatch{JobID: "j1"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// The test worker never reads (so it never replies to ping) and never sends
	// anything more — the hub's read loop must time out on its read deadline.
	select {
	case err := <-sink.lost:
		if err == nil || err.Error() != "worker disconnected" {
			t.Fatalf("OnDisconnect err = %v, want 'worker disconnected'", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("half-open not detected within the read-deadline window")
	}
	// The worker must be evicted from the registry.
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return !ok })
}

// TestWorkerDisconnectMidJobFailsJob (acceptance #4): a clean close of the worker
// connection mid-job must fail the in-flight job (worker-lost MVP, §5.3).
func TestWorkerDisconnectMidJobFailsJob(t *testing.T) {
	hub := shortHeartbeat(map[string]string{"w1": "w1"}, time.Second, 3*time.Second)
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, reg := dialAndRegister(t, ctx, wsURL, "w1")
	if !reg.Accepted {
		t.Fatal("register rejected")
	}
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })

	sink := newFakeSink()
	if err := hub.RegisterSink("w1", "j1", sink); err != nil {
		t.Fatalf("RegisterSink: %v", err)
	}
	if err := hub.Dispatch("w1", wsproto.Dispatch{JobID: "j1"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// Hard-close the worker connection mid-job.
	_ = conn.Close(websocket.StatusNormalClosure, "bye")

	select {
	case err := <-sink.lost:
		if err != errWorkerDisconnected {
			t.Fatalf("OnDisconnect err = %v, want errWorkerDisconnected", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight job not failed on worker disconnect")
	}
}

// TestNoResultAfterDisconnect proves a result that landed BEFORE disconnect wins
// (OnDisconnect after a Finish does not override the real outcome). The sink's
// lostCh value is simply never consumed by Run.
func TestResultThenDisconnectKeepsResult(t *testing.T) {
	s := newFakeSink()
	s.Finish(wsproto.Result{JobID: "j1", Status: "done"})
	// A subsequent disconnect signal must not clobber the already-delivered result.
	s.OnDisconnect(errWorkerDisconnected)
	select {
	case res := <-s.finished:
		if res.Status != "done" {
			t.Fatalf("result lost: %+v", res)
		}
	default:
		t.Fatal("result not delivered")
	}
}

// TestDispatchAtCapacity (acceptance #6 capacity half): a worker advertising
// max_concurrent=2 accepts two dispatches but rejects the third with
// ErrWorkerAtCapacity. A completed (result) job frees the slot.
func TestDispatchAtCapacity(t *testing.T) {
	hub := shortHeartbeat(map[string]string{"w1": "w1"}, time.Second, 3*time.Second)
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Register WITH max_concurrent=2.
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(1 << 20)
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type:    wsproto.TypeRegister,
		Payload: mustRaw(wsproto.Register{WorkerID: "w1", MaxConcurrent: 2}),
	}); err != nil {
		t.Fatalf("write register: %v", err)
	}
	env, err := readEnvelope(ctx, conn)
	if err != nil {
		t.Fatalf("read registered: %v", err)
	}
	if reg, _ := wsproto.As[wsproto.Registered](env); !reg.Accepted {
		t.Fatal("register rejected")
	}
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })

	for _, id := range []string{"j1", "j2"} {
		_ = hub.RegisterSink("w1", id, newFakeSink())
		if err := hub.Dispatch("w1", wsproto.Dispatch{JobID: id}); err != nil {
			t.Fatalf("Dispatch %s: %v", id, err)
		}
	}
	// Third dispatch must be rejected at capacity.
	_ = hub.RegisterSink("w1", "j3", newFakeSink())
	if err := hub.Dispatch("w1", wsproto.Dispatch{JobID: "j3"}); err != ErrWorkerAtCapacity {
		t.Fatalf("3rd dispatch err = %v, want ErrWorkerAtCapacity", err)
	}

	// Finishing j1 (result frame) frees a slot, so j3 now succeeds.
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type: wsproto.TypeResult, JobID: "j1",
		Payload: mustRaw(wsproto.Result{JobID: "j1", Status: "done"}),
	}); err != nil {
		t.Fatalf("write result: %v", err)
	}
	waitFor(t, func() bool { return hub.Dispatch("w1", wsproto.Dispatch{JobID: "j3"}) == nil })
}

// TestSupersededReplacementDoesNotFailJobs (acceptance #5, §5.5): a same-worker_id
// reconnect replaces the prior connection; the replaced connection's teardown must
// NOT fail the in-flight job the new connection has taken over.
func TestSupersededReplacementDoesNotFailJobs(t *testing.T) {
	// A short read-deadline keeps the test fast: the replaced (superseded) conn's
	// read loop unblocks promptly on the graceful close, and we still assert no
	// false worker-lost was fired for the taken-over job.
	hub := shortHeartbeat(map[string]string{"w1": "w1"}, 100*time.Millisecond, 250*time.Millisecond)
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First connection + an in-flight job with a sink that records OnDisconnect.
	conn1, reg1 := dialAndRegister(t, ctx, wsURL, "w1")
	defer conn1.Close(websocket.StatusNormalClosure, "")
	if !reg1.Accepted {
		t.Fatal("register1 rejected")
	}
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })
	sink := newFakeSink()
	if err := hub.RegisterSink("w1", "j1", sink); err != nil {
		t.Fatalf("RegisterSink: %v", err)
	}
	if err := hub.Dispatch("w1", wsproto.Dispatch{JobID: "j1"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	wc1, _ := hub.reg.Get("w1")

	// Second connection with the SAME worker_id (same token/callerID) replaces it.
	conn2, reg2 := dialAndRegister(t, ctx, wsURL, "w1")
	defer conn2.Close(websocket.StatusNormalClosure, "")
	if !reg2.Accepted {
		t.Fatal("register2 rejected")
	}
	// The registry now points at the NEW conn.
	waitFor(t, func() bool { wc, ok := hub.reg.Get("w1"); return ok && wc != wc1 })

	// The superseded old conn must NOT have fired worker-lost for j1.
	select {
	case err := <-sink.lost:
		t.Fatalf("superseded replacement wrongly failed in-flight job: %v", err)
	case <-time.After(300 * time.Millisecond):
		// good: no worker-lost for the taken-over job.
	}
}

// TestRestartReplacementFailsInFlightJob: when a NEW worker process (different
// instance_id) registers under the same worker_id, the old connection is NOT
// superseded, so its teardown fails the in-flight job the dead process can no longer
// finish — the orphan-job fix (z8ow). Mirror of the same-instance exemption test.
func TestRestartReplacementFailsInFlightJob(t *testing.T) {
	hub := shortHeartbeat(map[string]string{"w1": "w1"}, 100*time.Millisecond, 250*time.Millisecond)
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First process (instance A) + an in-flight job with a sink recording OnDisconnect.
	conn1, reg1 := dialAndRegisterInstance(t, ctx, wsURL, "w1", "inst-A")
	defer conn1.Close(websocket.StatusNormalClosure, "")
	if !reg1.Accepted {
		t.Fatal("register1 rejected")
	}
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })
	sink := newFakeSink()
	if err := hub.RegisterSink("w1", "j1", sink); err != nil {
		t.Fatalf("RegisterSink: %v", err)
	}
	if err := hub.Dispatch("w1", wsproto.Dispatch{JobID: "j1"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	wc1, _ := hub.reg.Get("w1")

	// A RESTARTED worker process (different instance_id) registers under the same id.
	conn2, reg2 := dialAndRegisterInstance(t, ctx, wsURL, "w1", "inst-B")
	defer conn2.Close(websocket.StatusNormalClosure, "")
	if !reg2.Accepted {
		t.Fatal("register2 rejected")
	}
	waitFor(t, func() bool { wc, ok := hub.reg.Get("w1"); return ok && wc != wc1 })

	// The old conn was a different instance → not superseded → j1 must be failed
	// (worker-lost), not left hanging.
	select {
	case err := <-sink.lost:
		if err == nil {
			t.Fatal("expected a non-nil worker-lost error for the orphaned job")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restart replacement did not fail the orphaned in-flight job (z8ow regression)")
	}
}

// TestTryReserveConcurrent (-race): many goroutines race to reserve capacity on
// one worker conn; the in-flight count must never exceed maxConcurrent.
func TestTryReserveConcurrent(t *testing.T) {
	wc := newWorkerConn("w", "w", nil, wsproto.Register{MaxConcurrent: 4})
	const goroutines = 64
	var granted int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			jobID := string(rune('a'+n%26)) + string(rune('0'+n/26))
			if wc.tryReserve(jobID) {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if granted > 4 {
		t.Fatalf("granted %d reservations, max_concurrent=4 exceeded", granted)
	}
	if len(wc.inflightJobs()) != int(granted) {
		t.Fatalf("inflight set size %d != granted %d", len(wc.inflightJobs()), granted)
	}
}

// TestMultiWorkerRouting (acceptance #6 routing half): two distinct workers are
// online; a dispatch for w1 is delivered to w1 only and w2 receives nothing.
func TestMultiWorkerRouting(t *testing.T) {
	hub := shortHeartbeat(map[string]string{"w1": "w1", "w2": "w2"}, time.Second, 3*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Two routers binding two different caller ids (so each worker registers under
	// its own identity).
	_, ws1 := hubServer(t, hub, "w1")
	_, ws2 := hubServer(t, hub, "w2")

	conn1, reg1 := dialAndRegister(t, ctx, ws1, "w1")
	defer conn1.Close(websocket.StatusNormalClosure, "")
	conn2, reg2 := dialAndRegister(t, ctx, ws2, "w2")
	defer conn2.Close(websocket.StatusNormalClosure, "")
	if !reg1.Accepted || !reg2.Accepted {
		t.Fatalf("register: w1=%v w2=%v", reg1.Accepted, reg2.Accepted)
	}
	waitFor(t, func() bool { _, a := hub.reg.Get("w1"); _, b := hub.reg.Get("w2"); return a && b })

	// Dispatch a job to w1 only.
	if err := hub.Dispatch("w1", wsproto.Dispatch{JobID: "jw1", ProjectKey: "p"}); err != nil {
		t.Fatalf("Dispatch w1: %v", err)
	}

	// w1 must receive the dispatch.
	rctx, rcancel := context.WithTimeout(ctx, 2*time.Second)
	defer rcancel()
	env, err := readEnvelope(rctx, conn1)
	if err != nil {
		t.Fatalf("w1 read dispatch: %v", err)
	}
	if env.Type != wsproto.TypeDispatch || env.JobID != "jw1" {
		t.Fatalf("w1 got %s/%s, want dispatch/jw1", env.Type, env.JobID)
	}

	// w2 must NOT receive any dispatch (only its own heartbeat ping may arrive). Read
	// with a short deadline; assert no dispatch frame shows up.
	w2ctx, w2cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer w2cancel()
	for {
		env2, err := readEnvelope(w2ctx, conn2)
		if err != nil {
			break // deadline: no dispatch leaked to w2 (good)
		}
		if env2.Type == wsproto.TypeDispatch {
			t.Fatalf("dispatch leaked to w2: %+v", env2)
		}
		// ping/pong/other control frames are fine; keep draining until the deadline.
	}
}

// TestWorkerSnapshotExposed (C6/P4): the registry exposes a read-only snapshot
// of a worker's live state — last_heartbeat / in-flight count / labels /
// projects / agents — and
// reports ok=false for an offline / never-seen worker. The in-flight count
// reflects reservations and the register metadata slices are defensive copies.
func TestWorkerSnapshotExposed(t *testing.T) {
	r := newRegistry()

	// Offline worker → no snapshot.
	if _, ok := r.WorkerSnapshot("w1"); ok {
		t.Fatal("offline worker should have no snapshot")
	}

	wc := newWorkerConn("w1", "w1", nil, wsproto.Register{
		WorkerID:      "w1",
		Labels:        []string{"gpu", "linux"},
		Projects:      []string{"proj-a", "proj-b"},
		Agents:        []string{"codex", "exec"},
		MaxConcurrent: 4,
	})
	wc.lastHeartbeat.Store(1750300000)
	wc.tryReserve("j1")
	wc.tryReserve("j2")
	r.Put(wc)

	snap, ok := r.WorkerSnapshot("w1")
	if !ok {
		t.Fatal("online worker should have a snapshot")
	}
	if snap.WorkerID != "w1" || snap.LastHeartbeat != 1750300000 || snap.InFlight != 2 {
		t.Fatalf("snapshot fields wrong: %+v", snap)
	}
	if len(snap.Labels) != 2 || snap.Labels[0] != "gpu" {
		t.Fatalf("snapshot labels wrong: %+v", snap.Labels)
	}
	if len(snap.Projects) != 2 || snap.Projects[0] != "proj-a" {
		t.Fatalf("snapshot projects wrong: %+v", snap.Projects)
	}
	if len(snap.Agents) != 2 || snap.Agents[0] != "codex" {
		t.Fatalf("snapshot agents wrong: %+v", snap.Agents)
	}
	// Register metadata must be defensive copies: mutating the returned slices
	// must not affect the conn's register meta.
	snap.Labels[0] = "MUTATED"
	snap.Projects[0] = "MUTATED"
	snap.Agents[0] = "MUTATED"
	if wc.meta.Labels[0] != "gpu" || wc.meta.Projects[0] != "proj-a" || wc.meta.Agents[0] != "codex" {
		t.Fatal("WorkerSnapshot leaked register metadata slices (not copies)")
	}
}

// TestLastHeartbeatExposed verifies the registry exposes last_heartbeat for C6/P4.
func TestLastHeartbeatExposed(t *testing.T) {
	hub := shortHeartbeat(map[string]string{"w1": "w1"}, 50*time.Millisecond, 200*time.Millisecond)
	if hub.LastHeartbeat("w1") != 0 {
		t.Fatal("offline worker should have 0 last_heartbeat")
	}
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, reg := dialAndRegister(t, ctx, wsURL, "w1")
	defer conn.Close(websocket.StatusNormalClosure, "")
	if !reg.Accepted {
		t.Fatal("register rejected")
	}
	waitFor(t, func() bool { return hub.LastHeartbeat("w1") > 0 })
}

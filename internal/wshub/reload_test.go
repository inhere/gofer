package wshub

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/wsproto"
)

// dialAndRegisterFull dials the hub and registers with a caller-supplied register
// frame, so a reload test can start from a worker with real (and later, changed)
// capabilities. It returns the live worker-side conn.
func dialAndRegisterFull(t *testing.T, ctx context.Context, wsURL string, reg wsproto.Register) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })
	conn.SetReadLimit(1 << 20)
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeRegister, Payload: mustRaw(reg)}); err != nil {
		t.Fatalf("write register: %v", err)
	}
	env, err := readEnvelope(ctx, conn)
	if err != nil {
		t.Fatalf("read registered: %v", err)
	}
	ack, _ := wsproto.As[wsproto.Registered](env)
	if !ack.Accepted {
		t.Fatalf("register rejected: %+v", ack)
	}
	return conn
}

// readReloadFrame reads frames on the worker side until the reload request arrives and
// returns its request_id (heartbeat pings are skipped rather than mistaken for it).
func readReloadFrame(t *testing.T, ctx context.Context, conn *websocket.Conn) string {
	t.Helper()
	for {
		env, err := readEnvelope(ctx, conn)
		if err != nil {
			t.Fatalf("worker never received the reload frame: %v", err)
		}
		if env.Type != wsproto.TypeReload {
			continue
		}
		rl, err := wsproto.As[wsproto.Reload](env)
		if err != nil {
			t.Fatalf("bad reload payload: %v", err)
		}
		if rl.RequestID == "" {
			t.Fatal("reload frame carries no request_id: the receipt could never be correlated")
		}
		return rl.RequestID
	}
}

// reloadReply is the (Caps, error) pair a ReloadWorker call produced, ferried off the
// goroutine that made it.
type reloadReply struct {
	caps wsproto.Caps
	err  error
	took time.Duration
}

// callReload runs hub.ReloadWorker on its own goroutine (it blocks until the receipt)
// and delivers the outcome on the returned channel.
func callReload(h *Hub, ctx context.Context, workerID, reason string) <-chan reloadReply {
	out := make(chan reloadReply, 1)
	go func() {
		started := time.Now()
		caps, err := h.ReloadWorker(ctx, workerID, reason)
		out <- reloadReply{caps: caps, err: err, took: time.Since(started)}
	}()
	return out
}

// TestReloadWorkerOffline: a worker that is not connected cannot be reloaded. This is a
// caller error (409), never a server fault (500) — asserted by the sentinel.
func TestReloadWorkerOffline(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := hub.ReloadWorker(ctx, "w1", "test")
	if !errors.Is(err, ErrWorkerOffline) {
		t.Fatalf("ReloadWorker on an offline worker = %v, want ErrWorkerOffline", err)
	}
}

// TestReloadWorkerTooOld: a worker one protocol behind is connected and fully able to
// run jobs — it simply has no reload frame to answer with. The hub must say so at once
// (so the operator gets "upgrade and restart it"), and must NOT send a frame the peer
// would drop and then sit out the deadline.
func TestReloadWorkerTooOld(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dialAndRegisterFull(t, ctx, wsURL, wsproto.Register{
		WorkerID: "w1", ProtocolVersion: wsproto.ReloadMinProtocolVersion - 1,
	})
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })

	start := time.Now()
	_, err := hub.ReloadWorker(ctx, "w1", "test")
	if !errors.Is(err, ErrWorkerTooOld) {
		t.Fatalf("ReloadWorker on a v%d worker = %v, want ErrWorkerTooOld",
			wsproto.ReloadMinProtocolVersion-1, err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the version gate took %v: it must fail fast, not wait for a receipt", took)
	}
	// It stays a usable worker: the gate is a capability check, not an eviction.
	if !hub.IsOnline("w1") {
		t.Fatal("a too-old worker must remain registered and usable after a refused reload")
	}
}

// TestReloadWorkerAppliesCapsBeforeReturning (acceptance 1): a successful reload must
// leave the hub's view of the worker ALREADY updated by the time the caller unblocks.
// The caller's very next act is to answer an HTTP request whose reader may immediately
// query the worker's capabilities — a window where it still reports the old ones is a
// reload that looks like it did nothing.
func TestReloadWorkerAppliesCapsBeforeReturning(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn := dialAndRegisterFull(t, ctx, wsURL, wsproto.Register{
		WorkerID: "w1", ProtocolVersion: wsproto.CurrentProtocolVersion,
		MaxConcurrent: 1,
		Projects:      []string{"alpha"},
		Agents:        []string{"claude"},
		AgentCaps:     []wsproto.AgentBrief{{Key: "claude", Type: "cli-agent", Interactive: true}},
	})
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })

	res := callReload(hub, ctx, "w1", "config changed")
	reqID := readReloadFrame(t, ctx, conn)

	// The worker re-read its config: it gained a project and an agent, and raised its
	// concurrency.
	newCaps := wsproto.Caps{
		Labels:   []string{"gpu"},
		Projects: []string{"alpha", "beta"},
		Agents:   []string{"claude", "tty-demo"},
		AgentCaps: []wsproto.AgentBrief{
			{Key: "claude", Type: "cli-agent", Interactive: true},
			{Key: "tty-demo", Type: "exec", Interactive: true},
		},
		MaxConc: 4,
	}
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type:    wsproto.TypeReloadResult,
		Payload: mustRaw(wsproto.ReloadResult{RequestID: reqID, OK: true, Caps: &newCaps}),
	}); err != nil {
		t.Fatalf("worker write reload_result: %v", err)
	}

	var got reloadReply
	select {
	case got = <-res:
	case <-ctx.Done():
		t.Fatal("ReloadWorker never returned after the worker acknowledged")
	}
	if got.err != nil {
		t.Fatalf("ReloadWorker = %v, want success", got.err)
	}
	if len(got.caps.Agents) != 2 || got.caps.Agents[1] != "tty-demo" {
		t.Fatalf("returned caps = %+v, want the worker's new snapshot", got.caps)
	}

	// The registry is updated ALREADY — no polling, no waitFor: if this needs a retry
	// the ordering guarantee is broken.
	snap, ok := hub.WorkerSnapshot("w1")
	if !ok {
		t.Fatal("worker vanished from the registry")
	}
	if len(snap.Projects) != 2 || snap.Projects[1] != "beta" {
		t.Fatalf("projects = %v, want the reloaded [alpha beta] visible the moment ReloadWorker returned", snap.Projects)
	}
	if len(snap.AgentCaps) != 2 || snap.AgentCaps[1].Key != "tty-demo" {
		t.Fatalf("agent_caps = %+v, want the newly configured agent", snap.AgentCaps)
	}
	if snap.Labels[0] != "gpu" {
		t.Fatalf("labels = %v, want [gpu]", snap.Labels)
	}
	// …and the reload moved the limit that actually admits jobs, not just the display.
	wc, _ := hub.reg.Get("w1")
	for i := 0; i < 4; i++ {
		if !wc.tryReserve("j" + string(rune('a'+i))) {
			t.Fatalf("job %d refused although the reload raised max_concurrent to 4", i)
		}
	}
	if wc.tryReserve("j-overflow") {
		t.Fatal("a 5th job was admitted at max_concurrent=4")
	}
}

// TestReloadWorkerRejectedCarriesWorkerReason (acceptance 4): a worker with a broken
// config keeps running its OLD config and says why. That reason is the entire value of
// the synchronous receipt — the caller has to hand it to the user verbatim, so the
// error must carry it, and the capability view must stay exactly as it was.
func TestReloadWorkerRejectedCarriesWorkerReason(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn := dialAndRegisterFull(t, ctx, wsURL, wsproto.Register{
		WorkerID: "w1", ProtocolVersion: wsproto.CurrentProtocolVersion,
		Projects: []string{"alpha"}, Agents: []string{"claude"},
	})
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })

	res := callReload(hub, ctx, "w1", "bad config")
	reqID := readReloadFrame(t, ctx, conn)

	const workerReason = "yaml: line 7: mapping values are not allowed in this context"
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type:    wsproto.TypeReloadResult,
		Payload: mustRaw(wsproto.ReloadResult{RequestID: reqID, OK: false, Err: workerReason}),
	}); err != nil {
		t.Fatalf("worker write reload_result: %v", err)
	}

	var got reloadReply
	select {
	case got = <-res:
	case <-ctx.Done():
		t.Fatal("ReloadWorker never returned after the worker refused")
	}
	if got.err == nil {
		t.Fatal("a refused reload must be an error, not a silent success")
	}
	var rejected *ReloadRejected
	if !errors.As(got.err, &rejected) {
		t.Fatalf("error = %T (%v), want *ReloadRejected so the caller can map it to a client error", got.err, got.err)
	}
	if rejected.Reason != workerReason {
		t.Fatalf("Reason = %q, want the worker's message verbatim (%q)", rejected.Reason, workerReason)
	}
	if !strings.Contains(got.err.Error(), workerReason) {
		t.Fatalf("Error() = %q, must contain the worker's reason", got.err.Error())
	}
	// A refused reload changes nothing: the worker still runs its old config.
	snap, _ := hub.WorkerSnapshot("w1")
	if len(snap.Projects) != 1 || snap.Projects[0] != "alpha" || len(snap.Agents) != 1 {
		t.Fatalf("a refused reload must leave the capability view untouched, got %+v", snap)
	}
	if !hub.IsOnline("w1") {
		t.Fatal("a worker that refused a reload is healthy and must stay registered")
	}
}

// TestReloadWorkerDisconnectMidWait: the worker dies (or its process is killed) while
// the caller is parked on the receipt. The wait MUST end at the disconnect, not at the
// deadline — the receipt is provably never coming, and holding an HTTP request open for
// the full timeout on a process that no longer exists is a bug.
func TestReloadWorkerDisconnectMidWait(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	// A deliberately GENEROUS deadline: if the implementation waits it out, the timing
	// assertion below fails — which is exactly the bug being guarded.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn := dialAndRegisterFull(t, ctx, wsURL, wsproto.Register{
		WorkerID: "w1", ProtocolVersion: wsproto.CurrentProtocolVersion,
	})
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })

	res := callReload(hub, ctx, "w1", "worker will die")
	readReloadFrame(t, ctx, conn) // the request went out …
	_ = conn.CloseNow()           // … and the worker dies before answering

	select {
	case got := <-res:
		if !errors.Is(got.err, ErrWorkerOffline) {
			t.Fatalf("ReloadWorker after a mid-wait disconnect = %v, want ErrWorkerOffline", got.err)
		}
		if got.took > 5*time.Second {
			t.Fatalf("waited %v for a worker that had already disconnected: the waiter must be woken by the disconnect", got.took)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReloadWorker is still waiting for a receipt from a disconnected worker (it will hang until the deadline)")
	}
}

// TestReloadWorkerTimeoutNoPendingLeak: a worker that never answers must not hold the
// caller forever, and the abandoned request must not stay in the pending map — a leak
// there is unbounded memory on a worker that goes deaf.
func TestReloadWorkerTimeoutNoPendingLeak(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()

	conn := dialAndRegisterFull(t, dialCtx, wsURL, wsproto.Register{
		WorkerID: "w1", ProtocolVersion: wsproto.CurrentProtocolVersion,
	})
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })
	wc, _ := hub.reg.Get("w1")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := hub.ReloadWorker(ctx, "w1", "worker goes deaf")
	if !errors.Is(err, ErrReloadTimeout) {
		t.Fatalf("ReloadWorker with no receipt = %v, want ErrReloadTimeout", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("the wait outlived its deadline by a lot (%v)", took)
	}
	if n := wc.pendingReloads(); n != 0 {
		t.Fatalf("pending reload requests = %d after the waiter left, want 0 (leak)", n)
	}
	// The worker did receive the request — the timeout is about the ANSWER, and the
	// connection is still fully alive.
	if id := readReloadFrame(t, dialCtx, conn); id == "" {
		t.Fatal("no reload frame reached the worker")
	}
	if !hub.IsOnline("w1") {
		t.Fatal("a timed-out reload must not tear the connection down")
	}
}

// TestReloadResultUnknownRequestIgnored: a receipt whose request_id nobody is waiting
// on — a late answer to a timed-out request, a duplicate, a confused worker — must be
// dropped harmlessly. It must not panic on a nil channel and must not stop the read
// loop, which is still streaming every other job on that connection.
func TestReloadResultUnknownRequestIgnored(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn := dialAndRegisterFull(t, ctx, wsURL, wsproto.Register{
		WorkerID: "w1", ProtocolVersion: wsproto.CurrentProtocolVersion,
		Projects: []string{"alpha"},
	})
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })

	// A receipt for a request that was never made / already gave up. It even carries
	// caps: an OK receipt's caps are applied regardless of whether a waiter is left
	// (the worker DID reload — dropping the caps would leave the hub stale).
	orphanCaps := wsproto.Caps{Projects: []string{"gamma"}, Agents: []string{"exec"}}
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type:    wsproto.TypeReloadResult,
		Payload: mustRaw(wsproto.ReloadResult{RequestID: "nobody-is-waiting", OK: true, Caps: &orphanCaps}),
	}); err != nil {
		t.Fatalf("worker write: %v", err)
	}

	// The read loop must still be alive: a normal job still streams to its result.
	sink := newFakeSink()
	if err := hub.RegisterSink("w1", "j1", sink); err != nil {
		t.Fatalf("RegisterSink: %v", err)
	}
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type: wsproto.TypeResult, JobID: "j1",
		Payload: mustRaw(wsproto.Result{JobID: "j1", Status: "done"}),
	}); err != nil {
		t.Fatalf("worker write result: %v", err)
	}
	select {
	case <-sink.finished:
	case <-ctx.Done():
		t.Fatal("an unmatched reload receipt broke the read loop")
	}
}

// TestUnsolicitedCapsFrameUpdatesRegistry: the worker reloaded on its own (SIGHUP) and
// broadcasts its new capabilities with no request to answer. The hub must take them —
// otherwise it keeps routing on capabilities the worker no longer has.
func TestUnsolicitedCapsFrameUpdatesRegistry(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn := dialAndRegisterFull(t, ctx, wsURL, wsproto.Register{
		WorkerID: "w1", ProtocolVersion: wsproto.CurrentProtocolVersion,
		MaxConcurrent: 1,
		Projects:      []string{"alpha"}, Agents: []string{"claude"},
	})
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })

	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type: wsproto.TypeCaps,
		Payload: mustRaw(wsproto.Caps{
			Labels: []string{"edge"}, Projects: []string{"beta"},
			Agents:    []string{"exec"},
			AgentCaps: []wsproto.AgentBrief{{Key: "exec", Type: "exec"}},
			MaxConc:   2,
		}),
	}); err != nil {
		t.Fatalf("worker write caps: %v", err)
	}

	waitFor(t, func() bool {
		snap, ok := hub.WorkerSnapshot("w1")
		return ok && len(snap.Projects) == 1 && snap.Projects[0] == "beta"
	})
	snap, _ := hub.WorkerSnapshot("w1")
	if len(snap.Agents) != 1 || snap.Agents[0] != "exec" || snap.Labels[0] != "edge" {
		t.Fatalf("a SIGHUP caps broadcast did not fully replace the capability view: %+v", snap)
	}
	wc, _ := hub.reg.Get("w1")
	if !wc.tryReserve("j1") || !wc.tryReserve("j2") {
		t.Fatal("the broadcast raised max_concurrent to 2 but admission still enforces 1")
	}
	if wc.tryReserve("j3") {
		t.Fatal("a 3rd job was admitted at max_concurrent=2")
	}
}

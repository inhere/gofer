package wshub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/wsproto"
)

// maxWSReadBytes caps a single inbound WS message (P0 locked: prevent one
// oversized frame from blowing up memory; per-job throughput back-pressure is in
// the sink, §4.3). It is a var so tests can shrink it.
var maxWSReadBytes int64 = 8 << 20 // 8 MiB

// Heartbeat / half-open detection defaults (P3 §4.1). All configurable; these
// are the constants applied when serve passes nothing. read deadline is ~3× the
// ping interval so a GC/scheduling hiccup that drops one or two pings does not
// falsely flag a worker offline (and falsely fail its in-flight jobs, §6.1).
const (
	// DefaultPingInterval is how often the hub sends a ping frame to each worker.
	DefaultPingInterval = 15 * time.Second
	// DefaultReadDeadline bounds a single conn.Read; exceeding it tears the
	// connection down (half-open detection, review #7). It MUST be ≥ 2× the ping
	// interval or normal heartbeats would themselves time out.
	DefaultReadDeadline = 45 * time.Second
)

// ErrWorkerOffline is returned by Dispatch/RegisterSink when no live connection
// exists for the target worker_id.
var ErrWorkerOffline = errors.New("worker offline")

// ErrWorkerAtCapacity is returned by Dispatch when the target worker is at its
// advertised max_concurrent (§5.4). Queueing is deferred to WP4; the caller maps
// this to a failed job with a clear error so the submitter can retry / pick
// another worker rather than silently block.
var ErrWorkerAtCapacity = errors.New("worker at capacity")

// errWorkerDisconnected is the error a worker-lost in-flight job is finished with
// (§5.3). The runner returns it as Result.Err; classify maps it to StatusFailed
// and the text flows verbatim into jobs.error.
var errWorkerDisconnected = errors.New("worker disconnected")

// HeartbeatConfig holds the hub-side heartbeat / read-deadline timings. A zero
// value (both fields 0) means "use the package defaults" (resolved in withDefaults).
type HeartbeatConfig struct {
	PingInterval time.Duration
	ReadDeadline time.Duration
}

// withDefaults returns hc with any unset (<= 0) field filled from the package
// defaults, then enforces the read deadline ≥ 2× ping invariant (a misconfig
// that would make normal heartbeats time out is corrected up to 3× ping).
func (hc HeartbeatConfig) withDefaults() HeartbeatConfig {
	if hc.PingInterval <= 0 {
		hc.PingInterval = DefaultPingInterval
	}
	if hc.ReadDeadline <= 0 {
		hc.ReadDeadline = DefaultReadDeadline
	}
	if hc.ReadDeadline < 2*hc.PingInterval {
		hc.ReadDeadline = 3 * hc.PingInterval
	}
	return hc
}

// Hub is the serve-process singleton: WS accept + worker registry + per-job
// inbound-frame demux + dispatch + heartbeat. One instance is shared by every
// worker runner (assemble.buildCore). It is safe for concurrent use.
type Hub struct {
	reg *WorkerRegistry
	// bindings maps worker_id → the caller id its token authenticates as
	// (review #1). A register frame is accepted only when bindings[worker_id]
	// equals the connection's authenticated callerID. Built from
	// cfg.Server.Workers at assemble time.
	bindings map[string]string
	nowFn    func() time.Time
	hb       HeartbeatConfig

	// lostFn, when set, is invoked once per in-flight job_id when a worker's
	// connection drops (and the connection was NOT superseded by a same-worker_id
	// replacement, §5.5). The worker runner registers it (via the per-job sink's
	// lost channel) so a dropped worker fails its server-side in-flight jobs
	// (§5.3). It is set through the sink — see RegisterSink / boundedSink — so the
	// hub never imports the runner.
	//
	// (Field reserved: the actual signalling is done through the JobSink's
	// OnDisconnect, keeping the hub decoupled from the runner.)

	// stop, when non-nil, is the serve-level shutdown channel. When it closes the
	// hub gracefully closes every live connection (§5.6). Set via SetStop.
	stop <-chan struct{}

	// policySrc computes the per-worker Policy the hub pushes (T1-E seam). It is the
	// ONLY way the hub obtains a Policy — the hub never imports config or computes a
	// policy itself (verification 17: internal/wshub depends only on internal/wsproto).
	// nil (T1 default, until T3 wires corePolicySource) ⇒ PushPolicyAll is a no-op.
	policySrc PolicySource
}

// PolicySource is the seam through which the hub obtains the Policy for one
// worker without importing config or knowing how a policy is computed (T1-E). ok
// is false when no policy applies to that worker (source not wired, or the worker
// has no projects) — the hub then pushes nothing to it.
type PolicySource interface {
	PolicyFor(workerID string) (wsproto.Policy, bool)
}

// policyWriteTimeout bounds a single policy-frame write so ONE slow/half-open
// connection cannot stall the whole broadcast. Unlike Dispatch/Answer/Cancel
// (which use context.Background()), a policy push must never block indefinitely
// on a wedged writer — a skipped worker re-converges on its next reconnect.
const policyWriteTimeout = 5 * time.Second

// New builds a Hub. bindings is the worker_id → expected caller-id map (from
// cfg.Server.Workers); a nil map means no worker may register (per-worker token
// is mandatory, §7 / review #1).
func New(bindings map[string]string) *Hub {
	if bindings == nil {
		bindings = map[string]string{}
	}
	return &Hub{
		reg:      newRegistry(),
		bindings: bindings,
		nowFn:    time.Now,
		hb:       HeartbeatConfig{}.withDefaults(),
	}
}

// SetHeartbeat overrides the heartbeat timings (serve resolves them from config;
// defaults apply for any unset field). Must be called before Accept handles any
// connection (i.e. at assemble time, single-threaded).
func (h *Hub) SetHeartbeat(hc HeartbeatConfig) { h.hb = hc.withDefaults() }

// SetStop wires the serve-level shutdown channel and starts the shutdown watcher
// goroutine: when stop closes, every live connection is gracefully closed (close
// code 1001 going-away), which unblocks every per-connection read loop and stops
// the heartbeat goroutines (§5.6). Idempotent-unsafe: call once at assemble time.
func (h *Hub) SetStop(stop <-chan struct{}) {
	h.stop = stop
	if stop == nil {
		return
	}
	go func() {
		<-stop
		for _, wc := range h.reg.All() {
			_ = wc.conn.Close(websocket.StatusGoingAway, "hub shutdown")
		}
	}()
}

// SetPolicySource wires the Policy computation seam (T1-E). serve/core calls it
// once at assemble time (single-threaded) before any broadcast. A nil source
// leaves PushPolicyAll a no-op.
func (h *Hub) SetPolicySource(ps PolicySource) { h.policySrc = ps }

// PushPolicyAll broadcasts the current per-worker Policy to every live connection
// that negotiated policy support (SupportsPolicy). It is called OFF the caller's
// config-write lock (core.flushPush): each frame is written under a short
// per-connection deadline (policyWriteTimeout) and a slow/failing connection is
// logged and skipped — never allowed to stall the others (D-HIGH-5). A skipped
// worker re-converges on its next reconnect / the next push.
//
// With no policy source wired (T1, until T3) this returns immediately: there is
// nothing to compute, so the broadcast is a legitimate no-op.
func (h *Hub) PushPolicyAll() {
	if h.policySrc == nil {
		return
	}
	for _, wc := range h.reg.All() {
		if !wsproto.SupportsPolicy(wc.protocolVersion()) {
			continue // pre-v4 worker: cannot receive policy frames (stays on local/legacy)
		}
		pol, ok := h.policySrc.PolicyFor(wc.workerID)
		if !ok {
			continue // no policy applies to this worker
		}
		ctx, cancel := context.WithTimeout(context.Background(), policyWriteTimeout)
		err := wc.writeFrame(ctx, wsproto.TypePolicy, "", pol)
		cancel()
		if err != nil {
			slog.Warn("hub policy push failed; skipping worker",
				"worker_id", wc.workerID, "rev", pol.Rev, "err", err)
			continue
		}
		// E-HIGH-1: mark pending on the broadcast path too (not just the ack), otherwise
		// an online worker pushed a new rev would show not-pending in /v1/meta and the
		// diagnostic would be useless. Committed only after the push is on the wire.
		wc.markPolicyPending(pol.Rev)
	}
}

// catchUpPolicy closes the §7-N1 race window: after Put makes wc visible, re-read the
// policy source and push once more when the current rev is newer than the one the ack
// carried (ackedRev). Without it, a broadcast that fired between the ack's PolicyFor and
// Put — when wc was not yet in the registry — leaves the worker stuck on the ack's rev
// until the next config change. A no-op for a pre-policy (v3) worker or an unwired
// source. Pending is committed only after the frame is written (mark-after-write).
func (h *Hub) catchUpPolicy(ctx context.Context, wc *workerConn, ackedRev int64) {
	if h.policySrc == nil || !wsproto.SupportsPolicy(wc.protocolVersion()) {
		return
	}
	p, ok := h.policySrc.PolicyFor(wc.workerID)
	if !ok || p.Rev <= ackedRev {
		return
	}
	wctx, cancel := context.WithTimeout(ctx, policyWriteTimeout)
	err := wc.writeFrame(wctx, wsproto.TypePolicy, "", p)
	cancel()
	if err != nil {
		slog.Warn("hub policy catch-up push failed; skipping worker",
			"worker_id", wc.workerID, "rev", p.Rev, "err", err)
		return
	}
	wc.markPolicyPending(p.Rev)
}

// nowMillis returns the current unix time in milliseconds (SR102 / Registered).
func (h *Hub) nowMillis() int64 { return h.nowFn().UnixNano() / int64(time.Millisecond) }

// Accept upgrades GET /v1/workers/connect to a WebSocket and runs the
// per-connection read loop. The route layer (httpapi) has already done Bearer
// auth and passes the resolved callerID; Accept does the worker_id↔caller
// binding check (review #1) then the register handshake.
//
// It receives rux's c.Resp directly: rux v2.0.2 fixed the P0 finding — its
// *responseWriter.Hijack() now flushes the recorded 101 status to the underlying
// writer before detaching the connection, so the old wsUpgradeWriter adapter (the
// spike workaround that forced an early flush) is no longer needed.
func (h *Hub) Accept(w http.ResponseWriter, req *http.Request, callerID string) {
	conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{
		// Workers are non-browser, pure-outbound clients: relax the default origin
		// check (P0 proved it is required). OriginPatterns "*" is rejected by the
		// library, so InsecureSkipVerify is the locked choice.
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled, // P0 locked
	})
	if err != nil {
		return // Accept already wrote the handshake rejection
	}
	conn.SetReadLimit(maxWSReadBytes)
	defer conn.Close(websocket.StatusInternalError, "hub defer")

	ctx := req.Context()

	// 1) First frame must be register. The register handshake uses the bare ctx
	// (no read deadline) — a worker connects and registers promptly; the
	// heartbeat read deadline only governs the steady-state read loop.
	env, err := readEnvelope(ctx, conn)
	if err != nil || env.Type != wsproto.TypeRegister {
		slog.Warn("hub rejected worker handshake", "remote", req.RemoteAddr,
			"caller_id", callerID, "reason", "first frame was not a register frame", "err", err)
		_ = conn.Close(websocket.StatusProtocolError, "expected register")
		return
	}
	reg, err := wsproto.As[wsproto.Register](env)
	if err != nil {
		slog.Warn("hub rejected worker handshake", "remote", req.RemoteAddr,
			"caller_id", callerID, "reason", "bad register payload", "err", err)
		_ = conn.Close(websocket.StatusProtocolError, "bad register payload")
		return
	}

	// 2) Token↔worker binding (review #1, mandatory): the register's worker_id
	// must match the worker the presented token is bound to (callerID).
	want, bound := h.bindings[reg.WorkerID]
	if !bound || want != callerID {
		// The #1 operator gotcha: token authenticates as caller_id but register's
		// worker_id is not bound to it (missing server.workers entry, or worker_id
		// mismatch). Log both so the misalignment is obvious.
		slog.Warn("hub rejected worker registration",
			"worker_id", reg.WorkerID, "caller_id", callerID, "bound", bound,
			"reason", "worker_id not bound to this token (check server.workers)")
		_ = writeEnvelope(ctx, conn, wsproto.TypeRegistered, "", wsproto.Registered{
			Accepted:   false,
			Reason:     "worker_id not bound to this token",
			ServerTime: h.nowMillis(),
		})
		_ = conn.Close(websocket.StatusPolicyViolation, "binding")
		return
	}

	// 2b) Version gate (hard incompatibility): the gate is the compatibility FLOOR
	// (MinProtocolVersion), never the version this build implements — a worker one
	// release behind must keep working across a rolling upgrade, and only loses the
	// features its version predates (negotiated per connection, see supportsReload).
	// A pre-federation worker (absent protocol_version → 0) is below the floor: it
	// does not report authoritative capabilities, so it is rejected with an upgrade
	// prompt. The frame extension is additive, so an old worker's register still
	// decodes cleanly — which is exactly why we can answer with a clear Reason here
	// instead of failing to parse.
	if reg.ProtocolVersion < wsproto.MinProtocolVersion {
		slog.Warn("hub rejected worker: protocol too old",
			"worker_id", reg.WorkerID, "worker_proto", reg.ProtocolVersion, "min", wsproto.MinProtocolVersion)
		_ = writeEnvelope(ctx, conn, wsproto.TypeRegistered, "", wsproto.Registered{
			Accepted:   false,
			Reason:     fmt.Sprintf("worker 协议版本过旧(v%d)，请升级 worker 到 v%d 后重连", reg.ProtocolVersion, wsproto.MinProtocolVersion),
			ServerTime: h.nowMillis(),
		})
		_ = conn.Close(websocket.StatusPolicyViolation, "protocol version too old")
		return
	}

	wc := newWorkerConn(reg.WorkerID, callerID, conn, reg)
	wc.remoteAddr = req.RemoteAddr // observability only; hostname is the machine identity
	wc.lastHeartbeat.Store(h.nowFn().Unix())

	// 3) Ack BEFORE the connection enters the registry (B3 / §7-N1). Any broadcast
	// that iterates the registry (e.g. a policy push) must not be able to write a
	// frame onto this connection before the worker has read its registered ack: the
	// worker decodes the first frame As[Registered], so a stray frame ahead of the ack
	// becomes Accepted=false with an empty reason → a bogus "registration rejected" →
	// reconnect storm. Writing the ack through wc.writeFrame keeps a SINGLE writer
	// (writeMu, §7-N2) instead of the package-level writeEnvelope. On ack failure the
	// conn never entered the registry, so there is nothing to Remove — just close.
	//
	// A policy-capable worker (SupportsPolicy) gets its authoritative Policy bundled ON
	// the ack (Q7-b) so it converges on register with no second frame; ackedRev is what
	// the ack carried and the catch-up (step 4b) compares against. ProtocolVersion lets
	// the worker negotiate server-side features off the version THIS build implements.
	ack := wsproto.Registered{
		Accepted:        true,
		ServerTime:      h.nowMillis(),
		ProtocolVersion: wsproto.CurrentProtocolVersion,
	}
	ackedRev := int64(0)
	if h.policySrc != nil && wsproto.SupportsPolicy(reg.ProtocolVersion) {
		if p, ok := h.policySrc.PolicyFor(reg.WorkerID); ok {
			ack.Policy = &p
			ackedRev = p.Rev
		}
	}
	if err := wc.writeFrame(ctx, wsproto.TypeRegistered, "", ack); err != nil {
		return
	}
	if ack.Policy != nil {
		// Commit pending only AFTER the ack (carrying the policy) is on the wire (F-HIGH-2):
		// a failed ack returned above, so this never leaves a phantom pending.
		wc.markPolicyPending(ackedRev)
	}

	// 4) Only now publish the connection. A same-worker_id reconnect replaces the
	// prior connection (constraint #5): Put marks the old conn superseded — exempting
	// its in-flight jobs from the teardown failure — ONLY when this conn has the same
	// instance_id (same process reconnecting). A new instance_id means the worker
	// restarted, so the old conn is left un-superseded and gracefulClose's teardown
	// fails its now-dead jobs (z8ow).
	if old := h.reg.Put(wc); old != nil {
		old.gracefulClose("replaced by new registration")
	}
	slog.Info("hub accepted worker", "worker_id", reg.WorkerID, "remote", req.RemoteAddr,
		"hostname", reg.Hostname, "labels", reg.Labels, "max_concurrent", reg.MaxConcurrent,
		"proto", reg.ProtocolVersion, "os", reg.OS, "arch", reg.Arch,
		"gofer_version", reg.GoferVersion, "agent_caps", len(reg.AgentCaps))

	// 4b) Catch-up push (§7-N1). A PushPolicyAll that fired in the window between the
	// ack's PolicyFor and Put could not see this conn (not yet registered), so the worker
	// would sit forever on the rev the ack carried. Now that the conn is visible, re-read
	// the source and push once more if the rev advanced. Idempotent: the worker applies
	// only a rev newer than the one it last applied.
	h.catchUpPolicy(ctx, wc, ackedRev)

	// 5) Start the per-connection heartbeat sender, then run the single read loop
	// (review #2: never goroutine-per-frame). When the read loop returns the
	// connection is gone: stop the heartbeat goroutine, evict from the registry and
	// fail the in-flight jobs (unless superseded).
	h.startHeartbeat(ctx, wc)
	h.readLoop(ctx, wc)
	h.onDisconnect(wc)
	slog.Info("hub worker disconnected", "worker_id", reg.WorkerID)
}

// startHeartbeat launches the per-connection ping sender (P3, review #7). It
// sends an application-level ping{ts} every pingInterval; the worker refreshes
// its own read deadline on it and replies pong (the read loop then refreshes the
// hub's last_heartbeat). The goroutine stops on the per-conn done channel (closed
// when the read loop exits) or on ctx cancel. A write error is benign: the read
// loop's deadline is the authoritative disconnect detector.
func (h *Hub) startHeartbeat(ctx context.Context, wc *workerConn) {
	go func() {
		ticker := time.NewTicker(h.hb.PingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-wc.done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = wc.writeFrame(ctx, wsproto.TypePing, "", wsproto.Ping{TS: h.nowFn().Unix()})
			}
		}
	}()
}

// readLoop is the connection's ONLY read goroutine: it reads frames in order and
// demuxes them by job_id to the matching sink. Because there is exactly one
// reader and the sink calls are synchronous, frames for a given job are
// delivered strictly in arrival order — so a result is always observed after all
// preceding log frames for that job (review #2). It never spawns a goroutine per
// frame (which would break ordering and back-pressure).
//
// Half-open detection (review #7): every Read is bounded by a per-read deadline
// (readDeadline). A silently-dead TCP connection (no FIN) never delivers another
// frame, so the deadlined Read returns an error within the window and the loop
// exits → onDisconnect. Any inbound frame refreshes last_heartbeat (§6.5).
func (h *Hub) readLoop(ctx context.Context, wc *workerConn) {
	for {
		rctx, cancel := context.WithTimeout(ctx, h.hb.ReadDeadline)
		env, err := readEnvelope(rctx, wc.conn)
		cancel()
		if err != nil {
			return // disconnect / read-deadline / ctx done → caller runs onDisconnect
		}
		wc.lastHeartbeat.Store(h.nowFn().Unix())
		switch env.Type {
		case wsproto.TypeLog:
			lf, derr := wsproto.As[wsproto.Log](env)
			if derr != nil {
				continue
			}
			if sk := wc.sink(env.JobID); sk != nil {
				sk.WriteLog(lf.Stream, lf.Seq, lf.Text)
			}
		case wsproto.TypeStatus:
			// WP1: status is informational; result is authoritative. Recorded by
			// the read loop's ordering but not acted on here.
		case wsproto.TypeOutcome:
			// P4: the worker-captured产出, sent just before the result frame. Demux
			// to the job's sink IN ORDER on this single read loop so it is always
			// observed before Finish. An old worker never sends this frame (the host
			// job outcome stays empty); an unknown opcode would fall through harmlessly
			// anyway — both keep the回归红线 intact.
			of, derr := wsproto.As[wsproto.Outcome](env)
			if derr != nil {
				continue
			}
			if sk := wc.sink(env.JobID); sk != nil {
				sk.OnOutcome(of)
			}
		case wsproto.TypeResult:
			rf, derr := wsproto.As[wsproto.Result](env)
			if derr != nil {
				continue
			}
			// A terminal result frees the in-flight slot (§3.1) BEFORE the sink is
			// notified, so a worker that immediately re-dispatches is not falsely at
			// capacity. The sink (workerRunner) deregisters separately on Run exit.
			wc.release(env.JobID)
			if sk := wc.sink(env.JobID); sk != nil {
				sk.Finish(rf)
			}
		case wsproto.TypeInteraction:
			// P2: a worker-raised running-job interaction. Demux to the job's sink
			// IN ORDER on this single read loop (review #2) so the open can never be
			// reordered after the result frame. The sink bridges it onto the host job
			// (pending_interaction) and owns the async WaitAnswer goroutine.
			ifr, derr := wsproto.As[wsproto.Interaction](env)
			if derr != nil {
				continue
			}
			if sk := wc.sink(env.JobID); sk != nil {
				sk.OnInteraction(ifr.Action, ifr.Interaction)
			}
		case wsproto.TypeReloadResult:
			// P1 federation: the receipt for exactly one ReloadWorker call on this
			// connection. Apply the caps FIRST, resolve the waiter SECOND: the caller
			// unblocks into an HTTP response that may immediately re-read the worker's
			// capabilities, and it must never observe the pre-reload view of a reload it
			// was just told succeeded.
			rr, derr := wsproto.As[wsproto.ReloadResult](env)
			if derr != nil {
				continue
			}
			if rr.OK && rr.Caps != nil {
				h.reg.UpdateCaps(wc, *rr.Caps)
			}
			wc.resolveReload(rr) // unknown / already-gone request_id: dropped, never fatal
		case wsproto.TypeCaps:
			// P1 federation: an UNSOLICITED capability re-report (the worker reloaded on
			// its own, e.g. SIGHUP). There is no waiter to resolve — it must never be
			// mistaken for the answer to a pending reload (wsproto.Caps doc) — so it only
			// refreshes the hub's view of what this worker can now run.
			cf, derr := wsproto.As[wsproto.Caps](env)
			if derr != nil {
				continue
			}
			h.reg.UpdateCaps(wc, cf)
		case wsproto.TypeApplied:
			// P3 policy push: the worker's report of what it applied for a policy rev. Caps
			// is EMBEDDED and routes through the SAME reg.UpdateCaps path reload/caps use —
			// the hub keeps ONE capability source of truth, not a second channel. Rejected /
			// Degraded are diagnostic only (never gate routing). MarkPolicyApplied clears the
			// server-side pending Rev-monotonically and reuses UpdateCaps's superseded-conn guard.
			a, derr := wsproto.As[wsproto.Applied](env)
			if derr != nil {
				continue
			}
			if a.Caps != nil {
				h.reg.UpdateCaps(wc, *a.Caps)
			}
			h.reg.MarkPolicyApplied(wc, a.Rev, a.Rejected, a.Degraded)
		case wsproto.TypePing:
			// P3: the worker may send its own ping; reply pong{ts} (symmetric, §5.1).
			pf, _ := wsproto.As[wsproto.Ping](env)
			_ = wc.writeFrame(ctx, wsproto.TypePong, "", wsproto.Pong{TS: pf.TS})
		case wsproto.TypePong:
			// P3: reply to our ping. last_heartbeat already refreshed above (§6.5).
		}
	}
}

// onDisconnect runs when a connection's read loop has exited: it stops the
// heartbeat goroutine, evicts the connection from the registry and fails every
// in-flight server-side job (worker-lost MVP, §5.3) — UNLESS the connection was
// superseded by a same-worker_id, same-instance replacement (§5.5), in which case
// the same worker process has taken the jobs over and they must NOT be failed. A
// replacement by a DIFFERENT instance (worker restart) is left un-superseded by Put,
// so this path correctly fails the dead process's in-flight jobs (z8ow).
//
// Worker-lost is signalled through each in-flight job's sink (OnDisconnect),
// keeping the hub free of any runner/job import. The sink unblocks the
// workerRunner.Run wait with a worker-disconnected error → classify → StatusFailed.
func (h *Hub) onDisconnect(wc *workerConn) {
	wc.closeDone() // stop the heartbeat sender
	h.reg.Remove(wc.workerID, wc)

	if wc.superseded.Load() {
		// Replaced connection: the new conn owns these jobs now. Do NOT fail them.
		return
	}
	for _, jobID := range wc.inflightJobs() {
		wc.release(jobID)
		if sk := wc.sink(jobID); sk != nil {
			sk.OnDisconnect(errWorkerDisconnected)
		}
	}
}

// RegisterSink registers a per-job sink on the worker's connection. The
// workerRunner calls this BEFORE Dispatch so the first log frame is never lost
// (review #2). It errors when the worker is offline.
func (h *Hub) RegisterSink(workerID, jobID string, sk JobSink) error {
	wc, ok := h.reg.Get(workerID)
	if !ok {
		return ErrWorkerOffline
	}
	wc.putSink(jobID, sk)
	return nil
}

// DeregisterSink removes the per-job sink (workerRunner defers this on Run exit)
// and releases the in-flight slot it occupied.
func (h *Hub) DeregisterSink(workerID, jobID string) {
	if wc, ok := h.reg.Get(workerID); ok {
		wc.deleteSink(jobID)
		wc.release(jobID)
	}
}

// IsOnline reports whether a worker is currently registered (a live connection
// exists). Used for readiness checks / future /v1/runners observability (C6).
func (h *Hub) IsOnline(workerID string) bool {
	_, ok := h.reg.Get(workerID)
	return ok
}

// LastHeartbeat returns the unix-seconds timestamp of the most recent inbound
// frame for workerID (0 when offline). It seeds the C6/P4 /v1/runners surface.
func (h *Hub) LastHeartbeat(workerID string) int64 { return h.reg.LastHeartbeat(workerID) }

// WorkerSnapshot returns a read-only view of workerID's live state (ok=false when
// offline / never connected): last_heartbeat, in-flight job count and labels. It
// is the C6/P4 /v1/runners observability accessor; the handler reads it through a
// narrow interface so it never touches the internal conn.
func (h *Hub) WorkerSnapshot(workerID string) (WorkerSnapshot, bool) {
	return h.reg.WorkerSnapshot(workerID)
}

// LiveInstance returns the current live connection's process instance id for a
// worker. It is a narrow PTY relay seam: callers can bind a one-time nonce to a
// specific worker process without seeing the underlying connection.
func (h *Hub) LiveInstance(workerID string) (string, bool) {
	ws, ok := h.reg.WorkerSnapshot(workerID)
	if !ok || ws.InstanceID == "" {
		return "", false
	}
	return ws.InstanceID, true
}

// Dispatch sends a dispatch frame to the target worker. It errors when the
// worker is offline (ErrWorkerOffline) or already at its advertised
// max_concurrent (ErrWorkerAtCapacity, §5.4 — queueing is WP4). On success the
// job is recorded in the worker's in-flight set so a disconnect can fail it
// (§5.3) and so capacity accounting stays correct.
func (h *Hub) Dispatch(workerID string, d wsproto.Dispatch) error {
	wc, ok := h.reg.Get(workerID)
	if !ok {
		return ErrWorkerOffline
	}
	if !wc.tryReserve(d.JobID) {
		return ErrWorkerAtCapacity
	}
	if err := wc.writeFrame(context.Background(), wsproto.TypeDispatch, d.JobID, d); err != nil {
		wc.release(d.JobID) // dispatch write failed: free the slot we just reserved
		return err
	}
	return nil
}

// Answer sends an answer frame to the worker so its local job's interaction
// resumes (P2). It errors when the worker is offline (best-effort: the host
// interaction is already authoritative, so a lost answer never blocks the host).
func (h *Hub) Answer(workerID, jobID, interactionID, answer string) error {
	wc, ok := h.reg.Get(workerID)
	if !ok {
		return ErrWorkerOffline
	}
	return wc.writeFrame(context.Background(), wsproto.TypeAnswer, jobID, wsproto.Answer{
		JobID:         jobID,
		InteractionID: interactionID,
		Answer:        answer,
	})
}

// Cancel sends a cancel frame to the worker so it cancels the matching local job
// (P2). It errors when the worker is offline (best-effort: the host classifies
// the job from its own ctx regardless, so a lost cancel never strands the host).
func (h *Hub) Cancel(workerID, jobID string) error {
	wc, ok := h.reg.Get(workerID)
	if !ok {
		return ErrWorkerOffline
	}
	return wc.writeFrame(context.Background(), wsproto.TypeCancel, jobID, wsproto.Cancel{JobID: jobID})
}

// readEnvelope reads one JSON message and decodes it into a wsproto.Envelope.
func readEnvelope(ctx context.Context, c *websocket.Conn) (wsproto.Envelope, error) {
	var env wsproto.Envelope
	if err := wsjson.Read(ctx, c, &env); err != nil {
		return wsproto.Envelope{}, err
	}
	return env, nil
}

// writeEnvelope marshals a typed payload into an envelope and writes it (used on
// the Accept goroutine before the workerConn exists / for the registered ack).
func writeEnvelope(ctx context.Context, c *websocket.Conn, t wsproto.FrameType, jobID string, payload any) error {
	return wsjson.Write(ctx, c, wsproto.Envelope{Type: t, JobID: jobID, Payload: mustRaw(payload)})
}

// mustRaw marshals payload to json.RawMessage; on error it returns nil (the
// frame then carries an empty payload rather than panicking — As yields zero).
func mustRaw(payload any) json.RawMessage {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return b
}

package wshub

import (
	"context"
	"encoding/json"
	"errors"
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
}

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
		_ = conn.Close(websocket.StatusProtocolError, "expected register")
		return
	}
	reg, err := wsproto.As[wsproto.Register](env)
	if err != nil {
		_ = conn.Close(websocket.StatusProtocolError, "bad register payload")
		return
	}

	// 2) Token↔worker binding (review #1, mandatory): the register's worker_id
	// must match the worker the presented token is bound to (callerID).
	want, bound := h.bindings[reg.WorkerID]
	if !bound || want != callerID {
		_ = writeEnvelope(ctx, conn, wsproto.TypeRegistered, "", wsproto.Registered{
			Accepted:   false,
			Reason:     "worker_id not bound to this token",
			ServerTime: h.nowMillis(),
		})
		_ = conn.Close(websocket.StatusPolicyViolation, "binding")
		return
	}

	wc := newWorkerConn(reg.WorkerID, callerID, conn, reg)
	wc.lastHeartbeat.Store(h.nowFn().Unix())

	// 3) Same-worker_id reconnect replaces the prior connection (constraint #5).
	// Put marks the old conn superseded so its teardown does not fail the in-flight
	// jobs this new conn is taking over (§5.5).
	if old := h.reg.Put(wc); old != nil {
		old.gracefulClose("replaced by new registration")
	}

	// 4) Ack.
	if err := writeEnvelope(ctx, conn, wsproto.TypeRegistered, "", wsproto.Registered{
		Accepted:   true,
		ServerTime: h.nowMillis(),
	}); err != nil {
		h.reg.Remove(reg.WorkerID, wc)
		return
	}

	// 5) Start the per-connection heartbeat sender, then run the single read loop
	// (review #2: never goroutine-per-frame). When the read loop returns the
	// connection is gone: stop the heartbeat goroutine, evict from the registry and
	// fail the in-flight jobs (unless superseded).
	h.startHeartbeat(ctx, wc)
	h.readLoop(ctx, wc)
	h.onDisconnect(wc)
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
// superseded by a same-worker_id replacement (§5.5), in which case the new
// connection has taken the jobs over and they must NOT be failed.
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

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

// ErrWorkerOffline is returned by Dispatch/RegisterSink when no live connection
// exists for the target worker_id.
var ErrWorkerOffline = errors.New("worker offline")

// Hub is the serve-process singleton: WS accept + worker registry + per-job
// inbound-frame demux + dispatch. One instance is shared by every worker runner
// (assemble.buildCore). It is safe for concurrent use.
type Hub struct {
	reg *WorkerRegistry
	// bindings maps worker_id → the caller id its token authenticates as
	// (review #1). A register frame is accepted only when bindings[worker_id]
	// equals the connection's authenticated callerID. Built from
	// cfg.Server.Workers at assemble time.
	bindings map[string]string
	nowFn    func() time.Time
}

// New builds a Hub. bindings is the worker_id → expected caller-id map (from
// cfg.Server.Workers); a nil map means no worker may register (per-worker token
// is mandatory, §7 / review #1).
func New(bindings map[string]string) *Hub {
	if bindings == nil {
		bindings = map[string]string{}
	}
	return &Hub{reg: newRegistry(), bindings: bindings, nowFn: time.Now}
}

// nowMillis returns the current unix time in milliseconds (SR102 / Registered).
func (h *Hub) nowMillis() int64 { return h.nowFn().UnixNano() / int64(time.Millisecond) }

// Accept upgrades GET /v1/workers/connect to a WebSocket and runs the
// per-connection read loop. The route layer (httpapi) has already done Bearer
// auth and passes the resolved callerID; Accept does the worker_id↔caller
// binding check (review #1) then the register handshake.
//
// It MUST receive rux's c.Resp wrapped in wsUpgradeWriter (see upgrade_writer.go
// for the P0 finding) — never the raw c.Resp.
func (h *Hub) Accept(w http.ResponseWriter, req *http.Request, callerID string) {
	conn, err := websocket.Accept(&wsUpgradeWriter{rw: w}, req, &websocket.AcceptOptions{
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

	// 1) First frame must be register.
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

	wc := &workerConn{
		workerID: reg.WorkerID,
		callerID: callerID,
		conn:     conn,
		meta:     reg,
		sinks:    map[string]JobSink{},
	}

	// 3) Same-worker_id reconnect replaces the prior connection (constraint #5).
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

	// 5) Single per-connection read loop (review #2: never goroutine-per-frame).
	h.readLoop(ctx, wc)
	h.reg.Remove(reg.WorkerID, wc)
}

// readLoop is the connection's ONLY read goroutine: it reads frames in order and
// demuxes them by job_id to the matching sink. Because there is exactly one
// reader and the sink calls are synchronous, frames for a given job are
// delivered strictly in arrival order — so a result is always observed after all
// preceding log frames for that job (review #2). It never spawns a goroutine per
// frame (which would break ordering and back-pressure).
func (h *Hub) readLoop(ctx context.Context, wc *workerConn) {
	for {
		env, err := readEnvelope(ctx, wc.conn)
		if err != nil {
			return // disconnect / ctx done → caller removes from registry
		}
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
		case wsproto.TypeResult:
			rf, derr := wsproto.As[wsproto.Result](env)
			if derr != nil {
				continue
			}
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
		case wsproto.TypePong:
			// P3 placeholder: heartbeat.
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

// DeregisterSink removes the per-job sink (workerRunner defers this on Run exit).
func (h *Hub) DeregisterSink(workerID, jobID string) {
	if wc, ok := h.reg.Get(workerID); ok {
		wc.deleteSink(jobID)
	}
}

// IsOnline reports whether a worker is currently registered (a live connection
// exists). Used for readiness checks / future /v1/runners observability (C6).
func (h *Hub) IsOnline(workerID string) bool {
	_, ok := h.reg.Get(workerID)
	return ok
}

// Dispatch sends a dispatch frame to the target worker. It errors when the
// worker is offline.
func (h *Hub) Dispatch(workerID string, d wsproto.Dispatch) error {
	wc, ok := h.reg.Get(workerID)
	if !ok {
		return ErrWorkerOffline
	}
	return wc.writeFrame(context.Background(), wsproto.TypeDispatch, d.JobID, d)
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

// startHeartbeat is a P3 placeholder: ping/pong + read-deadline half-open
// detection. Declared so the protocol/hook surface is complete; no behaviour in
// WP1.
func (h *Hub) startHeartbeat(_ *workerConn) {}

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

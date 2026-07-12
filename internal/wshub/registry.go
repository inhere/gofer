package wshub

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/wsproto"
)

// workerConn is one live worker connection plus its register metadata and the
// set of per-job sinks the hub demuxes inbound frames to.
type workerConn struct {
	workerID string
	// instanceID is the worker's per-PROCESS nonce (wsproto.Register.InstanceID).
	// Put compares it to a re-registering conn's to tell a transient reconnect (same
	// instance → supersede, exempt in-flight jobs) from a restart (different instance
	// → fail the old process's in-flight jobs). Empty for legacy workers (z8ow).
	instanceID string
	callerID   string // authenticated identity (= the token-bound worker_id), review #1
	conn       *websocket.Conn
	meta       wsproto.Register

	// maxConcurrent is the worker's advertised per-worker concurrency cap (from the
	// register frame). 0 means "no hub-side cap" (the worker still has its own
	// per-project gate as a second line of defence, §5.4).
	maxConcurrent int

	// lastHeartbeat is the unix-seconds timestamp of the most recent inbound frame
	// on this connection (any frame refreshes it, not just pong — §6.5). Atomic so
	// the read loop can store while a future /v1/runners reader (C6/P4) loads it
	// without locking the registry. It seeds the C6 observability surface (P4).
	lastHeartbeat atomic.Int64

	// superseded marks a connection that was replaced by a same-worker_id
	// re-registration (§5.5). The replaced connection's read-loop teardown checks
	// this (via the registry) so it does NOT fail the in-flight jobs the new
	// connection has taken over. Atomic: set under the registry lock by Put, read
	// on the read-loop teardown path.
	superseded atomic.Bool

	// done is closed exactly once when the connection's read loop exits (disconnect
	// / read-deadline / replacement). Per-connection goroutines (the heartbeat
	// sender) select on it to stop. Guarded by doneOnce so a double close is safe.
	done     chan struct{}
	doneOnce sync.Once

	// writeMu serialises outbound writes: coder/websocket requires a single
	// concurrent writer per connection.
	writeMu sync.Mutex

	mu       sync.Mutex
	sinks    map[string]JobSink  // job_id → sink (review #2 lifecycle)
	inflight map[string]struct{} // job_id set of server-side dispatched jobs (§3.1)
}

// newWorkerConn builds a workerConn with its maps and done channel initialised.
func newWorkerConn(workerID, callerID string, conn *websocket.Conn, meta wsproto.Register) *workerConn {
	wc := &workerConn{
		workerID:      workerID,
		instanceID:    meta.InstanceID,
		callerID:      callerID,
		conn:          conn,
		meta:          meta,
		maxConcurrent: meta.MaxConcurrent,
		done:          make(chan struct{}),
		sinks:         map[string]JobSink{},
		inflight:      map[string]struct{}{},
	}
	return wc
}

// closeDone closes the per-connection done channel exactly once (stops the
// heartbeat goroutine). Safe to call multiple times.
func (wc *workerConn) closeDone() {
	wc.doneOnce.Do(func() { close(wc.done) })
}

// putSink registers a per-job sink (called before dispatch). registerSink is the
// hub-facing API; this is the per-conn primitive.
func (wc *workerConn) putSink(jobID string, sk JobSink) {
	wc.mu.Lock()
	wc.sinks[jobID] = sk
	wc.mu.Unlock()
}

func (wc *workerConn) deleteSink(jobID string) {
	wc.mu.Lock()
	delete(wc.sinks, jobID)
	wc.mu.Unlock()
}

// sink returns the sink for jobID, or nil when none is registered (a frame that
// arrives before the sink is registered, or after deregistration, is dropped —
// not a panic).
func (wc *workerConn) sink(jobID string) JobSink {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	return wc.sinks[jobID]
}

// tryReserve atomically admits jobID into the in-flight set when there is spare
// capacity (size < maxConcurrent), returning true. With maxConcurrent <= 0 there
// is no hub-side cap so it always admits. A capacity-full reservation returns
// false (the caller maps it to ErrWorkerAtCapacity, §5.4).
func (wc *workerConn) tryReserve(jobID string) bool {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	if _, dup := wc.inflight[jobID]; dup {
		return true // idempotent: an already-reserved job is fine
	}
	if wc.maxConcurrent > 0 && len(wc.inflight) >= wc.maxConcurrent {
		return false
	}
	wc.inflight[jobID] = struct{}{}
	return true
}

// release removes jobID from the in-flight set (on result / cancel / disconnect).
func (wc *workerConn) release(jobID string) {
	wc.mu.Lock()
	delete(wc.inflight, jobID)
	wc.mu.Unlock()
}

// inflightCount returns the number of in-flight server-side jobs on this
// connection (C6 observability — read under the per-conn lock for a consistent
// count without exposing the map).
func (wc *workerConn) inflightCount() int {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	return len(wc.inflight)
}

// inflightJobs returns a snapshot of the in-flight job ids (for the worker-lost
// broadcast on disconnect, §5.3).
func (wc *workerConn) inflightJobs() []string {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	out := make([]string, 0, len(wc.inflight))
	for id := range wc.inflight {
		out = append(out, id)
	}
	return out
}

// writeFrame marshals and sends one envelope under writeMu (single-writer).
func (wc *workerConn) writeFrame(ctx context.Context, t wsproto.FrameType, jobID string, payload any) error {
	wc.writeMu.Lock()
	defer wc.writeMu.Unlock()
	return wsjson.Write(ctx, wc.conn, wsproto.Envelope{
		Type:    t,
		JobID:   jobID,
		Payload: mustRaw(payload),
	})
}

// gracefulClose closes the connection with a normal-closure status and a reason.
// Used when a same-worker_id reconnect replaces an older connection (constraint
// #5) and on hub shutdown.
func (wc *workerConn) gracefulClose(reason string) {
	_ = wc.conn.Close(websocket.StatusNormalClosure, reason)
}

// WorkerRegistry maps worker_id → live connection, concurrency-safe.
type WorkerRegistry struct {
	mu    sync.RWMutex
	conns map[string]*workerConn
}

// newRegistry builds an empty registry.
func newRegistry() *WorkerRegistry {
	return &WorkerRegistry{conns: map[string]*workerConn{}}
}

// Put registers wc under its worker_id. If a connection already exists for that
// worker_id it is returned (old) so the caller can gracefully close it: a
// same-worker_id reconnect replaces the prior connection (constraint #5; the
// token binding already guarantees it is the same authenticated identity).
//
// The replaced connection is marked superseded — so its read-loop teardown does NOT
// fail its in-flight jobs (§5.5) — ONLY when the new conn carries the SAME
// instance_id, i.e. it is the same worker PROCESS re-establishing a dropped
// connection (a transient network reconnect): that process is still running those
// jobs, so failing them would be wrong. When the instance_id DIFFERS, a new process
// has taken over the worker_id (the old one restarted / was replaced); its in-flight
// jobs died with it, so the old conn is left un-superseded and its teardown fails
// them — the orphan-job fix (z8ow). Empty instance_id on BOTH (legacy workers)
// compares equal, preserving the original supersede-always behaviour.
func (r *WorkerRegistry) Put(wc *workerConn) (old *workerConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old = r.conns[wc.workerID]
	if old != nil && old.instanceID == wc.instanceID {
		old.superseded.Store(true)
	}
	r.conns[wc.workerID] = wc
	return old
}

// Get returns the live connection for workerID, if any.
func (r *WorkerRegistry) Get(workerID string) (*workerConn, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	wc, ok := r.conns[workerID]
	return wc, ok
}

// Remove deletes workerID's entry ONLY when the currently-registered connection
// is wc. This avoids a read-loop teardown for a replaced connection accidentally
// evicting the new connection that took its place.
func (r *WorkerRegistry) Remove(workerID string, wc *workerConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.conns[workerID]; ok && cur == wc {
		delete(r.conns, workerID)
	}
}

// All returns a snapshot of every live connection (used on hub shutdown to
// gracefully close them all).
func (r *WorkerRegistry) All() []*workerConn {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*workerConn, 0, len(r.conns))
	for _, wc := range r.conns {
		out = append(out, wc)
	}
	return out
}

// LastHeartbeat returns the unix-seconds timestamp of the most recent inbound
// frame for workerID (0 when offline / never seen). It seeds the C6/P4
// /v1/runners observability surface without exposing the internal conn.
func (r *WorkerRegistry) LastHeartbeat(workerID string) int64 {
	r.mu.RLock()
	wc, ok := r.conns[workerID]
	r.mu.RUnlock()
	if !ok {
		return 0
	}
	return wc.lastHeartbeat.Load()
}

// WorkerSnapshot is a read-only view of one worker's live state, taken under the
// registry lock. It seeds the C6/P4 /v1/runners observability surface without
// leaking the internal workerConn. Every slice is a defensive copy; an offline
// worker has no snapshot (WorkerSnapshot returns ok=false).
type WorkerSnapshot struct {
	WorkerID      string
	InstanceID    string
	LastHeartbeat int64 // unix seconds of the most recent inbound frame
	InFlight      int   // count of server-side dispatched jobs currently running
	PtyCapable    bool
	Labels        []string
	Projects      []string
	// Agents is the bare agent-key list (validation / selector, back-compat);
	// AgentCaps carries the typed detail (type/interactive) the UI cascade needs.
	// Both come straight from the worker's register frame (authoritative, P1).
	Agents    []string
	AgentCaps []wsproto.AgentBrief
	// Node info reported on register (P1) — surfaced for the P4 runners panel.
	OS           string
	Arch         string
	GoferVersion string
	StartedAt    int64 // worker process start, unix seconds
}

// WorkerSnapshot returns a point-in-time read-only view of workerID's live
// connection (ok=false when offline / never connected). It is the registry's C6
// observability accessor: the handler reads it through a narrow interface so it
// never touches the internal conn. Reads under the registry RLock; the in-flight
// count is taken under the per-conn lock so it is a consistent snapshot.
func (r *WorkerRegistry) WorkerSnapshot(workerID string) (WorkerSnapshot, bool) {
	r.mu.RLock()
	wc, ok := r.conns[workerID]
	r.mu.RUnlock()
	if !ok {
		return WorkerSnapshot{}, false
	}
	labels := append([]string(nil), wc.meta.Labels...)
	projects := append([]string(nil), wc.meta.Projects...)
	agents := append([]string(nil), wc.meta.Agents...)
	agentCaps := append([]wsproto.AgentBrief(nil), wc.meta.AgentCaps...)
	return WorkerSnapshot{
		WorkerID:      wc.workerID,
		InstanceID:    wc.meta.InstanceID,
		LastHeartbeat: wc.lastHeartbeat.Load(),
		InFlight:      wc.inflightCount(),
		PtyCapable:    wc.meta.PtyCapable,
		Labels:        labels,
		Projects:      projects,
		Agents:        agents,
		AgentCaps:     agentCaps,
		OS:            wc.meta.OS,
		Arch:          wc.meta.Arch,
		GoferVersion:  wc.meta.GoferVersion,
		StartedAt:     wc.meta.StartedAt,
	}, true
}

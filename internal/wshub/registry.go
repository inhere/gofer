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
	// remoteAddr is the connection's remote address as the hub saw it at accept
	// (req.RemoteAddr). Display-only observability: behind NAT/docker bridges it
	// may not be the worker machine's real address — meta.Hostname is the
	// machine-identifying field. Set once before Put, never mutated after.
	remoteAddr string

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

	// mu guards every MUTABLE per-connection field: the sink/in-flight maps, the
	// pending-reload map, and — since config hot-reload (P1) — meta and
	// maxConcurrent, which a caps re-report rewrites on a live connection. It is the
	// INNER lock: a path may take the registry lock and then mu, never the reverse
	// (see WorkerRegistry.UpdateCaps).
	mu       sync.Mutex
	sinks    map[string]JobSink  // job_id → sink (review #2 lifecycle)
	inflight map[string]struct{} // job_id set of server-side dispatched jobs (§3.1)

	// pending maps a reload request_id → the 1-buffered channel its (synchronous)
	// caller is parked on. The read loop resolves it when the matching ReloadResult
	// arrives; the caller always removes its own entry, so a late answer finds
	// nothing and is dropped. See reload.go.
	pending map[string]chan wsproto.ReloadResult

	// Policy-push diagnostic state (P3 T4), all guarded by wc.mu (the lock
	// WorkerSnapshot reads them under). policyRev is the HIGHEST rev the hub has
	// pushed to THIS connection (ack / catch-up / broadcast, max-monotonic);
	// policyPending is true while the worker has not yet reported an Applied at or
	// above policyRev; appliedRev is the highest rev the worker acknowledged applying.
	// Both transitions are Rev-monotonic so a late frame can neither lower the pushed
	// rev nor clear a pending set for a newer one. rejected/degraded are the last
	// Applied's diagnostics (surfaced by P4; never gate routing).
	policyRev      int64
	appliedRev     int64
	policyPending  bool
	policyRejected []wsproto.AppliedRejection
	policyDegraded []wsproto.AppliedDegrade
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
		pending:       map[string]chan wsproto.ReloadResult{},
	}
	return wc
}

// protocolVersion is the wire version the peer reported on register. It is a
// per-CONNECTION fact, not a hub-wide one: a fleet mid-upgrade holds connections of
// several versions at once, and each is negotiated against its own report.
//
// Lock-free on purpose: the protocol version is an IMMUTABLE property of the
// connection (a worker cannot change the wire version it speaks without
// reconnecting), so UpdateCaps must never write it — only the config-derived
// capability fields.
func (wc *workerConn) protocolVersion() int { return wc.meta.ProtocolVersion }

// supportsReload reports whether THIS connection's worker implements the config
// hot-reload frames. It is the only reload capability check in the hub: callers ask
// it instead of comparing protocol numbers, so the version that gained the feature
// lives in exactly one place (wsproto.ReloadMinProtocolVersion). A worker below it
// is registered and fully usable — it just cannot be asked to reload, and the caller
// must say so explicitly rather than send a frame the peer will silently drop.
func (wc *workerConn) supportsReload() bool {
	return wsproto.SupportsReload(wc.protocolVersion())
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

// markPolicyPending records that the hub has pushed policy rev to this connection and
// the worker has not yet reported it applied. It is Rev-monotonic (max): the three push
// paths (ack / catch-up / PushPolicyAll) may target the same conn concurrently, and a
// naive `if rev>cur { cur=rev }` off-lock could lose an update or let a lower rev lower
// the pushed value. Callers commit this only AFTER the frame is on the wire, so a failed
// write never leaves a phantom pending (F-HIGH-2). Only a policy-capable worker ever
// reaches here (all callers gate on SupportsPolicy), so a v3 worker is never pending.
func (wc *workerConn) markPolicyPending(rev int64) {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	if rev > wc.policyRev {
		wc.policyRev = rev
		wc.policyPending = true
	}
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
	Hostname     string // self-reported machine hostname (register node info)
	RemoteAddr   string // conn's remote addr as seen at accept (may be NAT/bridge)
	GoferVersion string
	StartedAt    int64 // worker process start, unix seconds
	// ProtocolVersion is the wire version this connection registered with
	// (wc.meta.ProtocolVersion, immutable per-connection — see protocolVersion()).
	// Surfaced so an operator can spot a too-old worker (reload/policy gated by
	// wsproto.SupportsReload/SupportsPolicy) before a reload/policy push 409s.
	ProtocolVersion int
	// Policy-push diagnostic state (P3 T4). PolicyPending is true while the worker has
	// negotiated policy support and the hub pushed a rev it has not yet reported applied;
	// a pre-policy (v3) worker is never marked pending. PolicyRev is the highest rev
	// pushed to it, AppliedRev the highest it reported applying.
	PolicyPending bool
	PolicyRev     int64
	AppliedRev    int64
}

// WorkerSnapshot returns a point-in-time read-only view of workerID's live
// connection (ok=false when offline / never connected). It is the registry's C6
// observability accessor: the handler reads it through a narrow interface so it
// never touches the internal conn.
//
// The conn is looked up under the registry RLock, but everything it reads OUT of the
// conn is read under the per-conn lock (snapshot): since config hot-reload (P1) the
// capability fields are mutable on a live connection, so reading meta outside wc.mu
// would be a data race against UpdateCaps. The in-flight count comes from the same
// critical section, so a snapshot is internally consistent.
func (r *WorkerRegistry) WorkerSnapshot(workerID string) (WorkerSnapshot, bool) {
	r.mu.RLock()
	wc, ok := r.conns[workerID]
	r.mu.RUnlock()
	if !ok {
		return WorkerSnapshot{}, false
	}
	return wc.snapshot(), true
}

// snapshot reads the connection's whole observable state under the per-conn lock.
// Every slice is a defensive copy so a consumer mutating its snapshot can never
// corrupt the live registry's capability view.
func (wc *workerConn) snapshot() WorkerSnapshot {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	return WorkerSnapshot{
		WorkerID:        wc.workerID,
		InstanceID:      wc.meta.InstanceID,
		LastHeartbeat:   wc.lastHeartbeat.Load(),
		InFlight:        len(wc.inflight),
		PtyCapable:      wc.meta.PtyCapable,
		Labels:          append([]string(nil), wc.meta.Labels...),
		Projects:        append([]string(nil), wc.meta.Projects...),
		Agents:          append([]string(nil), wc.meta.Agents...),
		AgentCaps:       append([]wsproto.AgentBrief(nil), wc.meta.AgentCaps...),
		OS:              wc.meta.OS,
		Arch:            wc.meta.Arch,
		Hostname:        wc.meta.Hostname,
		RemoteAddr:      wc.remoteAddr,
		GoferVersion:    wc.meta.GoferVersion,
		StartedAt:       wc.meta.StartedAt,
		ProtocolVersion: wc.meta.ProtocolVersion,
		PolicyPending:   wc.policyPending,
		PolicyRev:       wc.policyRev,
		AppliedRev:      wc.appliedRev,
	}
}

// current returns the connection currently registered for workerID (nil when the
// worker is offline). Caller must hold no lock; it takes the registry RLock.
func (r *WorkerRegistry) current(workerID string) *workerConn {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.conns[workerID]
}

// UpdateCaps applies a worker's re-reported capabilities (a successful reload's
// ReloadResult.Caps, or an unsolicited SIGHUP-driven Caps frame) to ONE specific
// connection. Three things it must get right, none of them optional:
//
//  1. It takes a *workerConn, not a worker_id. A worker that reconnects gets a NEW
//     conn under the same id, while the OLD conn's read loop may still be draining
//     frames — a caps frame from the dead process must not overwrite the live one's
//     capabilities. So the update is dropped unless this conn is still the registered
//     one (currentLocked check below).
//  2. It writes meta under wc.mu, the same lock WorkerSnapshot reads it under. meta
//     used to be immutable-after-register (published through the registry lock), which
//     is exactly why the old lock-free read was safe and no longer is.
//  3. It writes BOTH concurrency fields. wc.meta.MaxConcurrent is the displayed value;
//     wc.maxConcurrent is the one tryReserve actually admits against. Updating only the
//     first would show a new limit while enforcing the old one.
//
// Caps is a FULL SNAPSHOT, not a patch: an empty Projects list means "this worker now
// serves no project", and is applied as such. MaxConc is NOT an exception to that rule
// — wsproto.Caps has no omitempty (see its doc) and every producer of a Caps value
// (workerCaps, the register-time seed) fills MaxConc unconditionally from the worker's
// resolved config, so a decoded 0 IS the worker's current "no cap" setting, not an
// absent field. Gating on c.MaxConc > 0 here would silently keep the OLD limit on a
// reload that intentionally uncaps the worker (max_concurrent: 0 in worker.yaml) —
// that was tried once and reverted (tools-49r); 0 is applied like any other value.
func (r *WorkerRegistry) UpdateCaps(wc *workerConn, c wsproto.Caps) {
	if wc == nil {
		return
	}
	// Lock order: registry lock (outer) → wc.mu (inner). Holding the registry RLock
	// across the mutation closes the check-then-act window against a concurrent Put,
	// and is deadlock-free because no path ever takes wc.mu and then a registry lock
	// (WorkerSnapshot/Dispatch release the registry lock before touching the conn).
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.conns[wc.workerID] != wc {
		return // superseded by a newer connection: a late caps frame from the old one is stale
	}

	wc.mu.Lock()
	defer wc.mu.Unlock()
	wc.meta.Labels = c.Labels
	wc.meta.Projects = c.Projects
	wc.meta.Agents = c.Agents
	wc.meta.AgentCaps = c.AgentCaps
	wc.meta.MaxConcurrent = c.MaxConc
	wc.maxConcurrent = c.MaxConc // the field tryReserve admits against
}

// MarkPolicyApplied records a worker's Applied report for one policy rev on ONE
// connection. It shares UpdateCaps's two invariants, on purpose (T4-D):
//
//  1. It takes a *workerConn and drops the update unless this conn is still the
//     registered one — a late Applied from a superseded/old process must not touch the
//     live conn's diagnostic state (the same r.conns[id] != wc guard UpdateCaps uses).
//  2. It writes under wc.mu, the lock WorkerSnapshot reads pending/rev under.
//
// Clearing pending is Rev-MONOTONIC: only an Applied at or above the rev the hub is
// waiting on (wc.policyRev) clears pending and advances appliedRev. A stale Applied
// (rev < policyRev — the worker's report for an older rev arriving after the hub already
// pushed a newer one) is IGNORED and never rolls state back; rolling back would report
// an un-converged worker as converged. This pending is the SERVER-side diagnostic — a
// separate rev ladder from the worker executor's own applied lastRev.
func (r *WorkerRegistry) MarkPolicyApplied(wc *workerConn, rev int64, rejected []wsproto.AppliedRejection, degraded []wsproto.AppliedDegrade) {
	if wc == nil {
		return
	}
	// Lock order identical to UpdateCaps: registry RLock (outer) → wc.mu (inner).
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.conns[wc.workerID] != wc {
		return // superseded by a newer connection: a late Applied from the old one is stale
	}

	wc.mu.Lock()
	defer wc.mu.Unlock()
	if rev < wc.policyRev {
		return // stale Applied for an older rev: ignore, do not clear a pending set for a newer rev
	}
	wc.appliedRev = rev
	wc.policyPending = false
	wc.policyRejected = rejected
	wc.policyDegraded = degraded
}

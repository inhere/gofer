package wshub

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/inhere/gofer/internal/wsproto"
)

// errWorkerLost is the error an in-flight job is finished with when its worker's
// RECOV-01 recovery window expired: the connection dropped AND the same worker
// process did not come back (or did not prove it still tracks the job) in time.
// It is deliberately distinct from errWorkerDisconnected (recovery turned off /
// pre-RECOV-01 behaviour) so a post-mortem can tell "we waited and gave up" from
// "we never waited". Its text flows verbatim into jobs.error; the structured marker
// for log search is the "error_code=worker_lost" attribute on the warning below.
var errWorkerLost = errors.New("worker lost: recovery window expired")

// pendingCancelCap bounds the per-worker pending-cancel map (mirrors the worker's
// own pendingCancelCap). In practice the map only ever holds jobs that ARE in the
// recovery set, so the cap is a formality rather than a real bound.
const pendingCancelCap = 256

// recoveringJob is one job of a recoverySet: the host-side sink to notify and the
// server-side log offsets recorded when the job was suspended. The offsets are
// frozen for as long as the job is recovering (nothing can write to it while its
// connection is gone), so they are exactly what the resume ack must carry.
type recoveringJob struct {
	sink                 JobSink
	stdoutOff, stderrOff int64
}

// recoverySet holds the jobs of ONE worker that are being held in `recovering`
// after its connection dropped (RECOV-01), plus the per-worker timer bounding how
// long they may stay there. It is guarded by Hub.recMu.
type recoverySet struct {
	workerID   string
	instanceID string
	// jobs are the jobs still in `recovering`: a job leaves the set when it is
	// RESUMED (the worker proved it still runs it) or when the window expires
	// (worker lost). A job the worker still has but already finished stays here
	// until its replayed Result arrives (any frame for it proves the worker).
	jobs map[string]*recoveringJob
	// cancels are the host-side cancels that arrived while the worker was offline,
	// keyed by job id. They are delivered when that job is resumed, so a cancel
	// issued during the outage is not silently lost (the host already recorded the
	// job as cancelled; without this the worker would keep running it).
	cancels map[string]struct{}
	timer   *time.Timer
}

// SetRecoverWindow sets the RECOV-01 reconnect window (serve resolves it from
// server.job_recover_window_sec; 0 disables recovery, restoring the pre-RECOV-01
// behaviour of failing a worker's in-flight jobs the moment its connection dies).
// Must be called before Accept handles any connection (assemble time).
func (h *Hub) SetRecoverWindow(d time.Duration) {
	if d < 0 {
		d = 0
	}
	h.recoverWindow = d
}

// pendingRecovery is the decision taken for a registering worker's recovering jobs
// (RECOV-01). It is computed BEFORE the ack is written (the resume entries ride ON
// the ack, so the worker can rewind its log offsets before it resumes streaming)
// and applied AFTER the connection is in the registry (attaching a sink needs the
// live connection to be the registered one).
type pendingRecovery struct {
	// resumed are jobs the worker reported as still in flight and non-terminal: the
	// host job goes back to `running` as soon as the resume ack is on the wire. They
	// have been taken OUT of the recovery set (they are no longer `recovering`).
	resumed map[string]*recoveringJob
	// waiting are jobs that stay `recovering` under the window but whose sink must
	// follow the new connection: the worker still tracks them but reported them
	// terminal (its replayed Result will finish them), or the worker is too old to
	// report `inflight` at all (then the first frame for the job proves it). They
	// remain IN the recovery set until a frame arrives (markLive) or the window
	// expires.
	waiting map[string]*recoveringJob
	// cancels are the host-side cancels recorded while the worker was offline for the
	// RESUMED jobs. They are carried here (taken out of the recovery set, which the
	// plan may empty and drop) so the delivery does not depend on the set surviving
	// the plan; restoreRecovery puts them back if the ack never lands.
	cancels map[string]struct{}
}

// lostJob is a recovering job that must be failed at once, with the reason to log.
// It is collected during the plan and notified after recMu is released.
type lostJob struct {
	jobID  string
	rj     *recoveringJob
	reason string
}

// resumeEntries renders the ack's resume list in a deterministic (job-id sorted)
// order, carrying the server-side offsets the worker must rewind to.
func (p pendingRecovery) resumeEntries() []wsproto.ResumeJob {
	if len(p.resumed) == 0 {
		return nil
	}
	ids := make([]string, 0, len(p.resumed))
	for id := range p.resumed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]wsproto.ResumeJob, 0, len(ids))
	for _, id := range ids {
		rj := p.resumed[id]
		out = append(out, wsproto.ResumeJob{JobID: id, StdoutOff: rj.stdoutOff, StderrOff: rj.stderrOff})
	}
	return out
}

// suspendOnDisconnect puts every in-flight job of the dropped connection into
// `recovering` (RECOV-01) and arms the per-worker window, instead of failing them.
// Called by onDisconnect for a NON-superseded connection while a window is set.
//
// The sink calls happen OUTSIDE recMu: Suspend flips the host job's status and
// persists it (a DB write), so holding the hub's recovery lock across it would
// serialise every other worker's recovery decision behind one job's write.
func (h *Hub) suspendOnDisconnect(wc *workerConn, jobIDs []string) {
	type held struct {
		id  string
		sk  JobSink
		off [2]int64
	}
	heldJobs := make([]held, 0, len(jobIDs))
	for _, jobID := range jobIDs {
		sk := wc.sink(jobID)
		wc.release(jobID) // the in-flight slot dies with the connection
		heldJobs = append(heldJobs, held{id: jobID, sk: sk})
	}
	// Publish the jobs as recovering BEFORE notifying the sinks, so a host cancel
	// that races this disconnect already finds them recorded.
	h.recMu.Lock()
	rs := h.recov[wc.workerID]
	if rs == nil {
		rs = &recoverySet{
			workerID:   wc.workerID,
			instanceID: wc.instanceID,
			jobs:       map[string]*recoveringJob{},
			cancels:    map[string]struct{}{},
		}
		h.recov[wc.workerID] = rs
	}
	for i := range heldJobs {
		rs.jobs[heldJobs[i].id] = &recoveringJob{sink: heldJobs[i].sk}
	}
	h.recMu.Unlock()

	for i := range heldJobs {
		if heldJobs[i].sk != nil {
			heldJobs[i].off[0], heldJobs[i].off[1] = heldJobs[i].sk.Suspend("worker disconnected")
		}
		h.recMu.Lock()
		if rs := h.recov[wc.workerID]; rs != nil {
			if rj := rs.jobs[heldJobs[i].id]; rj != nil {
				rj.stdoutOff, rj.stderrOff = heldJobs[i].off[0], heldJobs[i].off[1]
			}
		}
		h.recMu.Unlock()
		slog.Info("worker.job_recovering", "event", "worker.job_recovering", "component", "server",
			"worker_id", wc.workerID, "job_id", heldJobs[i].id,
			"window_sec", int(h.recoverWindow.Seconds()))
	}
	h.attachToLiveReconnect(wc, jobIDs)
	h.armRecoverTimer(wc.workerID)
}

// attachToLiveReconnect handles the teardown/reconnect race: the SAME worker process
// may already have a live connection by the time this torn-down connection's jobs are
// suspended (its register ran while this teardown was still in flight, so it could not
// see these jobs and planned nothing for them). Attaching the sinks to that live
// connection is the only way the frames the still-running worker keeps sending reach
// their sink; the first frame for a job then proves it (markLive) and the window still
// bounds a job that never shows one. A connection of a DIFFERENT instance is not
// touched: its register already failed these jobs.
func (h *Hub) attachToLiveReconnect(wc *workerConn, jobIDs []string) {
	live, ok := h.reg.Get(wc.workerID)
	if !ok || live == wc || live.instanceID != wc.instanceID {
		return
	}
	for _, jobID := range jobIDs {
		h.recMu.Lock()
		rs := h.recov[wc.workerID]
		var rj *recoveringJob
		if rs != nil {
			rj = rs.jobs[jobID]
		}
		h.recMu.Unlock()
		if rj == nil || rj.sink == nil {
			continue
		}
		live.putSink(jobID, rj.sink)
		live.adoptReserve(jobID)
	}
}

// armRecoverTimer (re)arms the per-worker recovery window: after it elapses, every
// job still in `recovering` for that worker is failed through its sink's
// OnDisconnect (errWorkerLost) — the worker never came back, or never proved it
// still tracks the job. It is a no-op when nothing is left to recover.
//
// The window is measured from the DISCONNECT, never extended by a reconnect: a
// worker that keeps re-registering without proving anything does not push the
// deadline out.
func (h *Hub) armRecoverTimer(workerID string) {
	h.recMu.Lock()
	defer h.recMu.Unlock()
	rs := h.recov[workerID]
	if rs == nil {
		return
	}
	if len(rs.jobs) == 0 {
		h.dropRecoveryLocked(workerID, rs)
		return
	}
	if rs.timer != nil {
		rs.timer.Stop()
	}
	rs.timer = time.AfterFunc(h.recoverWindow, func() { h.expireRecovery(workerID) })
}

// dropRecoveryLocked stops the worker's recovery timer and forgets its set.
func (h *Hub) dropRecoveryLocked(workerID string, rs *recoverySet) {
	if rs.timer != nil {
		rs.timer.Stop()
		rs.timer = nil
	}
	delete(h.recov, workerID)
}

// expireRecovery fails every job still recovering for workerID with errWorkerLost.
// It runs on the timer goroutine, so it takes the set out of the map FIRST (which
// makes it idempotent against a concurrent reconnect) and notifies the sinks after
// releasing the lock.
func (h *Hub) expireRecovery(workerID string) {
	h.recMu.Lock()
	rs := h.recov[workerID]
	if rs == nil {
		h.recMu.Unlock()
		return
	}
	h.dropRecoveryLocked(workerID, rs)
	jobs := rs.jobs
	h.recMu.Unlock()

	for jobID, rj := range jobs {
		slog.Warn("worker.job_lost", "event", "worker.job_lost", "component", "server",
			"worker_id", workerID, "job_id", jobID, "error_code", "worker_lost",
			"reason", "recovery window expired", "window_sec", int(h.recoverWindow.Seconds()))
		if rj != nil && rj.sink != nil {
			rj.sink.OnDisconnect(errWorkerLost)
		}
	}
}

// planRecovery evaluates a registering worker against the jobs the hub holds in
// `recovering` for it and returns what the caller must apply after the ack. The
// three cases are the design's:
//
//   - a DIFFERENT instance_id → the worker process restarted: nothing it ran
//     survived, so every recovering job is failed at once (worker lost);
//   - the same instance with `inflight` reported (a RECOV-01 worker) → per job:
//     still in flight and non-terminal ⇒ resume (+ ack offsets); still in flight
//     but terminal ⇒ wait for the replayed Result; absent ⇒ the worker no longer
//     tracks it, fail it at once ("worker no longer tracks job");
//   - the same instance with a nil `inflight` (a pre-RECOV-01 worker) → it cannot
//     prove anything, so nothing is resumed NOW: the jobs stay recovering under the
//     window and the first frame for one of them proves the worker (markLive).
func (h *Hub) planRecovery(reg wsproto.Register) pendingRecovery {
	plan := pendingRecovery{
		resumed: map[string]*recoveringJob{},
		waiting: map[string]*recoveringJob{},
		cancels: map[string]struct{}{},
	}

	h.recMu.Lock()
	rs := h.recov[reg.WorkerID]
	if rs == nil || len(rs.jobs) == 0 {
		if rs != nil {
			h.dropRecoveryLocked(reg.WorkerID, rs)
		}
		h.recMu.Unlock()
		return plan
	}
	if rs.instanceID != reg.InstanceID {
		// A new process took over this worker_id: the old process and the jobs it was
		// running are gone. Fail them now — waiting would only delay a foregone
		// conclusion (and a restarted worker never re-sends their results).
		lost := rs.jobs
		h.dropRecoveryLocked(reg.WorkerID, rs)
		h.recMu.Unlock()
		for jobID, rj := range lost {
			slog.Warn("worker.job_lost", "event", "worker.job_lost", "component", "server",
				"worker_id", reg.WorkerID, "job_id", jobID, "error_code", "worker_lost",
				"reason", "worker restarted (new instance)")
			if rj != nil && rj.sink != nil {
				rj.sink.OnDisconnect(errWorkerLost)
			}
		}
		return plan
	}

	if reg.Inflight == nil {
		// Pre-RECOV-01 worker: it cannot confirm what it still runs. Keep every job
		// recovering (the window stays armed) but hand the sinks to the caller so
		// they are attached to the new connection — the first frame for a job then
		// proves it (markLive) without waiting out the whole window.
		for jobID, rj := range rs.jobs {
			plan.waiting[jobID] = rj
		}
		h.recMu.Unlock()
		return plan
	}

	inflight := make(map[string]wsproto.InflightJob, len(reg.Inflight))
	for _, f := range reg.Inflight {
		inflight[f.JobID] = f
	}
	var lost []lostJob
	for jobID, rj := range rs.jobs {
		f, ok := inflight[jobID]
		switch {
		case !ok:
			delete(rs.jobs, jobID)
			lost = append(lost, lostJob{jobID: jobID, rj: rj, reason: "worker no longer tracks job"})
		case isTerminalWireStatus(f.Status):
			plan.waiting[jobID] = rj // keep recovering: wait for the replayed Result
		default:
			plan.resumed[jobID] = rj
			delete(rs.jobs, jobID)
			// A cancel issued during the outage travels with the job it cancels: take
			// it out of the set (which may be dropped below) so it is delivered by
			// applyRecovery rather than lost with the set.
			if _, ok := rs.cancels[jobID]; ok {
				plan.cancels[jobID] = struct{}{}
				delete(rs.cancels, jobID)
			}
		}
	}
	if len(rs.jobs) == 0 {
		h.dropRecoveryLocked(reg.WorkerID, rs)
	}
	h.recMu.Unlock()

	for _, l := range lost {
		slog.Warn("worker.job_lost", "event", "worker.job_lost", "component", "server",
			"worker_id", reg.WorkerID, "job_id", l.jobID, "error_code", "worker_lost", "reason", l.reason)
		if l.rj != nil && l.rj.sink != nil {
			l.rj.sink.OnDisconnect(errWorkerLost)
		}
	}
	return plan
}

// applyRecovery moves the planned sinks onto the now-registered connection, resumes
// the confirmed jobs and delivers any cancel that was recorded while the worker was
// offline. It runs on the Accept goroutine, after Put, before the read loop starts.
func (h *Hub) applyRecovery(wc *workerConn, plan pendingRecovery) {
	attach := func(jobs map[string]*recoveringJob) {
		for jobID, rj := range jobs {
			if rj != nil && rj.sink != nil {
				wc.putSink(jobID, rj.sink)
			}
			// adoptReserve (not tryReserve): the job was already admitted when it was
			// first dispatched and is still running on the worker, so re-admitting it
			// must not be refused by a capacity check.
			wc.adoptReserve(jobID)
		}
	}
	attach(plan.resumed)
	attach(plan.waiting)
	for jobID, rj := range plan.resumed {
		if rj != nil && rj.sink != nil {
			rj.sink.Resume()
		}
		slog.Info("worker.job_resumed", "event", "worker.job_resumed", "component", "server",
			"worker_id", wc.workerID, "job_id", jobID)
		if _, cancelled := plan.cancels[jobID]; cancelled {
			h.deliverCancel(wc, jobID)
		}
	}
}

// deliverCancel sends a Cancel frame for a job the host cancelled while the worker
// was offline. Best-effort: the host job is already terminal on its own account, so a
// write error is only logged (the worker's own timeout remains as the backstop).
func (h *Hub) deliverCancel(wc *workerConn, jobID string) {
	if err := wc.writeFrame(context.Background(), wsproto.TypeCancel, jobID, wsproto.Cancel{JobID: jobID}); err != nil {
		slog.Warn("hub could not deliver a cancel recorded during recovery",
			"worker_id", wc.workerID, "job_id", jobID, "err", err)
		return
	}
	slog.Info("worker.job_cancel_delivered", "event", "worker.job_cancel_delivered", "component", "server",
		"worker_id", wc.workerID, "job_id", jobID)
}

// restoreRecovery puts the planned jobs back into the recovering set: the ack never
// reached the worker (its write failed), so the registration did not happen and the
// jobs must keep waiting under a freshly armed window rather than disappear. The
// adopting jobs of a previous serve process (RECOV-01 R4) go in with the REGISTERING
// instance id: their store rows are still `recovering`, so the next successful
// register must be able to reconcile them against the same process.
func (h *Hub) restoreRecovery(workerID, instanceID string, plan pendingRecovery) {
	if len(plan.resumed) == 0 && len(plan.waiting) == 0 {
		return
	}
	h.recMu.Lock()
	rs := h.recov[workerID]
	if rs == nil {
		rs = &recoverySet{workerID: workerID, instanceID: instanceID, jobs: map[string]*recoveringJob{}, cancels: map[string]struct{}{}}
		h.recov[workerID] = rs
	}
	for id, rj := range plan.resumed {
		rs.jobs[id] = rj
	}
	for id, rj := range plan.waiting {
		rs.jobs[id] = rj
	}
	for id := range plan.cancels {
		rs.cancels[id] = struct{}{}
	}
	h.recMu.Unlock()
	h.armRecoverTimer(workerID)
}

// markLive implements the "any frame proves the worker still has this job" rule:
// the first frame addressed to a recovering job resumes it. It covers the
// pre-RECOV-01 worker (which cannot report `inflight`) and the job the worker
// reported as terminal (its first replayed frame — the Outcome, then the Result —
// arrives before the Result itself). It is a cheap no-op for every job that is not
// recovering (the common case), so it can sit on the hub's single read loop.
func (h *Hub) markLive(wc *workerConn, jobID string) {
	if jobID == "" {
		return
	}
	h.recMu.Lock()
	rs := h.recov[wc.workerID]
	if rs == nil {
		h.recMu.Unlock()
		return
	}
	rj, ok := rs.jobs[jobID]
	if !ok {
		h.recMu.Unlock()
		return
	}
	delete(rs.jobs, jobID)
	if len(rs.jobs) == 0 {
		h.dropRecoveryLocked(wc.workerID, rs)
	}
	h.recMu.Unlock()

	slog.Info("worker.job_resumed", "event", "worker.job_resumed", "component", "server",
		"worker_id", wc.workerID, "job_id", jobID, "reason", "frame received within recovery window")
	if rj != nil && rj.sink != nil {
		rj.sink.Resume()
	}
	// A cancel recorded while the worker was offline is delivered now that the job is
	// proven live (the worker must tear down a job the host already finished).
	h.recMu.Lock()
	cancelled := false
	if cur := h.recov[wc.workerID]; cur != nil {
		if _, ok := cur.cancels[jobID]; ok {
			delete(cur.cancels, jobID)
			cancelled = true
		}
	}
	h.recMu.Unlock()
	if cancelled {
		h.deliverCancel(wc, jobID)
	}
}

// recordPendingCancel notes that a cancel for jobID arrived while the worker was
// offline. It only records when the job IS recovering for that worker (otherwise
// there is nobody to deliver it to, and the map must not grow for arbitrary ids).
// Returns true when it recorded.
func (h *Hub) recordPendingCancel(workerID, jobID string) bool {
	if jobID == "" {
		return false
	}
	h.recMu.Lock()
	defer h.recMu.Unlock()
	rs := h.recov[workerID]
	if rs == nil {
		return false
	}
	if _, ok := rs.jobs[jobID]; !ok {
		return false
	}
	if len(rs.cancels) >= pendingCancelCap {
		for id := range rs.cancels {
			delete(rs.cancels, id)
			break
		}
	}
	rs.cancels[jobID] = struct{}{}
	return true
}

// isTerminalWireStatus reports whether a worker-reported job status is terminal. It
// mirrors job.IsTerminal over the WIRE vocabulary: the hub must not import the job
// package (verification 17: wshub depends only on wsproto), so the set is pinned
// here. "recovering" is NOT terminal (the worker holds it for the same reason the
// server does).
func isTerminalWireStatus(status string) bool {
	switch status {
	case "done", "failed", "cancelled", "timeout":
		return true
	default:
		return false
	}
}

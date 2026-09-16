package wshub

import (
	"log/slog"

	"github.com/inhere/gofer/internal/wsproto"
)

// Adopter is the seam through which the hub ADOPTS the `recovering` jobs a PREVIOUS
// serve process left in the store (RECOV-01 R4). It exists because the hub is not
// allowed to know about jobs: the store, the log files and the classify/finish path
// belong to the job service, which implements this contract behind the assembly layer.
//
// Without it a restart is a dead end: the old process's sinks died with it, so the
// hub's recovery set — built from LIVE connections only — can never hold the rows the
// store still has, and the one-shot startup window can only defer their failure. With
// it, a worker process that reconnects and reports the job in its `inflight` list gets
// the job (and its logs) back instead of losing it.
type Adopter interface {
	// AdoptAfterRestart reconciles the store-held `recovering` jobs of workerID
	// against a worker process that just registered with instanceID and the given
	// inflight list (always non-nil for a RECOV-01 worker: an empty list is a
	// statement, "I track nothing").
	//
	// It returns the adopting sinks for the jobs that process PROVES it still runs
	// (the same process instance, reported in flight) — keyed by job id, with the
	// byte offsets already persisted server-side, so the hub can put them into the
	// resume ack — and fails every other store-held recovering job of that worker,
	// returning their ids as lost so the hub can log them. Failing them there (rather
	// than here) keeps the terminal write in the layer that owns the store; the hub
	// only reports the outcome.
	AdoptAfterRestart(workerID, instanceID string, inflight []wsproto.InflightJob) (adopted map[string]AdoptedJob, lost []string)
}

// AdoptedJob is one job the hub takes over from a previous serve process: the sink
// that will receive its frames on the new connection plus the server-side offsets the
// worker must rewind to (the hub echoes them in the resume ack, exactly as it does for
// a job held live in `recovering`).
type AdoptedJob struct {
	Sink                 JobSink
	StdoutOff, StderrOff int64
}

// SetAdopter wires the RECOV-01 R4 adoption seam. Like SetRecoverWindow it is called
// once at assemble time, before any connection is accepted; a nil adopter (the
// default) leaves adoption off and a restarted serve fails the rows after the window,
// exactly as before R4.
func (h *Hub) SetAdopter(a Adopter) { h.adopter = a }

// planAdoption merges the store-held recovering jobs into a registering worker's plan
// (RECOV-01 R4). It runs only for a worker that reported its `inflight` list — a
// pre-RECOV-01 worker cannot prove anything and keeps the window-only path — and only
// for a hub with an adopter wired.
//
// A job already planned by the live recovery set wins: that set means a sink is ALIVE
// for it in this process (a real disconnect/reconnect, or a restored plan), and
// adopting it again would create a second host-side sink for one job.
func (h *Hub) planAdoption(reg wsproto.Register, plan pendingRecovery) pendingRecovery {
	if h.adopter == nil || reg.Inflight == nil {
		return plan
	}
	adopted, lost := h.adopter.AdoptAfterRestart(reg.WorkerID, reg.InstanceID, reg.Inflight)
	for _, jobID := range lost {
		slog.Warn("worker.job_lost", "event", "worker.job_lost", "component", "server",
			"worker_id", reg.WorkerID, "job_id", jobID, "error_code", "worker_lost",
			"reason", "job not tracked after restart")
	}
	for jobID, aj := range adopted {
		if _, ok := plan.resumed[jobID]; ok {
			continue
		}
		if _, ok := plan.waiting[jobID]; ok {
			continue
		}
		if h.hasLiveSink(reg.WorkerID, jobID) {
			continue
		}
		plan.resumed[jobID] = &recoveringJob{sink: aj.Sink, stdoutOff: aj.StdoutOff, stderrOff: aj.StderrOff}
	}
	return plan
}

// hasLiveSink reports whether a connection ALIVE in this hub still holds a sink for
// jobID — the "no live sink" precondition of adoption. The registering connection is
// not yet in the registry when this runs (its ack has not been written), so what it
// sees is any OTHER live connection of the same worker.
func (h *Hub) hasLiveSink(workerID, jobID string) bool {
	wc, ok := h.reg.Get(workerID)
	if !ok {
		return false
	}
	return wc.sink(jobID) != nil
}

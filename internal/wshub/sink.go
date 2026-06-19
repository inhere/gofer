package wshub

import "github.com/inhere/gofer/internal/wsproto"

// JobSink receives the worker frames the hub demuxes for one job and signals the
// terminal result. It decouples the hub (transport) from the workerRunner
// (which owns the host-side mirror writers + the Run wait). The concrete
// implementation (boundedSink) lives in internal/runner/worker and applies the
// C4-style back-pressure (review #3).
//
// Lifecycle / ordering invariants (review #2):
//   - The workerRunner registers a sink (RegisterSink) BEFORE the hub sends the
//     dispatch frame, so the very first log frame is never dropped.
//   - The hub's single per-connection read loop calls WriteLog/Finish for a job
//     IN ORDER (never one goroutine per frame), so Finish is always observed
//     after every preceding WriteLog for that job.
type JobSink interface {
	// WriteLog mirrors one inbound log frame onto the host job's stream writer.
	// Implementations MUST NOT block the caller indefinitely (it is the hub's
	// single read loop): bound/throttle internally, never spawn a per-frame
	// goroutine that could reorder relative to Finish.
	WriteLog(stream string, seq int, text string)
	// Finish delivers the authoritative terminal result, unblocking the
	// workerRunner.Run wait. It must be non-blocking (drop a duplicate result).
	Finish(res wsproto.Result)
}

package wshub

import (
	"encoding/json"

	"github.com/inhere/gofer/internal/wsproto"
)

// JobSink receives the worker frames the hub demuxes for one job and signals the
// terminal result. It decouples the hub (transport) from the workerRunner
// (which owns the host-side mirror writers + the Run wait). The concrete
// implementation (boundedSink) lives in internal/runner/worker and applies the
// C4-style back-pressure (review #3).
//
// Lifecycle / ordering invariants (review #2):
//   - The workerRunner registers a sink (RegisterSink) BEFORE the hub sends the
//     dispatch frame, so the very first log frame is never dropped.
//   - The hub's single per-connection read loop calls WriteLog/OnInteraction/
//     Finish for a job IN ORDER (never one goroutine per frame), so Finish is
//     always observed after every preceding WriteLog/OnInteraction for that job,
//     and an interaction{open} can never be reordered after the result (which
//     would otherwise be rejected by injectInteraction on a terminal job).
type JobSink interface {
	// WriteLog mirrors one inbound log frame onto the host job's stream writer.
	// Implementations MUST NOT block the caller indefinitely (it is the hub's
	// single read loop): bound/throttle internally, never spawn a per-frame
	// goroutine that could reorder relative to Finish.
	WriteLog(stream string, seq int, text string)
	// OnInteraction bridges one worker-raised interaction frame (P2): action is
	// open|answered|cancelled and interaction is the raw job.Interaction body.
	// It must not block the hub's read loop (the blocking WaitAnswer wait is the
	// sink's own goroutine, not this call). The answer is sent back over WS via
	// the hub the sink was constructed with.
	OnInteraction(action string, interaction json.RawMessage)
	// OnOutcome stashes the worker-captured产出 (P4) for the job; the workerRunner
	// returns it on the runner.Result so the host applies it before finishing. It
	// arrives strictly BEFORE Finish (the worker sends the outcome frame just
	// before the result frame), enforced by the single in-order read loop. It must
	// be non-blocking (the hub's read loop).
	OnOutcome(o wsproto.Outcome)
	// Finish delivers the authoritative terminal result, unblocking the
	// workerRunner.Run wait. It must be non-blocking (drop a duplicate result).
	Finish(res wsproto.Result)
	// OnDisconnect signals that the worker connection dropped while this job was
	// in flight (worker-lost, §5.3). It unblocks the workerRunner.Run wait with
	// err (worker disconnected) so the host job is finished failed via the existing
	// classify/finish path. It must be non-blocking (the hub calls it from the
	// disconnect path) and idempotent w.r.t. a concurrent Finish (whichever lands
	// first wins; a result that beat the disconnect keeps the job's real outcome).
	OnDisconnect(err error)
}

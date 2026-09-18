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
	// OnJobEvent bridges one worker-raised job life-cycle event (SUP-01 G): the
	// approval gate / verify step events the worker recorded while executing this
	// job, which the host records on ITS row (tagged with the worker that raised
	// them). Duplicates are dropped by the hub before this is called. It runs on the
	// hub's single read loop, so it must not block for long (the host's own event
	// write is a local insert).
	OnJobEvent(ev wsproto.JobEvent)
	// Finish delivers the authoritative terminal result, unblocking the
	// workerRunner.Run wait. It must be non-blocking (drop a duplicate result).
	Finish(res wsproto.Result)
	// Suspend signals that the worker connection dropped while this job was in
	// flight, but the job is being HELD for a possible reconnect (RECOV-01) instead
	// of being failed: the host job moves to `recovering` and the worker gets a
	// bounded window to come back. It must not block (the hub calls it from the
	// disconnect path).
	//
	// The two return values are the byte counts this sink has durably written to the
	// host job's stdout/stderr logs — the SERVER-side offsets the hub echoes in the
	// resume ack so the reconnecting worker rewinds to exactly what was persisted
	// (no gap, no duplicate). They are only meaningful for a worker job; the hub
	// never calls Suspend when recovery is disabled.
	Suspend(reason string) (stdoutOff, stderrOff int64)
	// Resume signals that the worker process came back and still has this job
	// (RECOV-01): the host job returns to `running`. It must be idempotent w.r.t. a
	// concurrent Finish/OnDisconnect (whichever terminal signal landed first wins,
	// and a Resume after one is a no-op) and must not block.
	Resume()
	// OnDisconnect signals that the worker connection dropped while this job was
	// in flight (worker-lost, §5.3). It unblocks the workerRunner.Run wait with
	// err (worker disconnected) so the host job is finished failed via the existing
	// classify/finish path. It must be non-blocking (the hub calls it from the
	// disconnect path) and idempotent w.r.t. a concurrent Finish (whichever lands
	// first wins; a result that beat the disconnect keeps the job's real outcome).
	//
	// With RECOV-01 recovery enabled this is called when the recovery window
	// EXPIRES (err = worker lost), not at the moment of the disconnect.
	OnDisconnect(err error)
}

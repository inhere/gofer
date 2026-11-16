package worker

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/wsproto"
)

// Job-event replay (protocol v30). A mirrored job event used to be written once and
// lost with the connection. When BOTH ends speak v30 the worker now numbers each job's
// events (JobEvent.Seq), keeps them in a bounded unconfirmed buffer, and after a
// reconnect drops what the hub's resume ack says it already applied (ResumeJob.EventSeq)
// and replays the rest in order. The hub de-duplicates on the same number, so a replay
// can never produce a second row or a second notification.
//
// The buffer is only trimmed by that ack (and when the job ends): there is deliberately
// no per-event ack on a healthy connection, which would double the frame count.
const (
	// jobEventBufPerJob caps one job's unconfirmed events; jobEventBufGlobal caps the sum
	// over every job of this worker. Overflow drops the OLDEST event and counts it.
	jobEventBufPerJob = 256
	jobEventBufGlobal = 4096
)

// jobEventSeqEnabled reports whether the current hub connection takes part in event
// replay: only a v30 hub numbers/dedups by seq, so an older one keeps the old behaviour.
func (cl *Client) jobEventSeqEnabled() bool {
	return wsproto.SupportsJobEventSeq(int(cl.serverProto.Load()))
}

// stampJobEvent numbers ev for remoteID and remembers it in the job's unconfirmed
// buffer. It leaves ev untouched (Seq 0) when replay is not enabled for the current hub
// or the job has no in-flight entry (nothing to replay into).
func (cl *Client) stampJobEvent(remoteID string, ev *wsproto.JobEvent) {
	if !cl.jobEventSeqEnabled() {
		return
	}
	cl.inflMu.Lock()
	defer cl.inflMu.Unlock()
	f := cl.inflight[remoteID]
	if f == nil {
		return
	}
	f.evSeq++
	ev.Seq = f.evSeq
	f.evBuf = append(f.evBuf, *ev)
	cl.evTrimLocked(f)
}

// evTrimLocked enforces the per-job and global caps after an append, dropping the
// oldest events. Only an event that never reached the wire (seq above evSent) counts as
// LOST: one that was written has, barring a socket that died under the write, already
// been applied by the hub, and every long job overflows 256 events that way without
// anything being missing. Callers hold inflMu.
func (cl *Client) evTrimLocked(f *inflightJob) {
	for len(f.evBuf) > jobEventBufPerJob {
		f.evDropOldest()
	}
	for cl.evTotalLocked() > jobEventBufGlobal {
		var big *inflightJob
		for _, o := range cl.inflight {
			if big == nil || len(o.evBuf) > len(big.evBuf) {
				big = o
			}
		}
		if big == nil || len(big.evBuf) == 0 {
			return
		}
		big.evDropOldest()
	}
}

func (f *inflightJob) evDropOldest() {
	old := f.evBuf[0]
	f.evBuf = f.evBuf[1:]
	if old.Seq > f.evSent {
		f.evLost++
	}
}

func (cl *Client) evTotalLocked() int {
	n := 0
	for _, f := range cl.inflight {
		n += len(f.evBuf)
	}
	return n
}

// markJobEventSent records that the event with this seq was written to the wire, so a
// later overflow drop of it is not reported as lost.
func (cl *Client) markJobEventSent(remoteID string, seq int64) {
	if seq <= 0 {
		return
	}
	cl.inflMu.Lock()
	if f := cl.inflight[remoteID]; f != nil && seq > f.evSent {
		f.evSent = seq
	}
	cl.inflMu.Unlock()
}

// jobEventsAck applies the hub's resume ack for one job: everything up to eventSeq is
// applied there and leaves the buffer, and the "acp" mirror offset rewinds to the host
// file's size (R16: same footing as stdout/stderr).
func (cl *Client) jobEventsAck(remoteID string, eventSeq, acpOff int64) {
	cl.inflMu.Lock()
	defer cl.inflMu.Unlock()
	f := cl.inflight[remoteID]
	if f == nil {
		return
	}
	f.acpOff = acpOff
	n := 0
	for n < len(f.evBuf) && f.evBuf[n].Seq <= eventSeq {
		n++
	}
	f.evBuf = f.evBuf[n:]
	if eventSeq > f.evSent {
		f.evSent = eventSeq
	}
}

// pendingJobEvents returns a copy of the job's unconfirmed events in order, first
// appending the job.events_lost notice when overflow dropped events that never went out
// (the notice takes the next seq, so the hub applies it like any other event).
func (cl *Client) pendingJobEvents(remoteID string) []wsproto.JobEvent {
	cl.inflMu.Lock()
	defer cl.inflMu.Unlock()
	f := cl.inflight[remoteID]
	if f == nil {
		return nil
	}
	if f.evLost > 0 {
		f.evSeq++
		notice := wsproto.JobEvent{
			JobID: remoteID, Type: job.EventJobEventsLost, TS: time.Now().Unix(), Seq: f.evSeq,
			Detail: mustRaw(map[string]any{"count": f.evLost}),
		}
		f.evLost = 0
		// Appended without trimming: the notice must not push a real event out (and so
		// count it lost again); the next stamped event restores the cap.
		f.evBuf = append(f.evBuf, notice)
	}
	if len(f.evBuf) == 0 {
		return nil
	}
	return append([]wsproto.JobEvent(nil), f.evBuf...)
}

// replayJobEvents re-sends one job's unconfirmed events on the handshake connection,
// before it is published, so they precede anything the live pumps write. It reports
// false when a write failed (the caller stops; the buffer is untouched and the next
// ack/replay retries).
func (cl *Client) replayJobEvents(ctx context.Context, conn *websocket.Conn, remoteID string) bool {
	for _, ev := range cl.pendingJobEvents(remoteID) {
		if err := cl.writeFrameOn(ctx, conn, wsproto.TypeJobEvent, remoteID, ev); err != nil {
			return false
		}
		cl.markJobEventSent(remoteID, ev.Seq)
	}
	return true
}

// inflightCacheTerminal is inflightCacheResult plus the Outcome frame that must reach
// the hub BEFORE the Result: both ride one cache entry (and its workerResultTTL), so a
// job that ends while disconnected is replayed as events -> Outcome -> Result.
func (cl *Client) inflightCacheTerminal(remoteID string, o *wsproto.Outcome, res wsproto.Result) {
	cl.inflightCacheResult(remoteID, res)
	if o == nil {
		return
	}
	cp := *o
	cl.inflMu.Lock()
	if f := cl.inflight[remoteID]; f != nil {
		f.outcome = &cp
	}
	cl.inflMu.Unlock()
}

// sendTerminal delivers a job's Outcome (when it has one) and Result in that order. An
// Outcome write that fails parks BOTH in the cache instead of sending the Result alone:
// a Result that overtook its Outcome would finish the host job without the job's
// outputs, and the replay path restores the order.
func (cl *Client) sendTerminal(ctx context.Context, remoteJobID string, o *wsproto.Outcome, res wsproto.Result) error {
	if o != nil {
		if err := cl.writeFrame(ctx, wsproto.TypeOutcome, remoteJobID, *o); err != nil {
			cl.inflightCacheTerminal(remoteJobID, o, res)
			return err
		}
	}
	return cl.sendResult(ctx, remoteJobID, res)
}

// cachedOutcome returns the Outcome parked with a cached Result (nil if none).
func (cl *Client) cachedOutcome(remoteID string) *wsproto.Outcome {
	cl.inflMu.Lock()
	defer cl.inflMu.Unlock()
	if f := cl.inflight[remoteID]; f != nil && f.outcome != nil {
		cp := *f.outcome
		return &cp
	}
	return nil
}

// replayJobState re-sends everything the hub may be missing for this process's
// in-flight jobs on the handshake connection: per job its unconfirmed events (only when
// replay is enabled for this hub), then, for a job that already ended, the cached
// Outcome and Result. A write failure stops the whole replay and keeps every cache for
// the next reconnect.
//
// It runs from applyResume, on the freshly handshaken connection and BEFORE that
// connection is published — so the hub's recovery plan (which saw these jobs as terminal
// in our `inflight` snapshot and is holding their host jobs in `recovering`, waiting for
// the Result) gets the authoritative ending (RECOV-01). A Result whose entry is past
// workerResultTTL was already swept by inflightResult: the hub has given up on that job,
// and replaying into a finished job would be noise.
func (cl *Client) replayJobState(ctx context.Context, conn *websocket.Conn) {
	ids := cl.inflightIDs()
	sort.Strings(ids)
	replayEvents := cl.jobEventSeqEnabled()
	for _, id := range ids {
		if replayEvents && !cl.replayJobEvents(ctx, conn, id) {
			return
		}
		res, ok := cl.inflightResult(id)
		if !ok {
			continue
		}
		if o := cl.cachedOutcome(id); o != nil {
			if err := cl.writeFrameOn(ctx, conn, wsproto.TypeOutcome, id, *o); err != nil {
				return
			}
		}
		if err := cl.writeFrameOn(ctx, conn, wsproto.TypeResult, id, res); err != nil {
			return // still not deliverable: keep the cache for the next reconnect
		}
		slog.Debug("worker.terminal_replayed", "worker_id", cl.workerID, "job_id", id)
		cl.inflightDrop(id)
	}
}

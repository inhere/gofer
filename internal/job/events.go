package job

import (
	"encoding/json"
	"log/slog"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/notify"
)

// deliverySink is the narrow write side recordEvent uses to enqueue E14 webhook
// deliveries, satisfied by *jobstore.Store. Like eventSink it is an interface so
// tests can observe/inject the enqueue path. Production uses s.meta.
type deliverySink interface {
	InsertDelivery(d jobstore.Delivery) (int64, error)
}

// MaxEventDetailBytes caps a recorded event's detail_json. A detail larger than
// this is dropped (the event is still recorded with an empty detail) so a
// pathological payload never bloats the stream — events are an audit trail, not a
// data channel.
const MaxEventDetailBytes = 8 * 1024

// eventSink is the narrow write side recordEvent uses, satisfied by
// *jobstore.Store. It exists so tests can inject a failing sink and prove
// recordEvent is best-effort (never affects the job's terminal state). Production
// always uses s.meta.
type eventSink interface {
	InsertJobEvent(e jobstore.JobEvent) (int64, error)
}

// JobEventObserver receives the events a REMOTE executor mirrors to the machine that
// submitted the job (SUP-01 G). The worker client installs one (see
// worker.Client) so the events its local job service records for a DISPATCHED job
// ride the hub connection back to the host job, which would otherwise never see
// them. It must never block the job: an implementation is a bounded queue that
// drops on overflow.
type JobEventObserver func(jobID, eventType string, detail map[string]any)

// mirroredEventTypes is the WHITELIST of event types a worker mirrors up (SUP-01 G):
// exactly the ones raised on the EXECUTING machine that the host cannot observe or
// reconstruct — the approval gate's request/answer/timeout and the verify step's
// start/finish. The job's own lifecycle (submitted/running/terminal/cancelled) is
// deliberately absent: the HUB records those for the host job itself, so mirroring
// them would double every row and every notification.
var mirroredEventTypes = map[string]bool{
	EventJobPermissionRequested: true,
	EventJobPermissionAnswered:  true,
	EventJobPermissionTimedOut:  true,
	EventJobVerifyStarted:       true,
	EventJobVerifyFinished:      true,
}

// SetEventObserver installs (or with nil, clears) the mirror observer. It is called
// once by the worker client before it starts running jobs; recordEvent reads it on
// every event, so the two are ordered by the atomic swap rather than by a lock the
// event path would have to take.
func (s *Service) SetEventObserver(fn JobEventObserver) {
	if fn == nil {
		s.eventObserver.Store(nil)
		return
	}
	s.eventObserver.Store(&fn)
}

// notifyEventObserver hands one just-recorded, whitelisted event to the observer
// (best-effort: no observer, or a panicking one, never affects the job).
func (s *Service) notifyEventObserver(jobID, eventType, detailJSON string) {
	if !mirroredEventTypes[eventType] {
		return
	}
	fn := s.eventObserver.Load()
	if fn == nil {
		return
	}
	var detail map[string]any
	if detailJSON != "" {
		if json.Unmarshal([]byte(detailJSON), &detail) != nil {
			detail = nil
		}
	}
	(*fn)(jobID, eventType, detail)
}

// recordEvent appends one append-only lifecycle event for a job (E13, design
// §5.2). It is BEST-EFFORT: a marshal failure, an oversized detail or a write
// error only logs a warning — it MUST NOT panic and MUST NOT influence the job's
// terminal state (the same iron rule as captureOutcomes). detail must not carry
// secrets (SR403); callers pass only descriptive metadata.
//
// detail is marshalled to JSON; a nil detail or a payload exceeding
// MaxEventDetailBytes records an empty detail rather than failing.
func (s *Service) recordEvent(jobID, eventType string, detail any) {
	var dj string
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil && len(b) <= MaxEventDetailBytes {
			dj = string(b)
		}
	}
	sink := s.events
	if sink == nil {
		sink = s.meta
	}
	at := s.nowFn().Unix()
	seq, err := sink.InsertJobEvent(jobstore.JobEvent{
		JobID:  jobID,
		Type:   eventType,
		Detail: dj,
		At:     at,
	})
	if err != nil {
		slog.Warn("recordEvent: insert job event", "job_id", jobID, "type", eventType, "err", err)
		return
	}
	// E14: now that the event is durably persisted with its seq, enqueue a webhook
	// delivery for each subscribed target (best-effort — an enqueue failure only
	// warns and never affects the job's terminal state, same iron rule as above).
	s.enqueueDeliveries(seq, jobID, eventType, dj, at)
	// SUP-01 G: a mirrorable event also goes to the observer (the worker client's
	// connection pump) so the hub that dispatched this job learns about it too.
	s.notifyEventObserver(jobID, eventType, dj)
}

// enqueueDeliveries inserts one pending webhook delivery per subscribed target
// for a just-recorded event (E14, design §5.6). It is BEST-EFFORT: every failure
// (no config, unknown job, enqueue write error) only warns and never affects the
// job. Matching: the global notification config selects webhooks subscribed to
// this event type AND project; the per-project notify_enabled gate (default on)
// can suppress the whole project. Enqueued rows are pending with next_retry_at=now
// so the sweeper picks them up immediately.
func (s *Service) enqueueDeliveries(seq int64, jobID, eventType, detailJSON string, at int64) {
	cfg := s.config()
	if cfg == nil || cfg.Server.Notification == nil || len(cfg.Server.Notification.Webhooks) == 0 {
		return // no notification configured — nothing to enqueue (zero behaviour change)
	}
	// Resolve the job's project (and per-project gate). Get falls back to the meta
	// store, so a finished/evicted job still resolves. An unknown job (should not
	// happen — we just recorded its event) is skipped.
	jr, ok := s.Get(jobID)
	if !ok {
		return
	}
	if proj, ok := cfg.Projects[jr.ProjectKey]; ok && !proj.IsNotifyEnabled() {
		return // project opted out of notification
	}

	targets := notify.MatchWebhooks(cfg.Server.Notification, eventType, jr.ProjectKey)
	if len(targets) == 0 {
		return
	}
	sink := s.deliveries
	if sink == nil {
		sink = s.meta
	}
	for _, w := range targets {
		if _, err := sink.InsertDelivery(jobstore.Delivery{
			EventSeq:    seq,
			JobID:       jobID,
			Target:      w.URL,
			Status:      jobstore.DeliveryPending,
			NextRetryAt: at, // due now
			CreatedAt:   at,
		}); err != nil {
			slog.Warn("enqueueDeliveries: insert delivery", "job_id", jobID, "type", eventType, "target", w.URL, "err", err)
		}
	}
}

// ListDeliveriesByJob returns a job's webhook deliveries (E14) for the read-only
// deliveries view, forwarding to the metadata store.
func (s *Service) ListDeliveriesByJob(jobID string) ([]jobstore.Delivery, error) {
	return s.meta.ListDeliveriesByJob(jobID)
}

// ListJobEvents returns a job's append-only lifecycle events in seq order (E13),
// forwarding to the metadata store. sinceSeq > 0 returns only events after that
// cursor (the HTTP ?since / SSE incremental path). It does not consult in-memory
// state: events are durable-only (recordEvent writes them straight to the DB).
func (s *Service) ListJobEvents(jobID string, sinceSeq int64) ([]jobstore.JobEvent, error) {
	return s.meta.ListJobEvents(jobID, sinceSeq)
}

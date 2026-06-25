package job

import (
	"encoding/json"
	"log"

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
		log.Printf("recordEvent: job %s type %s: %v", jobID, eventType, err)
		return
	}
	// E14: now that the event is durably persisted with its seq, enqueue a webhook
	// delivery for each subscribed target (best-effort — an enqueue failure only
	// warns and never affects the job's terminal state, same iron rule as above).
	s.enqueueDeliveries(seq, jobID, eventType, dj, at)
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
			log.Printf("enqueueDeliveries: job %s type %s target %s: %v", jobID, eventType, w.URL, err)
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

package job

import (
	"encoding/json"
	"log"

	"github.com/inhere/gofer/internal/jobstore"
)

// maxEventDetailBytes caps a recorded event's detail_json. A detail larger than
// this is dropped (the event is still recorded with an empty detail) so a
// pathological payload never bloats the stream — events are an audit trail, not a
// data channel.
const maxEventDetailBytes = 8 * 1024

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
// maxEventDetailBytes records an empty detail rather than failing.
func (s *Service) recordEvent(jobID, eventType string, detail any) {
	var dj string
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil && len(b) <= maxEventDetailBytes {
			dj = string(b)
		}
	}
	sink := s.events
	if sink == nil {
		sink = s.meta
	}
	if _, err := sink.InsertJobEvent(jobstore.JobEvent{
		JobID:  jobID,
		Type:   eventType,
		Detail: dj,
		At:     s.nowFn().Unix(),
	}); err != nil {
		log.Printf("recordEvent: job %s type %s: %v", jobID, eventType, err)
	}
}

// ListJobEvents returns a job's append-only lifecycle events in seq order (E13),
// forwarding to the metadata store. sinceSeq > 0 returns only events after that
// cursor (the HTTP ?since / SSE incremental path). It does not consult in-memory
// state: events are durable-only (recordEvent writes them straight to the DB).
func (s *Service) ListJobEvents(jobID string, sinceSeq int64) ([]jobstore.JobEvent, error) {
	return s.meta.ListJobEvents(jobID, sinceSeq)
}

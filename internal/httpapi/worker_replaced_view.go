package httpapi

import (
	"encoding/json"

	"github.com/inhere/gofer/internal/job"
)

// workerReplacedView is the most recent takeover of a worker's connection by a
// process with a different instance_id (worker.replaced event).
type workerReplacedView struct {
	At          int64  `json:"at"`
	OldInstance string `json:"old_instance,omitempty"`
	NewInstance string `json:"new_instance,omitempty"`
	OldRemote   string `json:"old_remote,omitempty"`
	NewRemote   string `json:"new_remote,omitempty"`
	Suppressed  int    `json:"suppressed,omitempty"`
	Conflict    bool   `json:"conflict,omitempty"`
}

// latestReplaced reads the newest worker.replaced event of a worker; nil when none.
func (s *Server) latestReplaced(workerID string) *workerReplacedView {
	if s.jobs == nil {
		return nil
	}
	events, err := s.jobs.ListScopedEventsDesc(job.WorkerEventScope(workerID), 0, 1)
	if err != nil || len(events) == 0 || events[0].Type != job.EventWorkerReplaced {
		return nil
	}
	v := &workerReplacedView{At: events[0].At}
	if json.Unmarshal([]byte(events[0].Detail), v) != nil || !v.Conflict {
		// Events recorded before the conflict flag existed include plain restart and
		// upgrade handovers; they are not a two-processes warning.
		return nil
	}
	v.At = events[0].At
	return v
}

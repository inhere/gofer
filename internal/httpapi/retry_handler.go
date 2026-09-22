package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// retryView is the JSON projection of one AUTO-03 durable retry row (design §二.3).
// Every timestamp is unix SECONDS, like the other job views.
//
// max_attempts is resolved from the row's OWN request_json (job.RetryMaxAttempts):
// the ceiling belongs to the chain the row was scheduled under, not to whatever the
// config says when a human reads it, so a retry keeps explaining itself after a
// config change. 0 means the row carries no policy at all (a hand-written row) — the
// field is then omitted rather than invented, and `attempt 2` prints with no ceiling.
//
// lease_until is non-zero only while a sweeper holds the row for one submit attempt
// (that is an internal claim, not a time the caller should wait on); new_job_id only
// exists once the retry was actually submitted.
type retryView struct {
	ID          string `json:"id"`
	SourceJobID string `json:"source_job_id"`
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"max_attempts,omitempty"`
	Reason      string `json:"reason"`
	NextRunAt   int64  `json:"next_run_at"`
	LeaseUntil  int64  `json:"lease_until,omitempty"`
	State       string `json:"state"`
	NewJobID    string `json:"new_job_id,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

// retriesResp is the list envelope (`{job_id, retries: [...]}`). The job id is echoed
// so a client watching several jobs does not have to pair the response back by hand.
type retriesResp struct {
	JobID   string      `json:"job_id"`
	Retries []retryView `json:"retries"`
}

func toRetryView(rec jobstore.RetryRecord) retryView {
	return retryView{
		ID:          rec.ID,
		SourceJobID: rec.SourceJobID,
		Attempt:     rec.Attempt,
		MaxAttempts: job.RetryMaxAttempts(rec),
		Reason:      rec.Reason,
		NextRunAt:   rec.NextRunAt,
		LeaseUntil:  rec.LeaseUntil,
		State:       rec.State,
		NewJobID:    rec.NewJobID,
		CreatedAt:   rec.CreatedAt,
	}
}

// handleListRetries serves GET /v1/jobs/{id}/retries (R2/AUTO-03): the job's durable
// retry chain, oldest attempt first (the order ListRetries returns). An unknown job is
// a 404 — an empty list for a typo would read as "this job has no retries" (the same
// reason handleListWakeups 404s). Reads are not gated: a retry is job metadata, like
// the job's wakeups or events.
func (s *Server) handleListRetries(c *rux.Context) {
	id := c.Param("id")
	if _, ok := s.jobs.Get(id); !ok {
		writeError(c, http.StatusNotFound, "unknown job", "job "+id+" does not exist")
		return
	}
	rows, err := s.jobs.ListRetries(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list retries failed", err.Error())
		return
	}
	out := retriesResp{JobID: id, Retries: make([]retryView, 0, len(rows))}
	for _, rec := range rows {
		out.Retries = append(out.Retries, toRetryView(rec))
	}
	c.JSON(http.StatusOK, out)
}

// handleCancelRetry serves DELETE /v1/retries/{rid}: drop one retry that has not run
// yet (pending, or claimed by a sweeper that has not submitted it) so it will not be
// submitted. A row that already ran or was already cancelled is terminal — that same
// DELETE answers 404 with the store's own message, never a fake success.
func (s *Server) handleCancelRetry(c *rux.Context) {
	if err := s.jobs.CancelRetry(c.Param("rid")); err != nil {
		writeError(c, http.StatusNotFound, "retry cancel rejected", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "cancelled"})
}

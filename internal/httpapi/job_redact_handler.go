package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
)

type jobRedactRequest struct {
	Literals []string `json:"literals"`
	Patterns []string `json:"patterns"`
}

// handleRedactJob replaces sensitive text in one terminal job. Only the caller's own job
// or a holder of job.delete.others (the root administrator) may call it — the same rule
// DELETE /v1/jobs/{id} applies (plan T1.5a); the response is aggregate-only by contract.
func (s *Server) handleRedactJob(c *rux.Context) {
	jobID := c.Param("id")
	res, ok := s.jobs.Get(jobID)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "job not found")
		return
	}
	if !s.mayDeleteJob(c, res) {
		s.denyJobAction(c, authz.PermJobDeleteOthers)
		return
	}
	var req jobRedactRequest
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid redact request", "body must contain literals and/or patterns")
		return
	}
	report, err := s.jobs.Meta().RedactJob(jobID, req.Literals, req.Patterns)
	if err != nil {
		status := http.StatusBadRequest
		if len(err.Error()) >= 22 && err.Error()[:22] == "jobstore: redact: job " {
			status = http.StatusNotFound
		}
		writeError(c, status, "job redaction failed", redactSafeError(err))
		return
	}
	c.JSON(http.StatusOK, report)
}

func redactSafeError(err error) string {
	if err == nil {
		return ""
	}
	// Do not echo caller literals or regex bodies in transport errors.
	msg := err.Error()
	if len(msg) > 240 {
		msg = msg[:240]
	}
	return msg
}

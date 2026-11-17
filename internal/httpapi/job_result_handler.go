package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"
	"github.com/inhere/gofer/internal/job"
)

// handleSubmitJobResult is POST /v1/jobs/{id}/result: a job's agent reports how its own
// run ended (gofer_submit_result). Only that job's own credential may write it — a
// user-class caller (a person or a token) cannot speak for a job, and another job's
// credential is refused. The report is recorded and shown; it never changes the job's
// status. A second report for the same job is a 409.
func (s *Server) handleSubmitJobResult(c *rux.Context) {
	id := c.Param("id")
	if callerKindFromCtx(c) != callerKindJob {
		writeError(c, http.StatusForbidden, "job result denied", "only the job's own credential may report its result")
		return
	}
	if callerFromCtx(c) != id {
		writeError(c, http.StatusForbidden, "job credential may not report another job's result", "a job credential may only report its own job")
		return
	}
	var body job.SelfResultInput
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid job result", err.Error())
		return
	}
	res, err := s.jobs.SubmitSelfResult(id, body, callerFromCtx(c))
	if err != nil {
		switch {
		case errors.Is(err, job.ErrInvalidRequest):
			writeError(c, http.StatusBadRequest, "invalid job result", err.Error())
		case errors.Is(err, job.ErrSelfResultExists):
			writeError(c, http.StatusConflict, "result already reported", "this job already reported its result")
		case strings.Contains(err.Error(), "unknown job"):
			writeError(c, http.StatusNotFound, "unknown job", err.Error())
		default:
			writeError(c, http.StatusInternalServerError, "record job result failed", err.Error())
		}
		return
	}
	c.JSON(http.StatusOK, res)
}

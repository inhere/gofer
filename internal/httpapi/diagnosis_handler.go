package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
)

// handleGetJobDiagnosis serves a job's diagnosis (R10): the derived termination reason,
// the continuation chain with its earliest failure, the stage timeline and deterministic
// next steps. Same visibility as GET /v1/jobs/{id} — a caller who may not read the job is
// denied with the same permission, and an unknown id is a 404.
//
// It only binds, authorises and forwards (G021): everything is derived in internal/job.
func (s *Server) handleGetJobDiagnosis(c *rux.Context) {
	id := c.Param("id")
	res, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
		return
	}
	if !s.mayReadJob(c, res) {
		s.denyJobAction(c, authz.PermJobReadOthers)
		return
	}
	d, err := s.jobs.DiagnoseIn(s.requestLang(c), c.Req.Context(), id)
	if err != nil {
		if err == job.ErrJobNotFound {
			writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
			return
		}
		writeError(c, http.StatusInternalServerError, "diagnosis failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, d)
}

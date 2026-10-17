package httpapi

import (
	"errors"
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
)

// sessionJobCallerAllowed applies the A1 caller rule at the HTTP boundary.
// A job credential can address only a session submitted by that job. It writes the 403
// itself on every refusal (exactly once), so the caller must not write another response.
func (s *Server) sessionJobCallerAllowed(c *rux.Context, target job.JobResult) bool {
	switch callerKindFromCtx(c) {
	case callerKindUser:
		// InHandler(job.manage.others, deny): speaking into someone else's session job needs
		// the permission (plan T1.5a).
		if !s.mayManageJob(c, target, false) {
			s.denyJobAction(c, authz.PermJobManageOthers)
			return false
		}
		return true
	case callerKindJob:
		if target.CallerID == callerFromCtx(c) {
			return true
		}
	}
	writeError(c, http.StatusForbidden, "session operation denied", "caller may only operate its own dispatched session job")
	return false
}

func (s *Server) sessionJobTarget(c *rux.Context) (job.JobResult, bool) {
	id := c.Param("id")
	target, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "job not found")
		return job.JobResult{}, false
	}
	if !s.sessionJobCallerAllowed(c, target) {
		return job.JobResult{}, false
	}
	return target, true
}

func sessionJobErrorStatus(err error) int {
	switch {
	case errors.Is(err, job.ErrJobNotFound):
		return http.StatusNotFound
	case errors.Is(err, job.ErrJobNotRunning):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

func (s *Server) handleSessionJobSay(c *rux.Context) {
	target, ok := s.sessionJobTarget(c)
	if !ok {
		return
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid session message", err.Error())
		return
	}
	if err := s.jobs.SaySession(target.ID, body.Message); err != nil {
		writeError(c, sessionJobErrorStatus(err), "session say rejected", err.Error())
		return
	}
	updated, _ := s.jobs.Get(target.ID)
	c.JSON(http.StatusOK, updated)
}

func (s *Server) handleSessionJobEnd(c *rux.Context) {
	target, ok := s.sessionJobTarget(c)
	if !ok {
		return
	}
	if err := s.jobs.EndSession(target.ID); err != nil {
		writeError(c, sessionJobErrorStatus(err), "session end rejected", err.Error())
		return
	}
	updated, _ := s.jobs.Get(target.ID)
	c.JSON(http.StatusOK, updated)
}

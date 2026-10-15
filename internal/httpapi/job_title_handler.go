package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"
	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
)

type patchJobBody struct {
	Title *string `json:"title"`
}

func (s *Server) handlePatchJob(c *rux.Context) {
	if callerKindFromCtx(c) != callerKindUser && callerKindFromCtx(c) != callerKindJob {
		writeError(c, http.StatusForbidden, "job title update denied", "only a user caller or the job's own credential may change a title")
		return
	}
	id := c.Param("id")
	if callerKindFromCtx(c) == callerKindJob && callerFromCtx(c) != id {
		writeError(c, http.StatusForbidden, "job credential may not change another job's title", "a job credential may only change its own job")
		return
	}
	// InHandler(job.manage.others, deny): changing ANOTHER caller's job title needs the
	// permission (plan T1.5a).
	if res, ok := s.jobForAccess(id); ok && !s.mayManageJob(c, res, false) {
		s.denyJobAction(c, authz.PermJobManageOthers)
		return
	}
	var body patchJobBody
	if err := c.BindJSON(&body); err != nil || body.Title == nil {
		writeError(c, http.StatusBadRequest, "invalid job patch", "only the title field is supported")
		return
	}
	res, err := s.jobs.SetTitle(id, *body.Title, callerFromCtx(c))
	if err != nil {
		if errors.Is(err, job.ErrInvalidRequest) {
			writeError(c, http.StatusBadRequest, "invalid job title", err.Error())
			return
		}
		if strings.Contains(err.Error(), "unknown job") {
			writeError(c, http.StatusNotFound, "unknown job", err.Error())
			return
		}
		writeError(c, http.StatusInternalServerError, "update job title failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

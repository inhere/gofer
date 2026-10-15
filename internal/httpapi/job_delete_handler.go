package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"
	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
)

func (s *Server) handleDeleteJob(c *rux.Context) {
	id := c.Param("id")
	res, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "job not found")
		return
	}
	if !s.mayDeleteJob(c, res) {
		s.denyJobAction(c, authz.PermJobDeleteOthers)
		return
	}
	caller := callerFromCtx(c)
	if err := s.jobs.DeleteJob(id, caller); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, job.ErrJobFinishing) {
			status = http.StatusConflict
		} else if strings.HasPrefix(err.Error(), "jobstore: delete: job ") {
			if strings.Contains(err.Error(), "not found") {
				status = http.StatusNotFound
			} else {
				status = http.StatusConflict
			}
		}
		writeError(c, status, "job deletion failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"id": id, "deleted": true})
}

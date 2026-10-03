package httpapi

import (
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"
)

func (s *Server) handleDeleteJob(c *rux.Context) {
	id := c.Param("id")
	res, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "job not found")
		return
	}
	caller := callerFromCtx(c)
	if res.CallerID != caller && (s.cfg == nil || !s.cfg.CallerCanAdmin(caller)) {
		writeError(c, http.StatusForbidden, "job deletion denied", "only the job owner or an administrator may delete it")
		return
	}
	if err := s.jobs.Meta().DeleteJob(id, caller); err != nil {
		status := http.StatusBadRequest
		if strings.HasPrefix(err.Error(), "jobstore: delete: job ") {
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

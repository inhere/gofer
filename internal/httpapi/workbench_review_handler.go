package httpapi

import (
	"errors"
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/workbench"
)

func (s *Server) handleGetWorkbenchThreadDiff(c *rux.Context) {
	if s.workbench == nil {
		writeError(c, http.StatusServiceUnavailable, "workbench unavailable", "job metadata service is not wired")
		return
	}
	result, err := s.workbench.Diff(c.Param("id"))
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, workbench.ErrInvalidThreadID):
			status = http.StatusBadRequest
		case errors.Is(err, workbench.ErrUnknownThread):
			status = http.StatusNotFound
		case errors.Is(err, workbench.ErrMissingDiffBase), errors.Is(err, workbench.ErrNotResumable):
			status = http.StatusConflict
		case errors.Is(err, workbench.ErrDiffTimeout):
			status = http.StatusGatewayTimeout
		case errors.Is(err, workbench.ErrUnavailable):
			status = http.StatusServiceUnavailable
		}
		writeError(c, status, "get workbench thread diff failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, result)
}

package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/today"
)

// GET /v1/today/lanes — the Today page's parallel lanes (N3 §3): open work items and
// running / blocked plans with their agents, progress and health. Read-only; the
// assembly lives in internal/today.
func (s *Server) handleTodayLanes(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	v, err := today.NewLanesBuilder(s.work).Build()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "build today lanes failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, v)
}

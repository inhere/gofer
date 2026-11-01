package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/today"
)

// GET /v1/memory-findings?tracker_id=  server-side doctor over the tracker mirror.

func (s *Server) handleMemoryFindings(c *rux.Context) {
	if !s.todayReady(c) {
		return
	}
	items, err := s.today.MemoryFindings(today.MemoryFindingsQuery{TrackerID: c.Req.URL.Query().Get("tracker_id")})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "memory findings failed", err.Error())
		return
	}
	items = scopeRowsByKind(s, c, "tracker_repo", items, func(f today.MemoryFinding) string { return f.TrackerID })
	c.JSON(http.StatusOK, map[string]any{"findings": items})
}

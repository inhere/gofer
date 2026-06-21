package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/jobstore"
)

// handleListEvents returns a job's append-only lifecycle events (E13) in seq
// order. An unknown job id is a 404 (consistent with handleGetJob). The optional
// ?since=<seq> returns only events strictly after that cursor (incremental poll;
// a non-numeric/absent value lists from the start). The list is always a non-nil
// array, so an empty result serialises as {"events":[]}.
func (s *Server) handleListEvents(c *rux.Context) {
	id := c.Param("id")
	if _, ok := s.jobs.Get(id); !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
		return
	}
	// since 非数值 -> 0 -> 不过滤（仿 list/stream 的容错）。
	since, _ := strconv.ParseInt(c.Query("since"), 10, 64)
	events, err := s.jobs.ListJobEvents(id, since)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list events failed", err.Error())
		return
	}
	if events == nil {
		events = []jobstore.JobEvent{}
	}
	c.JSON(http.StatusOK, map[string]any{"events": events})
}

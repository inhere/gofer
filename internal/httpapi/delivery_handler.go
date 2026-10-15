package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/jobstore"
)

// handleListDeliveries returns a job's E14 webhook deliveries (read-only
// visibility, P2-d). An unknown job id is a 404 (consistent with handleGetJob /
// handleListEvents). The list is always a non-nil array, so an empty result
// serialises as {"deliveries":[]}.
func (s *Server) handleListDeliveries(c *rux.Context) {
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
	deliveries, err := s.jobs.ListDeliveriesByJob(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list deliveries failed", err.Error())
		return
	}
	if deliveries == nil {
		deliveries = []jobstore.Delivery{}
	}
	c.JSON(http.StatusOK, map[string]any{"deliveries": deliveries})
}

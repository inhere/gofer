package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/jobstore"
)

// P1 daily synthesis is read-only: the endpoint exposes bounded run metadata and the
// optional already-scrubbed material snapshot, never prompts or model output candidates.
func (s *Server) synthesisStore() *jobstore.Store {
	if s.trackerStore != nil {
		return s.trackerStore
	}
	if s.jobs != nil {
		return s.jobs.Meta()
	}
	return nil
}

func (s *Server) handleListMemorySyntheses(c *rux.Context) {
	store := s.synthesisStore()
	if store == nil {
		writeError(c, http.StatusServiceUnavailable, "memory synthesis unavailable", "job store is not wired")
		return
	}
	q := c.Req.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	rows, err := store.ListMemorySynthesisRuns(jobstore.MemorySynthesisRunFilter{
		ProjectKey: q.Get("project"), Day: q.Get("day"), Status: q.Get("state"), Limit: limit,
	})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list memory syntheses failed", err.Error())
		return
	}
	rows = scopeRows(s, c, rows, func(r jobstore.MemorySynthesisRun) string { return r.ProjectKey })
	for i := range rows {
		// Listing is metadata-only; material previews are available from the detail route.
		rows[i].MaterialSnapshot = ""
		rows[i].ResultJSON = ""
	}
	c.JSON(http.StatusOK, map[string]any{"runs": rows})
}

func (s *Server) handleGetMemorySynthesis(c *rux.Context) {
	store := s.synthesisStore()
	if store == nil {
		writeError(c, http.StatusServiceUnavailable, "memory synthesis unavailable", "job store is not wired")
		return
	}
	run, ok, err := store.GetMemorySynthesisRun(c.Param("id"))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get memory synthesis failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "memory synthesis not found", c.Param("id"))
		return
	}
	if !s.requireProjectScope(c, run.ProjectKey) {
		return
	}
	if callerKindFromCtx(c) == callerKindJob {
		// Job credentials may inspect their project-scoped metadata, but never a material
		// snapshot (the snapshot can contain user-authored text even after scrubbing).
		run.MaterialSnapshot = ""
	}
	c.JSON(http.StatusOK, run)
}

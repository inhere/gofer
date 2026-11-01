package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/memorysynthesis/engine"
)

// The read routes expose bounded run metadata and the optional already-scrubbed material
// snapshot, never prompts. The run route (POST /v1/memory-syntheses/run) only binds the
// request and forwards it to the synthesis engine.
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

// memorySynthesisRunRequest is the body of POST /v1/memory-syntheses/run.
type memorySynthesisRunRequest struct {
	Project string `json:"project"`
	// Day is YYYY-MM-DD in the server time zone; empty means today.
	Day   string `json:"day,omitempty"`
	Force bool   `json:"force,omitempty"`
}

// handleRunMemorySynthesis runs one explicit synthesis and answers when it is finished:
// {"outcome": succeeded|failed|reused|running|empty, "run": {...}} (run is absent for
// "empty"). A model failure is a recorded `failed` run, not an HTTP error; a disabled
// feature or an unusable agent is refused before anything is read or sent.
func (s *Server) handleRunMemorySynthesis(c *rux.Context) {
	if !workNotAWorker(c) || !stewardUserOnly(c, "run a memory synthesis") {
		return
	}
	if s.synthesis == nil {
		writeError(c, http.StatusServiceUnavailable, "memory synthesis unavailable", "job store is not wired")
		return
	}
	var body memorySynthesisRunRequest
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if body.Project == "" {
		writeError(c, http.StatusBadRequest, "project is required", "name the project to synthesize")
		return
	}
	if !s.requireProjectScope(c, body.Project) {
		return
	}
	origin, oerr := s.originCallerFromCtx(c)
	if oerr != nil {
		writeError(c, http.StatusForbidden, "caller ceiling", oerr.Error())
		return
	}
	out, err := s.synthesis.Run(c.Req.Context(), engine.Request{
		Project: body.Project, Day: body.Day, Force: body.Force, CreatedBy: workBy(c), Origin: origin,
	})
	switch {
	case err == nil:
		resp := map[string]any{"outcome": out.State}
		if out.Run.ID != "" {
			resp["run"] = out.Run
		}
		c.JSON(http.StatusOK, resp)
	case errors.Is(err, engine.ErrDisabled):
		writeError(c, http.StatusConflict, "memory synthesis disabled", err.Error())
	case errors.Is(err, engine.ErrProject), errors.Is(err, engine.ErrDay):
		writeError(c, http.StatusBadRequest, "invalid memory synthesis request", err.Error())
	case errors.Is(err, engine.ErrAgent):
		writeError(c, http.StatusConflict, "memory synthesis agent unusable", err.Error())
	default:
		writeError(c, http.StatusInternalServerError, "run memory synthesis failed", err.Error())
	}
}

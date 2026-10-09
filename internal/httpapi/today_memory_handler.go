package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/today"
)

// Memory hygiene (design prime-memory-quality §2.6, P4):
//
//	GET  /v1/memory-findings?tracker_id=&all=1   server-side doctor over the tracker mirror
//	GET  /v1/memory-suggestions?state=pending|adopted|dismissed|stale|all
//	POST /v1/memory-suggestions                  {tracker_id,key,action,payload,reason}
//	POST /v1/memory-suggestions/{n}/adopt|dismiss
//
// Proposing is the steward's (or a person's); adopting / dismissing is a person's only.

func (s *Server) handleMemoryFindings(c *rux.Context) {
	if !s.todayReady(c) {
		return
	}
	q := c.Req.URL.Query()
	items, err := s.today.MemoryFindings(today.MemoryFindingsQuery{TrackerID: q.Get("tracker_id"), All: q.Get("all") == "1" || q.Get("all") == "true"})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "memory findings failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"findings": items, "daily_cap": today.MemorySuggestDailyCap})
}

func (s *Server) handleListMemorySuggestions(c *rux.Context) {
	if !s.todayReady(c) {
		return
	}
	items, err := s.today.ListMemorySuggestions(c.Req.URL.Query().Get("state"))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list memory suggestions failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"suggestions": items})
}

func (s *Server) handleAddMemorySuggestion(c *rux.Context) {
	if !s.todayReady(c) {
		return
	}
	jobID := ""
	if jc, ok := jobCallerFromCtx(c); ok {
		if !jc.isSteward() {
			writeError(c, http.StatusForbidden, "job credential may not propose memory cleanup", "only the steward (or a person) proposes memory cleanup")
			return
		}
		jobID = jc.JobID
	} else if callerKindFromCtx(c) != callerKindUser {
		writeError(c, http.StatusForbidden, "memory suggestions require a person or the steward", "")
		return
	}
	var body today.MemorySuggestInput
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	sg, recorded, err := s.today.SuggestMemory(body, workBy(c), jobID)
	if err != nil {
		writeMemorySuggestionError(c, err, "record memory suggestion")
		return
	}
	status := http.StatusCreated
	if !recorded {
		status = http.StatusOK
	}
	c.JSON(status, map[string]any{"suggestion": sg, "recorded": recorded})
}

func (s *Server) handleDecideMemorySuggestion(adopt bool) rux.HandlerFunc {
	what := "dismiss a memory suggestion"
	if adopt {
		what = "adopt a memory suggestion"
	}
	return func(c *rux.Context) {
		if !s.todayReady(c) || !stewardUserOnly(c, what) {
			return
		}
		n, err := strconv.ParseInt(c.Param("n"), 10, 64)
		if err != nil || n <= 0 {
			writeError(c, http.StatusBadRequest, "invalid suggestion id", c.Param("n"))
			return
		}
		var sg today.MemorySuggestion
		if adopt {
			sg, err = s.today.AdoptMemorySuggestion(n, workBy(c))
		} else {
			sg, err = s.today.DismissMemorySuggestion(n, workBy(c))
		}
		if err != nil {
			writeMemorySuggestionError(c, err, what)
			return
		}
		c.JSON(http.StatusOK, map[string]any{"suggestion": sg})
	}
}

func writeMemorySuggestionError(c *rux.Context, err error, what string) {
	switch {
	case errors.Is(err, today.ErrInvalidMemorySuggestion), errors.Is(err, jobstore.ErrWorkInvalid):
		writeError(c, http.StatusBadRequest, what+" failed", err.Error())
	case errors.Is(err, today.ErrMemoryNotFound), errors.Is(err, jobstore.ErrMemorySuggestionNotFound):
		writeError(c, http.StatusNotFound, what+" failed", err.Error())
	case errors.Is(err, today.ErrMemorySuggestCooldown), errors.Is(err, today.ErrMemorySuggestCap),
		errors.Is(err, jobstore.ErrMemorySuggestionDecided), errors.Is(err, jobstore.ErrTrackerConflict):
		writeError(c, http.StatusConflict, what+" failed", err.Error())
	default:
		writeError(c, http.StatusInternalServerError, what+" failed", err.Error())
	}
}

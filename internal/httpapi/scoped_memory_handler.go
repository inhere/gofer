package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gookit/rux/v2"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
)

type scopedMemoryRequest struct {
	Scope    string   `json:"scope"`
	ScopeKey string   `json:"scope_key"`
	Key      string   `json:"key"`
	Content  string   `json:"content"`
	Tags     []string `json:"tags"`
	// Optional memory fields; absent keeps the stored value, "" / {} clears.
	Kind      *string             `json:"kind"`
	Summary   *string             `json:"summary"`
	When      *tracker.MemoryWhen `json:"when"`
	ExpiresAt *string             `json:"expires_at"`
	Source    *string             `json:"source"`
}

func (r scopedMemoryRequest) meta() jobstore.ScopedMemoryMetaPatch {
	return jobstore.ScopedMemoryMetaPatch{Kind: r.Kind, Summary: r.Summary, When: r.When, ExpiresAt: r.ExpiresAt, Source: r.Source}
}

func (s *Server) handleScopedMemories(c *rux.Context) {
	if s.trackerStore == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "memory store unavailable"})
		return
	}
	scope, scopeKey := c.Req.URL.Query().Get("scope"), c.Req.URL.Query().Get("scope_key")
	items, err := s.trackerStore.ListScopedMemories(scope, scopeKey, c.Req.URL.Query().Get("q"), c.Req.URL.Query()["tag"])
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{"memories": items})
}

func (s *Server) handleScopedMemoryCreate(c *rux.Context) {
	if s.trackerStore == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "memory store unavailable"})
		return
	}
	if !scopedMemoryUserWrite(c) {
		return
	}
	var req scopedMemoryRequest
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	item, err := s.trackerStore.PutScopedMemoryPatch(req.Scope, req.ScopeKey, req.Key, req.Content, req.Tags, req.meta(), callerFromCtx(c))
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, item)
}

func (s *Server) handleScopedMemoryGet(c *rux.Context) {
	if s.trackerStore == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "memory store unavailable"})
		return
	}
	item, err := s.trackerStore.GetScopedMemory(c.Param("scope"), scopeKeyParam(c), c.Param("key"))
	if errors.Is(err, jobstore.ErrScopedMemoryNotFound) {
		c.JSON(http.StatusNotFound, map[string]string{"error": "memory not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, item)
}

func (s *Server) handleScopedMemoryPut(c *rux.Context) {
	if s.trackerStore == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "memory store unavailable"})
		return
	}
	if !scopedMemoryUserWrite(c) {
		return
	}
	var req scopedMemoryRequest
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	req.Scope, req.ScopeKey, req.Key = c.Param("scope"), scopeKeyParam(c), c.Param("key")
	item, err := s.trackerStore.PutScopedMemoryPatch(req.Scope, req.ScopeKey, req.Key, req.Content, req.Tags, req.meta(), callerFromCtx(c))
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, item)
}

func (s *Server) handleScopedMemoryDelete(c *rux.Context) {
	if s.trackerStore == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "memory store unavailable"})
		return
	}
	if !scopedMemoryUserWrite(c) {
		return
	}
	err := s.trackerStore.DeleteScopedMemory(c.Param("scope"), scopeKeyParam(c), c.Param("key"), callerFromCtx(c))
	if errors.Is(err, jobstore.ErrScopedMemoryNotFound) {
		c.JSON(http.StatusNotFound, map[string]string{"error": "memory not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "deleted"})
}

type scopedMemoryFlagRequest struct {
	Reason string `json:"reason"`
	Job    string `json:"job"`
}

// handleScopedMemoryFlag records an agent's "this memory no longer matches" report.
// Unlike the other scoped writes a job credential may flag: that is the point of it
// (the agent that hit the stale memory is the one running in the job). The job is
// then the credential's own id, not what the body claims.
func (s *Server) handleScopedMemoryFlag(c *rux.Context) {
	if s.trackerStore == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "memory store unavailable"})
		return
	}
	var req scopedMemoryFlagRequest
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if callerKindFromCtx(c) == callerKindJob {
		req.Job = callerFromCtx(c)
		// Ownership: a job flags the global memories and its own project's, the
		// ones its prime injected — not another project's.
		if c.Param("scope") == jobstore.ScopedMemoryProject {
			asking, ok := s.jobs.Get(req.Job)
			if !ok || asking.ProjectKey != scopeKeyParam(c) {
				c.JSON(http.StatusForbidden, map[string]string{"error": "job caller may only flag global or its own project's memories"})
				return
			}
		}
	}
	flag, err := tracker.NewMemoryFlag(req.Reason, callerFromCtx(c), req.Job, time.Now())
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.writeScopedMemoryFlag(c, &flag)
}

// handleScopedMemoryUnflag clears the flags of a scoped memory: the human review, so
// a job credential may not.
func (s *Server) handleScopedMemoryUnflag(c *rux.Context) {
	if s.trackerStore == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "memory store unavailable"})
		return
	}
	if !scopedMemoryUserWrite(c) {
		return
	}
	s.writeScopedMemoryFlag(c, nil)
}

func (s *Server) writeScopedMemoryFlag(c *rux.Context, flag *tracker.MemoryFlag) {
	item, err := s.trackerStore.FlagScopedMemory(c.Param("scope"), scopeKeyParam(c), c.Param("key"), flag)
	if errors.Is(err, jobstore.ErrScopedMemoryNotFound) {
		c.JSON(http.StatusNotFound, map[string]string{"error": "memory not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, item)
}

func scopeKeyParam(c *rux.Context) string {
	if c.Param("scope") == jobstore.ScopedMemoryGlobal {
		return ""
	}
	return strings.TrimSpace(c.Param("scope_key"))
}

func scopedMemoryUserWrite(c *rux.Context) bool {
	if callerKindFromCtx(c) == callerKindJob {
		c.JSON(http.StatusForbidden, map[string]string{"error": "job caller may only read scoped memories"})
		return false
	}
	return true
}

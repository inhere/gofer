package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/gookit/rux/v2"
	"github.com/inhere/gofer/internal/jobstore"
)

type trackerSyncRequest struct {
	TrackerID   string              `json:"tracker_id"`
	ProjectKey  string              `json:"project_key"`
	RelPath     string              `json:"rel_path"`
	Prefix      string              `json:"prefix"`
	Issue       []trackerRecordBody `json:"issues"`
	Memory      []trackerRecordBody `json:"memories"`
	IssueSince  int64               `json:"issue_since"`
	MemorySince int64               `json:"memory_since"`
}

type trackerRecordBody struct {
	ID        string          `json:"id"`
	Body      json.RawMessage `json:"body"`
	Rev       int64           `json:"rev"`
	UpdatedAt string          `json:"updated_at"`
	Deleted   bool            `json:"deleted"`
	DeletedAt string          `json:"deleted_at"`
	DeletedBy string          `json:"deleted_by"`
}

func (s *Server) handleTrackerSync(c *rux.Context) {
	if s.trackerStore == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "tracker mirror unavailable"})
		return
	}
	var req trackerSyncRequest
	if err := c.BindJSON(&req); err != nil || req.TrackerID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "tracker_id required"})
		return
	}
	if callerFromCtx(c) == "" && c.Req.Header.Get("Authorization") == "" {
		c.JSON(http.StatusForbidden, map[string]string{"error": "authenticated caller required"})
		return
	}
	if err := s.trackerStore.UpsertTrackerRepo(jobstore.TrackerRepo{TrackerID: req.TrackerID, ProjectKey: req.ProjectKey, RelPath: req.RelPath, Prefix: req.Prefix}); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for _, item := range req.Issue {
		if err := s.trackerStore.UpsertTrackerIssue(jobstore.TrackerRecord{TrackerID: req.TrackerID, ID: item.ID, Body: item.Body, Rev: item.Rev, UpdatedAt: item.UpdatedAt}); err != nil {
			c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	for _, item := range req.Memory {
		if err := s.trackerStore.UpsertTrackerMemory(jobstore.TrackerRecord{TrackerID: req.TrackerID, ID: item.ID, Body: item.Body, Rev: item.Rev, UpdatedAt: item.UpdatedAt, Deleted: item.Deleted, DeletedAt: item.DeletedAt, DeletedBy: item.DeletedBy}); err != nil {
			c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	issues, err := s.trackerStore.ListTrackerIssues(req.TrackerID, req.IssueSince)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	memories, err := s.trackerStore.ListTrackerMemories(req.TrackerID, req.MemorySince)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{"issues": issues, "memories": memories})
}

func (s *Server) handleTrackerIssues(c *rux.Context) {
	if s.trackerStore == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "tracker mirror unavailable"})
		return
	}
	id := c.Req.URL.Query().Get("tracker_id")
	if id == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "tracker_id required"})
		return
	}
	items, err := s.trackerStore.ListTrackerIssues(id, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{"issues": items})
}

func (s *Server) handleTrackerMemories(c *rux.Context) {
	if s.trackerStore == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "tracker mirror unavailable"})
		return
	}
	id := c.Req.URL.Query().Get("tracker_id")
	if id == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "tracker_id required"})
		return
	}
	items, err := s.trackerStore.ListTrackerMemories(id, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{"memories": items})
}

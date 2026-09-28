package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gookit/rux/v2"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
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

func (s *Server) linkIssueJob(req job.JobRequest, event tracker.JobIssueEvent) {
	if s.trackerStore == nil || req.TrackerID == "" || req.IssueID == "" {
		return
	}
	items, err := s.trackerStore.ListTrackerIssues(req.TrackerID, 0)
	if err != nil {
		return
	}
	for _, item := range items {
		if item.ID != req.IssueID {
			continue
		}
		var issue tracker.Issue
		if json.Unmarshal(item.Body, &issue) != nil {
			return
		}
		if event.Phase == "started" {
			issue.Status = "in_progress"
		}
		issue = tracker.LinkIssueToJob(issue, event)
		body, _ := json.Marshal(issue)
		_ = s.trackerStore.UpsertTrackerIssue(jobstore.TrackerRecord{TrackerID: req.TrackerID, ID: req.IssueID, Body: body, Rev: item.Rev + 1, UpdatedAt: issue.UpdatedAt})
		return
	}
}

func (s *Server) trackerIssueExists(trackerID, issueID string) bool {
	if s.trackerStore == nil || trackerID == "" || issueID == "" {
		return false
	}
	items, err := s.trackerStore.ListTrackerIssues(trackerID, 0)
	if err != nil {
		return false
	}
	for _, item := range items {
		if item.ID == issueID {
			return true
		}
	}
	return false
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

func (s *Server) handleTrackerIssueEdit(c *rux.Context) {
	if s.trackerStore == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "tracker mirror unavailable"})
		return
	}
	id := c.Req.URL.Query().Get("tracker_id")
	if id == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "tracker_id required"})
		return
	}
	var body json.RawMessage
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if err := s.trackerStore.UpsertTrackerIssue(jobstore.TrackerRecord{TrackerID: id, ID: c.Param("id"), Body: body, Rev: 2, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleTrackerMemoryEdit(c *rux.Context) {
	if s.trackerStore == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "tracker mirror unavailable"})
		return
	}
	id := c.Req.URL.Query().Get("tracker_id")
	if id == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "tracker_id required"})
		return
	}
	var body json.RawMessage
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if err := s.trackerStore.UpsertTrackerMemory(jobstore.TrackerRecord{TrackerID: id, ID: c.Param("id"), Body: body, Rev: 2, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleTrackerMemoryDelete(c *rux.Context) {
	if s.trackerStore == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "tracker mirror unavailable"})
		return
	}
	id := c.Req.URL.Query().Get("tracker_id")
	if id == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "tracker_id required"})
		return
	}
	if err := s.trackerStore.UpsertTrackerMemory(jobstore.TrackerRecord{TrackerID: id, ID: c.Param("id"), Body: json.RawMessage("{}"), Rev: 2, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Deleted: true, DeletedAt: time.Now().UTC().Format(time.RFC3339Nano), DeletedBy: callerFromCtx(c)}); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "deleted"})
}

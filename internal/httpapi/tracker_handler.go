package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
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
		rev := item.Rev
		skip := false
		if existing, _ := s.trackerStore.ListTrackerIssues(req.TrackerID, 0); true {
			for _, old := range existing {
				if old.ID == item.ID {
					if old.Rev > rev {
						skip = true
					}
					if old.Rev == rev {
						rev = old.Rev + 1
					}
				}
			}
		}
		if skip {
			continue
		}
		if err := s.trackerStore.UpsertTrackerIssue(jobstore.TrackerRecord{TrackerID: req.TrackerID, ID: item.ID, Body: item.Body, Rev: rev, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
			c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	for _, item := range req.Memory {
		rev := item.Rev
		skip := false
		if existing, _ := s.trackerStore.ListTrackerMemories(req.TrackerID, 0); true {
			for _, old := range existing {
				if old.ID == item.ID {
					if old.Rev > rev {
						skip = true
					}
					if old.Rev == rev {
						rev = old.Rev + 1
					}
				}
			}
		}
		if skip {
			continue
		}
		if err := s.trackerStore.UpsertTrackerMemory(jobstore.TrackerRecord{TrackerID: req.TrackerID, ID: item.ID, Body: item.Body, Rev: rev, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Deleted: item.Deleted, DeletedAt: item.DeletedAt, DeletedBy: item.DeletedBy}); err != nil {
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
	var issueCursor, memoryCursor int64
	for _, item := range issues {
		if item.Rev > issueCursor {
			issueCursor = item.Rev
		}
	}
	for _, item := range memories {
		if item.Rev > memoryCursor {
			memoryCursor = item.Rev
		}
	}
	if issueCursor == 0 {
		all, _ := s.trackerStore.ListTrackerIssues(req.TrackerID, 0)
		for _, item := range all {
			if item.Rev > issueCursor {
				issueCursor = item.Rev
			}
		}
	}
	if memoryCursor == 0 {
		all, _ := s.trackerStore.ListTrackerMemories(req.TrackerID, 0)
		for _, item := range all {
			if item.Rev > memoryCursor {
				memoryCursor = item.Rev
			}
		}
	}
	c.JSON(http.StatusOK, map[string]any{"issues": issues, "memories": memories, "issue_cursor": issueCursor, "memory_cursor": memoryCursor})
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
	status, typ, q := c.Req.URL.Query().Get("status"), c.Req.URL.Query().Get("type"), strings.ToLower(c.Req.URL.Query().Get("q"))
	tags := c.Req.URL.Query()["tag"]
	filtered := items[:0]
	for _, item := range items {
		var issue tracker.Issue
		if json.Unmarshal(item.Body, &issue) != nil {
			continue
		}
		if status != "" && issue.Status != status || typ != "" && issue.Type != typ {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(issue.Title+" "+issue.Description), q) {
			continue
		}
		ok := true
		for _, want := range tags {
			found := false
			for _, tag := range issue.Tags {
				if tag == want {
					found = true
				}
			}
			if !found {
				ok = false
			}
		}
		if ok {
			filtered = append(filtered, item)
		}
	}
	c.JSON(http.StatusOK, map[string]any{"issues": filtered})
}

func (s *Server) handleTrackerIssueGet(c *rux.Context) {
	id := c.Req.URL.Query().Get("tracker_id")
	if id == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "tracker_id required"})
		return
	}
	items, err := s.trackerStore.ListTrackerIssues(id, 0)
	if err != nil {
		c.JSON(500, map[string]string{"error": err.Error()})
		return
	}
	for _, item := range items {
		if item.ID == c.Param("id") {
			var issue tracker.Issue
			_ = json.Unmarshal(item.Body, &issue)
			c.JSON(http.StatusOK, issue)
			return
		}
	}
	c.JSON(http.StatusNotFound, map[string]string{"error": "issue not found"})
}

func (s *Server) handleTrackerIssueComment(c *rux.Context) {
	id := c.Req.URL.Query().Get("tracker_id")
	if id == "" {
		c.JSON(400, map[string]string{"error": "tracker_id required"})
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if c.BindJSON(&in) != nil || strings.TrimSpace(in.Body) == "" {
		c.JSON(400, map[string]string{"error": "body required"})
		return
	}
	items, _ := s.trackerStore.ListTrackerIssues(id, 0)
	for _, item := range items {
		if item.ID == c.Param("id") {
			var issue tracker.Issue
			_ = json.Unmarshal(item.Body, &issue)
			issue.Comments = append(issue.Comments, tracker.Comment{At: tracker.Now(), By: callerFromCtx(c), Text: in.Body})
			b, _ := json.Marshal(issue)
			_ = s.trackerStore.UpsertTrackerIssue(jobstore.TrackerRecord{TrackerID: id, ID: item.ID, Body: b, Rev: item.Rev + 1, UpdatedAt: tracker.Now()})
			c.JSON(http.StatusOK, issue)
			return
		}
	}
	c.JSON(404, map[string]string{"error": "issue not found"})
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
	rev := int64(1)
	if old, _ := s.trackerStore.ListTrackerMemories(id, 0); true {
		for _, item := range old {
			if item.Rev >= rev {
				rev = item.Rev + 1
			}
		}
	}
	if err := s.trackerStore.UpsertTrackerMemory(jobstore.TrackerRecord{TrackerID: id, ID: c.Param("id"), Body: json.RawMessage("{}"), Rev: rev, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Deleted: true, DeletedAt: time.Now().UTC().Format(time.RFC3339Nano), DeletedBy: callerFromCtx(c)}); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "deleted"})
}

package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	SyncSummary string              `json:"sync_summary"`
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
		if err := s.trackerStore.UpsertTrackerIssue(jobstore.TrackerRecord{TrackerID: req.TrackerID, ID: req.IssueID, Body: body, Rev: item.Rev + 1, UpdatedAt: issue.UpdatedAt}); err != nil {
			slog.Error("tracker issue start link failed", "issue_id", req.IssueID, "error", err)
		}
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

func (s *Server) LinkIssueStarted(snap job.JobResult) {
	if s.trackerStore == nil || snap.TrackerID == "" || snap.IssueID == "" {
		return
	}
	items, _ := s.trackerStore.ListTrackerIssues(snap.TrackerID, 0)
	for _, item := range items {
		if item.ID != snap.IssueID {
			continue
		}
		var issue tracker.Issue
		if json.Unmarshal(item.Body, &issue) != nil {
			return
		}
		issue.Status = "in_progress"
		issue.UpdatedAt = tracker.Now()
		body, _ := json.Marshal(issue)
		if err := s.trackerStore.UpsertTrackerIssue(jobstore.TrackerRecord{TrackerID: snap.TrackerID, ID: snap.IssueID, Body: body, Rev: item.Rev + 1, UpdatedAt: issue.UpdatedAt}); err != nil {
			slog.Error("tracker issue finish link failed", "issue_id", snap.IssueID, "error", err)
		}
		return
	}
}

func (s *Server) LinkIssueFinished(snap job.JobResult) {
	if s.trackerStore == nil || snap.TrackerID == "" || snap.IssueID == "" {
		return
	}
	items, _ := s.trackerStore.ListTrackerIssues(snap.TrackerID, 0)
	for _, item := range items {
		if item.ID != snap.IssueID {
			continue
		}
		var issue tracker.Issue
		if json.Unmarshal(item.Body, &issue) != nil {
			return
		}
		commits := make([]string, 0, len(snap.Commits))
		for _, c := range snap.Commits {
			commits = append(commits, c.SHA)
		}
		issue = tracker.LinkIssueToJob(issue, tracker.JobIssueEvent{JobID: snap.ID, Phase: "finished", Status: snap.Status, At: tracker.Now(), Commits: commits, Uncommitted: snap.UncommittedFiles})
		body, _ := json.Marshal(issue)
		if err := s.trackerStore.UpsertTrackerIssue(jobstore.TrackerRecord{TrackerID: snap.TrackerID, ID: snap.IssueID, Body: body, Rev: item.Rev + 1, UpdatedAt: issue.UpdatedAt}); err != nil {
			slog.Error("tracker issue finish link failed", "issue_id", snap.IssueID, "error", err)
		}
		return
	}
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
		if item.ChangedSeq > issueCursor {
			issueCursor = item.ChangedSeq
		}
	}
	for _, item := range memories {
		if item.ChangedSeq > memoryCursor {
			memoryCursor = item.ChangedSeq
		}
	}
	if issueCursor == 0 {
		all, _ := s.trackerStore.ListTrackerIssues(req.TrackerID, 0)
		for _, item := range all {
			if item.ChangedSeq > issueCursor {
				issueCursor = item.ChangedSeq
			}
		}
	}
	if memoryCursor == 0 {
		all, _ := s.trackerStore.ListTrackerMemories(req.TrackerID, 0)
		for _, item := range all {
			if item.ChangedSeq > memoryCursor {
				memoryCursor = item.ChangedSeq
			}
		}
	}
	relPath, projectKey, prefix := req.RelPath, req.ProjectKey, req.Prefix
	if repos, listErr := s.trackerStore.ListTrackerRepos(); listErr == nil {
		for _, old := range repos {
			if old.TrackerID == req.TrackerID {
				if relPath == "" {
					relPath = old.RelPath
				}
				if projectKey == "" {
					projectKey = old.ProjectKey
				}
				if prefix == "" {
					prefix = old.Prefix
				}
				break
			}
		}
	}
	summary := req.SyncSummary
	if summary == "" {
		summary = fmt.Sprintf("同步完成：issues=%d memories=%d", len(issues), len(memories))
	}
	if err := s.trackerStore.UpsertTrackerRepo(jobstore.TrackerRepo{TrackerID: req.TrackerID, ProjectKey: projectKey, RelPath: relPath, Prefix: prefix, LastSyncAt: time.Now().Unix(), SyncSummary: summary}); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
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

func (s *Server) handleTrackerRepos(c *rux.Context) {
	if s.trackerStore == nil {
		c.JSON(503, map[string]string{"error": "tracker mirror unavailable"})
		return
	}
	repos, err := s.trackerStore.ListTrackerRepos()
	if err != nil {
		c.JSON(500, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{"repos": repos})
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
		Text string `json:"text"`
	}
	if c.BindJSON(&in) != nil || strings.TrimSpace(in.Text) == "" {
		c.JSON(400, map[string]string{"error": "text required"})
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
			if err := json.Unmarshal(item.Body, &issue); err != nil {
				c.JSON(500, map[string]string{"error": err.Error()})
				return
			}
			issue.Comments = append(issue.Comments, tracker.Comment{At: tracker.Now(), By: callerFromCtx(c), Text: in.Text})
			comments, _ := json.Marshal(issue.Comments)
			now := tracker.Now()
			if _, err := s.trackerStore.PatchTrackerIssue(id, item.ID, item.Rev, map[string]json.RawMessage{"comments": comments}, now, callerFromCtx(c)); err != nil {
				if errors.Is(err, jobstore.ErrTrackerConflict) {
					c.JSON(409, map[string]string{"error": err.Error()})
					return
				}
				c.JSON(500, map[string]string{"error": err.Error()})
				return
			}
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
	var body map[string]json.RawMessage
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	expected := int64(0)
	if raw := body["expected_rev"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &expected)
		delete(body, "expected_rev")
	}
	if raw := body["status"]; len(raw) > 0 {
		var v string
		_ = json.Unmarshal(raw, &v)
		if !tracker.ValidStatus(v) {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid status"})
			return
		}
	}
	if raw := body["priority"]; len(raw) > 0 {
		var v int
		if json.Unmarshal(raw, &v) != nil || v < 0 || v > 4 {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid priority"})
			return
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.trackerStore.PatchTrackerIssue(id, c.Param("id"), expected, body, now, callerFromCtx(c)); err != nil {
		if errors.Is(err, jobstore.ErrTrackerConflict) {
			c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
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
	var body map[string]json.RawMessage
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	expected := int64(0)
	if raw := body["expected_rev"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &expected)
		delete(body, "expected_rev")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.trackerStore.PatchTrackerMemory(id, c.Param("id"), expected, body, now, callerFromCtx(c)); err != nil {
		if errors.Is(err, jobstore.ErrTrackerConflict) {
			c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
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

package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/tracker"
)

// Read-only issue views over the server's tracker mirror (X2): what the steward's
// gofer_issue_list / gofer_issue_get call. They filter by project / repo across every
// mirrored repository; nothing here writes.

// issueBrief is one row of GET /v1/issues.
type issueBrief struct {
	TrackerID  string   `json:"tracker_id"`
	ProjectKey string   `json:"project_key,omitempty"`
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Type       string   `json:"type,omitempty"`
	Status     string   `json:"status"`
	Priority   int      `json:"priority"`
	Assignee   string   `json:"assignee,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	UpdatedAt  string   `json:"updated_at,omitempty"`
}

const (
	issueListDefault = 50
	issueListMax     = 200
)

// GET /v1/issues?project=&tracker_id=&repo=&status=&type=&tag=&q=&limit=
func (s *Server) handleIssueList(c *rux.Context) {
	if s.trackerStore == nil {
		writeError(c, http.StatusServiceUnavailable, "tracker mirror unavailable", "no tracker mirror on this server")
		return
	}
	repos, err := s.trackerStore.ListTrackerRepos()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list issues failed", err.Error())
		return
	}
	project, trackerID, repo := c.Query("project"), c.Query("tracker_id"), c.Query("repo")
	status, typ, q := c.Query("status"), c.Query("type"), strings.ToLower(strings.TrimSpace(c.Query("q")))
	tags := c.Req.URL.Query()["tag"]
	limit := issueListDefault
	if n, perr := strconv.Atoi(c.Query("limit")); perr == nil && n > 0 {
		limit = n
	}
	if limit > issueListMax {
		limit = issueListMax
	}
	out := []issueBrief{}
	total := 0
	for _, r := range repos {
		if project != "" && r.ProjectKey != project || trackerID != "" && r.TrackerID != trackerID || repo != "" && r.RelPath != repo {
			continue
		}
		rows, lerr := s.trackerStore.ListTrackerIssues(r.TrackerID, 0)
		if lerr != nil {
			writeError(c, http.StatusInternalServerError, "list issues failed", lerr.Error())
			return
		}
		for _, row := range rows {
			var is tracker.Issue
			if json.Unmarshal(row.Body, &is) != nil || !issueMatches(is, status, typ, q, tags) {
				continue
			}
			total++
			if len(out) < limit {
				out = append(out, issueBrief{
					TrackerID: r.TrackerID, ProjectKey: r.ProjectKey, ID: is.ID, Title: is.Title, Type: is.Type,
					Status: is.Status, Priority: is.Priority, Assignee: is.Assignee, Tags: is.Tags, UpdatedAt: is.UpdatedAt,
				})
			}
		}
	}
	c.JSON(http.StatusOK, map[string]any{"issues": out, "total": total, "truncated": total > len(out)})
}

func issueMatches(is tracker.Issue, status, typ, q string, tags []string) bool {
	if status != "" && is.Status != status || typ != "" && is.Type != typ {
		return false
	}
	if q != "" && !strings.Contains(strings.ToLower(is.ID+" "+is.Title+" "+is.Description), q) {
		return false
	}
	for _, want := range tags {
		found := false
		for _, t := range is.Tags {
			if t == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// GET /v1/issues/{id}?tracker_id= — one issue in full. An id that exists in several
// repos needs tracker_id (409 lists them).
func (s *Server) handleIssueGet(c *rux.Context) {
	if s.trackerStore == nil {
		writeError(c, http.StatusServiceUnavailable, "tracker mirror unavailable", "no tracker mirror on this server")
		return
	}
	rows, err := s.trackerStore.FindTrackerIssues(c.Param("id"))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get issue failed", err.Error())
		return
	}
	if want := c.Query("tracker_id"); want != "" {
		kept := rows[:0]
		for _, r := range rows {
			if r.TrackerID == want {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	switch len(rows) {
	case 0:
		writeError(c, http.StatusNotFound, "get issue failed", "issue not found")
	case 1:
		var is tracker.Issue
		if json.Unmarshal(rows[0].Body, &is) != nil {
			writeError(c, http.StatusInternalServerError, "get issue failed", "unreadable issue body")
			return
		}
		c.JSON(http.StatusOK, map[string]any{"tracker_id": rows[0].TrackerID, "issue": is})
	default:
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.TrackerID)
		}
		writeError(c, http.StatusConflict, "get issue failed", "issue id exists in several repos; pass tracker_id: "+strings.Join(ids, ", "))
	}
}

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
)

// trackerIssueCreateReq is POST /v1/tracker/repos/{tracker_id}/issues.
type trackerIssueCreateReq struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Priority    *int     `json:"priority"` // 0..4, default 2
	Labels      []string `json:"labels"`
	Type        string   `json:"type"` // default task
}

const maxTrackerIssueTitle = 300

// normalizeIssueInput validates the user-supplied issue fields shared by the create route
// and the capture box's to=issue dispatch; the returned message is a 400 detail.
func normalizeIssueInput(in trackerIssueCreateReq) (tracker.Issue, string) {
	title := strings.Join(strings.Fields(in.Title), " ")
	if title == "" {
		return tracker.Issue{}, "title required"
	}
	if len([]rune(title)) > maxTrackerIssueTitle {
		return tracker.Issue{}, "title too long"
	}
	priority := 2
	if in.Priority != nil {
		priority = *in.Priority
	}
	if priority < 0 || priority > 4 {
		return tracker.Issue{}, "priority must be 0..4"
	}
	typ := strings.ToLower(strings.TrimSpace(in.Type))
	if typ == "" {
		typ = "task"
	}
	var tags []string
	seen := map[string]bool{}
	for _, l := range in.Labels {
		if l = strings.TrimSpace(l); l != "" && !seen[l] {
			seen[l] = true
			tags = append(tags, l)
		}
	}
	return tracker.Issue{Title: title, Description: strings.TrimSpace(in.Description), Priority: priority, Type: typ, Status: "open", Tags: tags}, ""
}

// writeTrackerCreateError maps the store's create errors to HTTP.
func writeTrackerCreateError(c *rux.Context, err error) {
	switch {
	case errors.Is(err, jobstore.ErrTrackerNoPrefix):
		writeError(c, http.StatusConflict, "tracker has no issue id prefix",
			"run `gofer repo sync` in the repository once so the server learns its prefix, then retry")
	case errors.Is(err, jobstore.ErrTrackerRepoUnknown):
		writeError(c, http.StatusNotFound, "unknown tracker repository", err.Error())
	case errors.Is(err, jobstore.ErrReservedTracker):
		writeError(c, http.StatusBadRequest, "the global memory tracker has no issues", "")
	default:
		writeError(c, http.StatusInternalServerError, "create issue", err.Error())
	}
}

// POST /v1/tracker/repos/{tracker_id}/issues {title, description?, priority?, labels?,
// type?}: file a new issue into the repository's server mirror. The id is allocated from
// the mirror's issues and the repository prefix; the record is flagged server_created
// (「待同步」) until a repository sync pulls it, and a repository that meanwhile pushes a
// different new issue under the same id makes the server's one take a fresh id. 409 when
// the repository has no prefix recorded yet. Worker tokens and job credentials are refused
// (SEC-01 default-denies a job's writes; the worker whitelist has no such route).
func (s *Server) handleTrackerIssueCreate(c *rux.Context) {
	if s.trackerStore == nil {
		writeError(c, http.StatusServiceUnavailable, "tracker mirror unavailable", "")
		return
	}
	if callerKindFromCtx(c) == callerKindWorker {
		writeError(c, http.StatusForbidden, "not permitted for this caller", "worker tokens cannot create issues")
		return
	}
	var in trackerIssueCreateReq
	if err := c.BindJSON(&in); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	issue, msg := normalizeIssueInput(in)
	if msg != "" {
		writeError(c, http.StatusBadRequest, msg, "")
		return
	}
	rec, err := s.trackerStore.CreateServerTrackerIssue(c.Param("tracker_id"), issue, callerFromCtx(c))
	if err != nil {
		writeTrackerCreateError(c, err)
		return
	}
	_ = json.Unmarshal(rec.Body, &issue)
	c.JSON(http.StatusCreated, map[string]any{"tracker_id": rec.TrackerID, "issue": issue, "rev": rec.Rev, "server_created": true})
}

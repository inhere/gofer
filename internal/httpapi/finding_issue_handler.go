package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
)

// findingIssueTag marks issues created from a job's out-of-scope findings.
const findingIssueTag = "discovered"

// findingIssueReq is POST /v1/jobs/{id}/findings/issue.
type findingIssueReq struct {
	Text     string `json:"text"`
	Priority *int   `json:"priority,omitempty"` // 0..4, default 2
}

// handleFindingIssue turns one of a job's out-of-scope findings into a tracker issue in
// the server's mirror of the job's repository; the repository gets it on its next
// `gofer repo sync`. The tracker is the job's own (tracker_id) or, failing that, the
// only tracker registered for the job's project. Asking twice for the same finding of
// the same job returns the issue already created. A person's action: worker and job
// credentials are refused.
func (s *Server) handleFindingIssue(c *rux.Context) {
	if callerKindFromCtx(c) == callerKindWorker {
		writeError(c, http.StatusForbidden, "not permitted for this caller", "worker tokens cannot create issues")
		return
	}
	if _, isJob := jobCallerFromCtx(c); isJob {
		writeError(c, http.StatusForbidden, "not permitted for this caller", "a job cannot file its own findings; the reviewer does")
		return
	}
	if s.trackerStore == nil || s.jobs == nil {
		writeError(c, http.StatusServiceUnavailable, "tracker mirror unavailable", "")
		return
	}
	var in findingIssueReq
	if err := c.BindJSON(&in); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	text := strings.Join(strings.Fields(in.Text), " ")
	if text == "" {
		writeError(c, http.StatusBadRequest, "text required", "")
		return
	}
	priority := 2
	if in.Priority != nil {
		priority = *in.Priority
	}
	if priority < 0 || priority > 4 {
		writeError(c, http.StatusBadRequest, "invalid priority", "priority must be 0..4")
		return
	}
	jobID := c.Param("id")
	snap, ok := s.jobs.Get(jobID)
	if !ok {
		writeError(c, http.StatusNotFound, "job not found", jobID)
		return
	}
	repo, ok, detail := s.findingTracker(snap.TrackerID, snap.ProjectKey)
	if !ok {
		writeError(c, http.StatusUnprocessableEntity, "no tracker for this job", detail)
		return
	}
	items, err := s.trackerStore.ListTrackerIssues(repo.TrackerID, 0)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "read tracker", err.Error())
		return
	}
	desc := "discovered in job " + jobID
	for _, item := range items {
		var issue tracker.Issue
		if json.Unmarshal(item.Body, &issue) != nil {
			continue
		}
		if issue.ID == "" {
			issue.ID = item.ID
		}
		if issue.Title == text && issue.Description == desc {
			c.JSON(http.StatusOK, map[string]any{"tracker_id": repo.TrackerID, "issue": issue, "created": false})
			return
		}
	}
	issue := tracker.Issue{
		Title: text, Type: "task", Status: "open", Priority: priority,
		Description: desc, Tags: []string{findingIssueTag},
	}
	rec, err := s.trackerStore.CreateServerTrackerIssue(repo.TrackerID, issue, callerFromCtx(c))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "create issue", err.Error())
		return
	}
	_ = json.Unmarshal(rec.Body, &issue)
	c.JSON(http.StatusCreated, map[string]any{"tracker_id": repo.TrackerID, "issue": issue, "created": true})
}

// findingTracker picks the tracker a job's findings go to: the job's own, else the
// single tracker registered for its project. ok=false carries why.
func (s *Server) findingTracker(trackerID, project string) (jobstore.TrackerRepo, bool, string) {
	repos, err := s.trackerStore.ListTrackerRepos()
	if err != nil {
		return jobstore.TrackerRepo{}, false, err.Error()
	}
	if trackerID != "" {
		for _, r := range repos {
			if r.TrackerID == trackerID || tracker.ShortTrackerID(r.TrackerID) == trackerID {
				if r.Prefix == "" {
					return r, false, "tracker " + r.TrackerID + " has no issue id prefix recorded yet (run gofer repo sync in its repository)"
				}
				return r, true, ""
			}
		}
	}
	var match []jobstore.TrackerRepo
	for _, r := range repos {
		if r.ProjectKey == project && r.Prefix != "" {
			match = append(match, r)
		}
	}
	switch len(match) {
	case 1:
		return match[0], true, ""
	case 0:
		return jobstore.TrackerRepo{}, false, "project " + project + " has no tracker synced to this server (run gofer repo sync in its repository)"
	default:
		return jobstore.TrackerRepo{}, false, "project " + project + " has several trackers; create the issue in the right repository with gofer issue create"
	}
}

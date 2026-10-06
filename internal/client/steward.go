package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/steward"
	"github.com/inhere/gofer/internal/work"
)

// W2b steward client: the CLI (`gofer steward ...`) and the steward's own MCP server call
// these. The shapes are the steward package's, shared with the server.

// StewardSettings is the effective steward: block (GET /v1/steward → settings).
type StewardSettings struct {
	Enabled            bool   `json:"enabled"`
	Agent              string `json:"agent"`
	Project            string `json:"project"`
	ReviewTime         string `json:"review_time"`
	IdleEndMin         int    `json:"idle_end_min"`
	ReviewMaxItems     int    `json:"review_max_items"`
	EventWake          bool   `json:"event_wake"`
	EventThrottleMin   int    `json:"event_throttle_min"`
	ReviewTimeExplicit bool   `json:"review_time_explicit"`
}

// StewardState is GET /v1/steward.
type StewardState struct {
	Status   steward.Status  `json:"status"`
	Settings StewardSettings `json:"settings"`
}

// StewardNotesResp is GET|PUT /v1/steward/notes.
type StewardNotesResp struct {
	Notes steward.Notes     `json:"notes"`
	Info  steward.NotesInfo `json:"info"`
}

// ErrStewardNotesConflict is returned by PutStewardNotes when the version moved; Current
// carries the latest notes.
type ErrStewardNotesConflict struct{ Current steward.Notes }

func (e *ErrStewardNotesConflict) Error() string {
	return fmt.Sprintf("steward notes changed (current version %d): read the latest and retry", e.Current.Version)
}

// StewardStatus reads the steward's status and settings.
func (c *Client) StewardStatus() (StewardState, error) {
	var out StewardState
	err := c.doJSON(http.MethodGet, "/v1/steward", nil, &out)
	return out, err
}

// StewardStart opens the steward session when none lives.
func (c *Client) StewardStart() (steward.StartResult, error) {
	var out steward.StartResult
	err := c.doJSON(http.MethodPost, "/v1/steward/start", nil, &out)
	return out, err
}

// StewardRestart ends the session and opens a fresh one from the prime.
func (c *Client) StewardRestart() (steward.StartResult, error) {
	var out steward.StartResult
	err := c.doJSON(http.MethodPost, "/v1/steward/restart", nil, &out)
	return out, err
}

// StewardStop ends the live session; stopped=false when none lived.
func (c *Client) StewardStop() (bool, error) {
	var out struct {
		Stopped bool `json:"stopped"`
	}
	err := c.doJSON(http.MethodPost, "/v1/steward/stop", nil, &out)
	return out.Stopped, err
}

// StewardAsk puts a question to the steward (starting it when needed).
func (c *Client) StewardAsk(text string) (steward.AskResult, error) {
	body, err := jsonBody(map[string]any{"text": text})
	if err != nil {
		return steward.AskResult{}, err
	}
	var out steward.AskResult
	err = c.doJSON(http.MethodPost, "/v1/steward/ask", body, &out)
	return out, err
}

// StewardReview runs a manual review now (force = even when nothing changed).
func (c *Client) StewardReview(force bool) (steward.ReviewResult, error) {
	body, err := jsonBody(map[string]any{"force": force})
	if err != nil {
		return steward.ReviewResult{}, err
	}
	var out steward.ReviewResult
	err = c.doJSON(http.MethodPost, "/v1/steward/review", body, &out)
	return out, err
}

// StewardNotes reads the latest notes (version 0) or one historical version.
func (c *Client) StewardNotes(version int) (StewardNotesResp, error) {
	path := "/v1/steward/notes"
	if version > 0 {
		path += "?version=" + strconv.Itoa(version)
	}
	var out StewardNotesResp
	err := c.doJSON(http.MethodGet, path, nil, &out)
	return out, err
}

// StewardNotesHistory lists every version, newest first.
func (c *Client) StewardNotesHistory() ([]steward.Notes, error) {
	var out struct {
		History []steward.Notes `json:"history"`
	}
	err := c.doJSON(http.MethodGet, "/v1/steward/notes?history=1", nil, &out)
	return out.History, err
}

// PutStewardNotes writes a new notes version when version is the current one (0 = the
// first write). A moved version returns *ErrStewardNotesConflict.
func (c *Client) PutStewardNotes(body string, version int) (StewardNotesResp, error) {
	rb, err := jsonBody(map[string]any{"body": body, "version": version})
	if err != nil {
		return StewardNotesResp{}, err
	}
	resp, err := c.do(http.MethodPut, "/v1/steward/notes", rb)
	if err != nil {
		return StewardNotesResp{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return StewardNotesResp{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode == http.StatusConflict {
		var conf struct {
			Current steward.Notes `json:"current"`
		}
		if json.Unmarshal(data, &conf) == nil {
			return StewardNotesResp{}, &ErrStewardNotesConflict{Current: conf.Current}
		}
	}
	if err := errorFor(resp.StatusCode, data); err != nil {
		return StewardNotesResp{}, err
	}
	var out StewardNotesResp
	if err := json.Unmarshal(data, &out); err != nil {
		return StewardNotesResp{}, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// StewardReviewSummary stores the review's point of view (the digest's steward comment).
func (c *Client) StewardReviewSummary(text string) error {
	body, err := jsonBody(map[string]any{"text": text})
	if err != nil {
		return err
	}
	return c.doJSON(http.MethodPost, "/v1/steward/review-summary", body, nil)
}

// SessionTail reads the read-only tail of a session's transcript.
func (c *Client) SessionTail(sid string, bytes int64) (work.SessionTailResult, error) {
	path := "/v1/sessions/" + url.PathEscape(sid) + "/tail"
	if bytes > 0 {
		path += "?bytes=" + strconv.FormatInt(bytes, 10)
	}
	var out work.SessionTailResult
	err := c.doJSON(http.MethodGet, path, nil, &out)
	return out, err
}

// MergeSuggest records that source should merge into target (a person confirms it).
func (c *Client) MergeSuggest(targetID, sourceID, reason string) (jobstore.WorkMergeSuggestion, bool, error) {
	body, err := jsonBody(map[string]any{"source_id": sourceID, "reason": reason})
	if err != nil {
		return jobstore.WorkMergeSuggestion{}, false, err
	}
	var out struct {
		Suggestion jobstore.WorkMergeSuggestion `json:"suggestion"`
		Recorded   bool                         `json:"recorded"`
	}
	err = c.doJSON(http.MethodPost, workPath(targetID, "merge-suggestions"), body, &out)
	return out.Suggestion, out.Recorded, err
}

// ListMergeSuggestions lists the pending merge suggestions.
func (c *Client) ListMergeSuggestions() ([]jobstore.WorkMergeSuggestion, error) {
	var out struct {
		Suggestions []jobstore.WorkMergeSuggestion `json:"suggestions"`
	}
	err := c.doJSON(http.MethodGet, "/v1/work-items/merge-suggestions", nil, &out)
	return out.Suggestions, err
}

// AcceptMergeSuggestion is a person's yes: it performs the merge.
func (c *Client) AcceptMergeSuggestion(id int64) (work.DetailView, error) {
	var out work.DetailView
	err := c.doJSON(http.MethodPost, "/v1/work-items/merge-suggestions/"+strconv.FormatInt(id, 10)+"/accept", nil, &out)
	return out, err
}

// DismissMergeSuggestion is a person's no.
func (c *Client) DismissMergeSuggestion(id int64) error {
	return c.doJSON(http.MethodPost, "/v1/work-items/merge-suggestions/"+strconv.FormatInt(id, 10)+"/dismiss", nil, nil)
}

// ---------------------------------------------------------------- X2

// SessionAsk "带话": delivers a short message to a running session (terminal session id
// or ACP / pty session job id). A session that is not online is a 409 error.
func (c *Client) SessionAsk(sid, text, workID string) (work.AskResult, error) {
	body, err := jsonBody(map[string]any{"session_id": sid, "text": text, "work_id": workID})
	if err != nil {
		return work.AskResult{}, err
	}
	var out work.AskResult
	err = c.doJSON(http.MethodPost, "/v1/session-ask", body, &out)
	return out, err
}

// IssueBrief is one row of the read-only issue list (GET /v1/issues).
type IssueBrief struct {
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

// IssueListOpts filters IssueList.
type IssueListOpts struct {
	Project, TrackerID, Repo, Status, Type, Query string
	Tags                                          []string
	Limit                                         int
}

// IssueListResp is the issue list with the unfiltered-by-limit total.
type IssueListResp struct {
	Issues    []IssueBrief `json:"issues"`
	Total     int          `json:"total"`
	Truncated bool         `json:"truncated"`
}

// IssueList reads the server's tracker mirror (read-only).
func (c *Client) IssueList(o IssueListOpts) (IssueListResp, error) {
	q := url.Values{}
	for k, v := range map[string]string{"project": o.Project, "tracker_id": o.TrackerID, "repo": o.Repo, "status": o.Status, "type": o.Type, "q": o.Query} {
		if v != "" {
			q.Set(k, v)
		}
	}
	for _, t := range o.Tags {
		q.Add("tag", t)
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	path := "/v1/issues"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	var out IssueListResp
	err := c.doJSON(http.MethodGet, path, nil, &out)
	return out, err
}

// IssueGetResp is one issue in full plus the repo it came from.
type IssueGetResp struct {
	TrackerID string         `json:"tracker_id"`
	Issue     map[string]any `json:"issue"`
}

// IssueGet reads one issue from the server's tracker mirror (read-only).
func (c *Client) IssueGet(id, trackerID string) (IssueGetResp, error) {
	path := "/v1/issues/" + url.PathEscape(id)
	if trackerID != "" {
		path += "?tracker_id=" + url.QueryEscape(trackerID)
	}
	var out IssueGetResp
	err := c.doJSON(http.MethodGet, path, nil, &out)
	return out, err
}

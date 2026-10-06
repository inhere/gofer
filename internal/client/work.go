package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

// WorkListOpts filters ListWorkItems (mirrors GET /v1/work-items).
type WorkListOpts struct {
	Statuses  []string
	Project   string
	Workspace string
	Session   string
	Query     string
	Unsorted  *bool
	Closed    bool
	Due       bool
	Limit     int
}

// WorkList is the list response: the cards plus the header counts.
type WorkList struct {
	Items   []work.ItemView `json:"items"`
	Summary work.Summary    `json:"summary"`
}

// WorkReportRequestResult is the per-session outcome of "ask it to report".
type WorkReportRequestResult struct {
	SessionID string `json:"session_id"`
	RequestID string `json:"request_id,omitempty"`
	Kind      string `json:"kind,omitempty"`
	State     string `json:"state,omitempty"`
	Sent      bool   `json:"sent"`
	Channel   string `json:"channel,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

func workPath(id string, rest ...string) string {
	p := "/v1/work-items/" + url.PathEscape(id)
	for _, r := range rest {
		p += "/" + r
	}
	return p
}

func jsonBody(v any) (*bytes.Reader, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	return bytes.NewReader(b), nil
}

// ListWorkItems lists work items.
func (c *Client) ListWorkItems(o WorkListOpts) (WorkList, error) {
	q := url.Values{}
	if len(o.Statuses) > 0 {
		q.Set("status", strings.Join(o.Statuses, ","))
	}
	for k, v := range map[string]string{"project": o.Project, "workspace": o.Workspace, "session": o.Session, "q": o.Query} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if o.Unsorted != nil {
		q.Set("unsorted", strconv.FormatBool(*o.Unsorted))
	}
	if o.Closed {
		q.Set("closed", "1")
	}
	if o.Due {
		q.Set("due", "1")
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	path := "/v1/work-items"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out WorkList
	err := c.doJSON(http.MethodGet, path, nil, &out)
	return out, err
}

// GetWorkItem reads one work item with its journal and sessions.
func (c *Client) GetWorkItem(id string) (work.DetailView, error) {
	var out work.DetailView
	err := c.doJSON(http.MethodGet, workPath(id), nil, &out)
	return out, err
}

// CreateWorkItem creates a work item; fields: title (required), goal, status,
// project_key, workspace, next_step, priority, session_ids.
func (c *Client) CreateWorkItem(fields map[string]any) (work.DetailView, error) {
	body, err := jsonBody(fields)
	if err != nil {
		return work.DetailView{}, err
	}
	var out work.DetailView
	err = c.doJSON(http.MethodPost, "/v1/work-items", body, &out)
	return out, err
}

// PatchWorkItem updates fields (PATCH body keys: rev, title, goal, status, status_source,
// blocker_kind, blocker_text, next_step, summary, project_key, workspace, priority,
// park_until, park_note, remind_at, unsorted). A stale rev answers 409.
func (c *Client) PatchWorkItem(id string, fields map[string]any) (work.DetailView, error) {
	body, err := jsonBody(fields)
	if err != nil {
		return work.DetailView{}, err
	}
	var out work.DetailView
	err = c.doJSON(http.MethodPatch, workPath(id), body, &out)
	return out, err
}

// AddWorkNote appends a note to the journal.
func (c *Client) AddWorkNote(id, text string) error {
	body, err := jsonBody(map[string]any{"text": text})
	if err != nil {
		return err
	}
	return c.doJSON(http.MethodPost, workPath(id, "journal"), body, nil)
}

// ReportWork writes a session's self-report (goal/status/blocker/next/summary).
func (c *Client) ReportWork(id string, fields map[string]any) (work.DetailView, error) {
	body, err := jsonBody(fields)
	if err != nil {
		return work.DetailView{}, err
	}
	var out work.DetailView
	err = c.doJSON(http.MethodPost, workPath(id, "report"), body, &out)
	return out, err
}

// LinkWork links an issue / plan / todo / job.
func (c *Client) LinkWork(id, kind, ref string) (work.DetailView, error) {
	body, err := jsonBody(map[string]any{"kind": kind, "ref": ref})
	if err != nil {
		return work.DetailView{}, err
	}
	var out work.DetailView
	err = c.doJSON(http.MethodPost, workPath(id, "links"), body, &out)
	return out, err
}

// UnlinkWork removes a link.
func (c *Client) UnlinkWork(id, kind, ref string) (work.DetailView, error) {
	q := url.Values{"kind": {kind}, "ref": {ref}}
	var out work.DetailView
	err := c.doJSON(http.MethodDelete, workPath(id, "links")+"?"+q.Encode(), nil, &out)
	return out, err
}

// AttachWorkSession makes a session a current session of the item.
func (c *Client) AttachWorkSession(id, sid string) (work.DetailView, error) {
	body, err := jsonBody(map[string]any{"session_id": sid})
	if err != nil {
		return work.DetailView{}, err
	}
	var out work.DetailView
	err = c.doJSON(http.MethodPost, workPath(id, "sessions"), body, &out)
	return out, err
}

// DetachWorkSession moves a session to the item's history.
func (c *Client) DetachWorkSession(id, sid string) (work.DetailView, error) {
	var out work.DetailView
	err := c.doJSON(http.MethodDelete, workPath(id, "sessions", url.PathEscape(sid)), nil, &out)
	return out, err
}

// MergeWorkItems folds sources into the target item.
func (c *Client) MergeWorkItems(target string, sources []string) (work.DetailView, error) {
	body, err := jsonBody(map[string]any{"sources": sources})
	if err != nil {
		return work.DetailView{}, err
	}
	var out work.DetailView
	err = c.doJSON(http.MethodPost, workPath(target, "merge"), body, &out)
	return out, err
}

// WorkSplitResult is the split response: the source and the new item.
type WorkSplitResult struct {
	Source work.DetailView `json:"source"`
	Item   work.DetailView `json:"item"`
}

// SplitWorkItem creates a new item out of id.
func (c *Client) SplitWorkItem(id, title, goal string, sessionIDs []string, keep bool) (WorkSplitResult, error) {
	body, err := jsonBody(map[string]any{"title": title, "goal": goal, "session_ids": sessionIDs, "keep_sessions": keep})
	if err != nil {
		return WorkSplitResult{}, err
	}
	var out WorkSplitResult
	err = c.doJSON(http.MethodPost, workPath(id, "split"), body, &out)
	return out, err
}

// ListWorkRequests reads the request ledger of one item ("" = every item).
func (c *Client) ListWorkRequests(id string, activeOnly bool, limit int) ([]jobstore.WorkRequest, error) {
	q := url.Values{}
	if activeOnly {
		q.Set("active", "1")
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/v1/work-items/requests"
	if id != "" {
		path = workPath(id, "requests")
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out struct {
		Requests []jobstore.WorkRequest `json:"requests"`
	}
	err := c.doJSON(http.MethodGet, path, nil, &out)
	return out.Requests, err
}

// RequestWorkReport asks the item's running session(s) to report (kind "report", the
// default) or to write a hand-over ("handoff") through the request ledger; a session
// that is not running is tidied up instead.
func (c *Client) RequestWorkReport(id, sessionID, kind string) (bool, []WorkReportRequestResult, error) {
	body, err := jsonBody(map[string]any{"session_id": sessionID, "kind": kind})
	if err != nil {
		return false, nil, err
	}
	var out struct {
		Sent    bool                      `json:"sent"`
		Results []WorkReportRequestResult `json:"results"`
	}
	err = c.doJSON(http.MethodPost, workPath(id, "report-request"), body, &out)
	return out.Sent, out.Results, err
}

// SummarizeWork triggers a tidy-up of the item now; it returns the ledger request that
// tracks it (the run is in the background).
func (c *Client) SummarizeWork(id string) (jobstore.WorkRequest, error) {
	var out struct {
		Request jobstore.WorkRequest `json:"request"`
	}
	err := c.doJSON(http.MethodPost, workPath(id, "summarize"), nil, &out)
	return out.Request, err
}

// AcceptWorkSuggestion adopts a pending suggestion ("goal", "blocker", "next", ...).
func (c *Client) AcceptWorkSuggestion(id, field string) (work.DetailView, error) {
	var out work.DetailView
	err := c.doJSON(http.MethodPost, workPath(id, "suggestions", url.PathEscape(field), "accept"), nil, &out)
	return out, err
}

// DismissWorkSuggestion drops a pending suggestion.
func (c *Client) DismissWorkSuggestion(id, field string) (work.DetailView, error) {
	var out work.DetailView
	err := c.doJSON(http.MethodPost, workPath(id, "suggestions", url.PathEscape(field), "dismiss"), nil, &out)
	return out, err
}

// WorkSummarizerStatus reads the summarizer's availability and the effective work
// settings as raw JSON (the console's shape).
func (c *Client) WorkSummarizerStatus() (map[string]any, error) {
	var out map[string]any
	err := c.doJSON(http.MethodGet, "/v1/work-items/summarizer", nil, &out)
	return out, err
}

// WorkDigest previews the digest, or with send queues it to the webhooks now.
func (c *Client) WorkDigest(send bool) (work.Digest, int, error) {
	if !send {
		var d work.Digest
		err := c.doJSON(http.MethodGet, "/v1/work-items/digest", nil, &d)
		return d, 0, err
	}
	var out struct {
		Digest work.Digest `json:"digest"`
		Queued int         `json:"queued"`
	}
	err := c.doJSON(http.MethodPost, "/v1/work-items/digest", nil, &out)
	return out.Digest, out.Queued, err
}

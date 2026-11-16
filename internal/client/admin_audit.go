package client

import (
	"net/http"
	"net/url"
	"strconv"
)

// AdminAuditEntry is one line of GET /v1/audit: a write request, who made it and how it ended.
type AdminAuditEntry struct {
	ID         int64  `json:"id"`
	At         int64  `json:"at"`
	CallerID   string `json:"caller_id,omitempty"`
	CallerKind string `json:"caller_kind,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	JobID      string `json:"job_id,omitempty"`
	Method     string `json:"method"`
	Route      string `json:"route"`
	Target     string `json:"target,omitempty"`
	QueryKeys  string `json:"query_keys,omitempty"`
	Status     int    `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	IP         string `json:"ip,omitempty"`
	Error      string `json:"error,omitempty"`
}

// AdminAuditQuery is the filter of GET /v1/audit; empty fields are not sent.
type AdminAuditQuery struct {
	Caller string
	Route  string
	Status string // ok | fail
	Since  string
	Until  string
	Cursor int64
	Limit  int
}

// AdminAuditPage is one page of the audit; NextCursor is non-zero while more lines exist.
type AdminAuditPage struct {
	Entries    []AdminAuditEntry `json:"entries"`
	Count      int               `json:"count"`
	NextCursor int64             `json:"next_cursor,omitempty"`
}

// ListAdminAudit fetches GET /v1/audit with the given filter.
func (c *Client) ListAdminAudit(q AdminAuditQuery) (AdminAuditPage, error) {
	v := url.Values{}
	for k, val := range map[string]string{"caller": q.Caller, "route": q.Route, "status": q.Status, "since": q.Since, "until": q.Until} {
		if val != "" {
			v.Set(k, val)
		}
	}
	if q.Cursor > 0 {
		v.Set("cursor", strconv.FormatInt(q.Cursor, 10))
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	path := "/v1/audit"
	if enc := v.Encode(); enc != "" {
		path += "?" + enc
	}
	var out AdminAuditPage
	if err := c.doJSON(http.MethodGet, path, nil, &out); err != nil {
		return AdminAuditPage{}, err
	}
	return out, nil
}

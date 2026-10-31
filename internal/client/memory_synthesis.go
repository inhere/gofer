package client

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/inhere/gofer/internal/jobstore"
)

// MemorySynthesisListOpts filters ListMemorySyntheses.
type MemorySynthesisListOpts struct {
	Project string
	Day     string
	State   string
	Limit   int
}

// ListMemorySyntheses reads GET /v1/memory-syntheses: the daily synthesis run records,
// metadata only (no result or material snapshot).
func (c *Client) ListMemorySyntheses(o MemorySynthesisListOpts) ([]jobstore.MemorySynthesisRun, error) {
	q := url.Values{}
	if o.Project != "" {
		q.Set("project", o.Project)
	}
	if o.Day != "" {
		q.Set("day", o.Day)
	}
	if o.State != "" {
		q.Set("state", o.State)
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	path := "/v1/memory-syntheses"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out struct {
		Runs []jobstore.MemorySynthesisRun `json:"runs"`
	}
	err := c.doJSON(http.MethodGet, path, nil, &out)
	return out.Runs, err
}

// GetMemorySynthesis reads GET /v1/memory-syntheses/{id}.
func (c *Client) GetMemorySynthesis(id string) (jobstore.MemorySynthesisRun, error) {
	var out jobstore.MemorySynthesisRun
	err := c.doJSON(http.MethodGet, "/v1/memory-syntheses/"+url.PathEscape(id), nil, &out)
	return out, err
}

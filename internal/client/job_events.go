package client

import (
	"net/http"
	"net/url"

	"github.com/inhere/gofer/internal/jobstore"
)

// ListJobEvents fetches a job's event timeline (GET /v1/jobs/{id}/events), oldest first.
func (c *Client) ListJobEvents(id string) ([]jobstore.JobEvent, error) {
	var out struct {
		Events []jobstore.JobEvent `json:"events"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/jobs/"+url.PathEscape(id)+"/events", nil, &out); err != nil {
		return nil, err
	}
	return out.Events, nil
}

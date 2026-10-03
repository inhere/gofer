package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/inhere/gofer/internal/jobstore"
)

type JobRedactRequest struct {
	Literals []string `json:"literals,omitempty"`
	Patterns []string `json:"patterns,omitempty"`
}

func (c *Client) RedactJob(id string, req JobRedactRequest) (jobstore.RedactReport, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return jobstore.RedactReport{}, fmt.Errorf("encode redact request: %w", err)
	}
	var out jobstore.RedactReport
	if err := c.doJSON(http.MethodPost, "/v1/jobs/"+url.PathEscape(id)+"/redact", bytes.NewReader(raw), &out); err != nil {
		return jobstore.RedactReport{}, err
	}
	return out, nil
}

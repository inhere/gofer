package client

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
)

type JobPreview struct {
	ID          string `json:"id"`
	JobID       string `json:"job_id"`
	Port        int    `json:"port"`
	State       string `json:"state"`
	CreatedAt   int64  `json:"created_at"`
	ExpiresAt   int64  `json:"expires_at"`
	CloseReason string `json:"close_reason,omitempty"`
	URL         string `json:"url,omitempty"`
	WorkerID    string `json:"worker_id,omitempty"`
}

func (c *Client) CreateJobPreview(id string, port, ttl int) (JobPreview, error) {
	var out JobPreview
	body, _ := json.Marshal(map[string]int{"port": port, "ttl_sec": ttl})
	err := c.doJSON(http.MethodPost, "/v1/jobs/"+url.PathEscape(id)+"/previews", bytes.NewReader(body), &out)
	return out, err
}
func (c *Client) ListJobPreviews(id string) ([]JobPreview, error) {
	var out struct {
		Previews []JobPreview `json:"previews"`
	}
	err := c.doJSON(http.MethodGet, "/v1/jobs/"+url.PathEscape(id)+"/previews", nil, &out)
	return out.Previews, err
}
func (c *Client) CloseJobPreview(jobID, previewID string) (JobPreview, error) {
	var out JobPreview
	err := c.doJSON(http.MethodDelete, "/v1/jobs/"+url.PathEscape(jobID)+"/previews/"+url.PathEscape(previewID), nil, &out)
	return out, err
}

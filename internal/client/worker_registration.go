package client

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
)

type WorkerRegistration struct {
	WorkerID         string `json:"worker_id"`
	WorkerToken      string `json:"worker_token"`
	WorkerConnectURL string `json:"worker_connect_url"`
}

func (c *Client) RegisterWorker(id string, labels, projects []string) (WorkerRegistration, error) {
	var out WorkerRegistration
	body := struct {
		WorkerID string   `json:"worker_id"`
		Labels   []string `json:"labels,omitempty"`
		Projects []string `json:"projects,omitempty"`
	}{WorkerID: id, Labels: labels, Projects: projects}
	raw, err := json.Marshal(body)
	if err != nil {
		return WorkerRegistration{}, err
	}
	if err := c.doJSON(http.MethodPost, "/v1/workers", bytes.NewReader(raw), &out); err != nil {
		return WorkerRegistration{}, err
	}
	return out, nil
}

func (c *Client) RemoveWorker(id string) error {
	return c.doJSON(http.MethodDelete, "/v1/workers/"+url.PathEscape(id), nil, nil)
}

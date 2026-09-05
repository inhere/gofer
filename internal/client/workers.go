package client

import "net/http"

// WorkerMeta is one worker returned by GET /v1/meta. It is deliberately a
// client-side wire type so commands can inspect the server without importing
// the HTTP API package.
type WorkerMeta struct {
	ID        string            `json:"id"`
	Labels    []string          `json:"labels,omitempty"`
	Projects  []string          `json:"projects,omitempty"`
	Agents    []string          `json:"agents,omitempty"`
	AgentCaps []RunnerAgentMeta `json:"agent_caps,omitempty"`
	Connected bool              `json:"connected"`
}

// ListWorkers fetches the server's worker registry from GET /v1/meta. This
// includes configured workers even when they are currently disconnected.
func (c *Client) ListWorkers() ([]WorkerMeta, error) {
	var resp struct {
		Workers []WorkerMeta `json:"workers"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/meta", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Workers, nil
}

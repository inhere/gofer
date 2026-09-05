package client

import "net/http"

// RunnerAgentMeta is the typed agent capability advertised by a runner in
// GET /v1/runners. Available is a pointer because older workers may omit the
// probe field entirely; nil means unknown rather than unavailable.
type RunnerAgentMeta struct {
	Key         string `json:"key"`
	Type        string `json:"type,omitempty"`
	Interactive bool   `json:"interactive,omitempty"`
	Available   *bool  `json:"available,omitempty"`
	Version     string `json:"version,omitempty"`
}

// RunnerCapabilities is the project and agent set a runner reports.
type RunnerCapabilities struct {
	Projects  []string          `json:"projects,omitempty"`
	AgentCaps []RunnerAgentMeta `json:"agent_caps,omitempty"`
}

// RunnerMeta is one row returned by GET /v1/runners.
type RunnerMeta struct {
	Name         string              `json:"name"`
	Type         string              `json:"type"`
	Status       string              `json:"status"`
	Capabilities *RunnerCapabilities `json:"capabilities,omitempty"`
}

// ListRunners fetches the server's configured runners and their advertised
// capabilities (GET /v1/runners). The endpoint is read-only and uses the same
// runner rows consumed by the web console.
func (c *Client) ListRunners() ([]RunnerMeta, error) {
	var resp struct {
		Runners []RunnerMeta `json:"runners"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/runners", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Runners, nil
}

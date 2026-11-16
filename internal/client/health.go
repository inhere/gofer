package client

import "net/http"

// HealthCheck is one item of the server self-check (design 2026-11-14 §5), as
// GET /v1/health/checks reports it: whether the latest probe run succeeded, how many
// runs failed in a row, and whether that streak reached the threshold (Failing).
type HealthCheck struct {
	Name                string `json:"name"`
	OK                  bool   `json:"ok"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	Failing             bool   `json:"failing"`
	LastError           string `json:"last_error,omitempty"`
	CheckedAt           int64  `json:"checked_at"`
}

// HealthChecks fetches the server's self-check items (GET /v1/health/checks).
func (c *Client) HealthChecks() ([]HealthCheck, error) {
	var resp struct {
		Checks []HealthCheck `json:"checks"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/health/checks", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Checks, nil
}

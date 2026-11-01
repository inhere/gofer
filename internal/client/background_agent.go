package client

import (
	"net/http"
	"net/url"

	"github.com/inhere/gofer/internal/bgagent"
)

// BackgroundAgents is GET /v1/background-agents.
func (c *Client) BackgroundAgents() (bgagent.View, error) {
	var out bgagent.View
	err := c.doJSON(http.MethodGet, "/v1/background-agents", nil, &out)
	return out, err
}

// ResetBackgroundAgent is POST /v1/background-agents/{kind}/session/reset.
func (c *Client) ResetBackgroundAgent(kind string) (bgagent.ResetView, error) {
	var out bgagent.ResetView
	err := c.doJSON(http.MethodPost, "/v1/background-agents/"+url.PathEscape(kind)+"/session/reset", nil, &out)
	return out, err
}

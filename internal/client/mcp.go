package client

import (
	"net/http"
	"net/url"

	"github.com/inhere/gofer/internal/config"
)

// MCPServerInfo is one MCP registry entry as GET /v1/mcp-servers reports it (MCP-06).
type MCPServerInfo struct {
	Name    string                 `json:"name"`
	Enabled bool                   `json:"enabled"`
	Spec    config.MCPServerConfig `json:"spec"`
	Secrets []struct {
		Env   string `json:"env"`
		Set   bool   `json:"set"`
		Usage string `json:"usage"`
	} `json:"secrets"`
	Bound struct {
		Server   bool     `json:"server"`
		Agents   []string `json:"agents"`
		Projects []string `json:"projects"`
	} `json:"bound"`
}

// MCPServers lists the server's MCP registry.
func (c *Client) MCPServers() ([]MCPServerInfo, error) {
	var out struct {
		Servers []MCPServerInfo `json:"servers"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/mcp-servers", nil, &out); err != nil {
		return nil, err
	}
	return out.Servers, nil
}

// MCPServer returns one registry entry.
func (c *Client) MCPServer(name string) (MCPServerInfo, error) {
	var out MCPServerInfo
	err := c.doJSON(http.MethodGet, "/v1/mcp-servers/"+url.PathEscape(name), nil, &out)
	return out, err
}

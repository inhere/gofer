package httpapi

import (
	"context"
	"net/http"
	"os"
	"sort"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/mcpprobe"
)

// mcpServerView is one MCP registry entry (MCP-06) as the API shows it: the
// declaration, the NAMES of the environment variables its secrets come from and
// whether each is set on this server — never a value — and where it is bound.
type mcpServerView struct {
	Name    string                 `json:"name"`
	Enabled bool                   `json:"enabled"`
	Spec    config.MCPServerConfig `json:"spec"`
	// Secrets lists every secret environment variable with whether this server's
	// process has it (a job run on a worker reads the worker's own environment).
	Secrets []mcpSecretView `json:"secrets"`
	Bound   mcpBindingView  `json:"bound"`
}

type mcpSecretView struct {
	Env   string `json:"env"`
	Set   bool   `json:"set"`
	Usage string `json:"usage"` // "env:<KEY>" or "header:<Name>"
}

type mcpBindingView struct {
	Server   bool     `json:"server"`
	Agents   []string `json:"agents"`
	Projects []string `json:"projects"`
}

func mcpViews(cfg *config.Config) []mcpServerView {
	out := []mcpServerView{}
	if cfg == nil {
		return out
	}
	names := make([]string, 0, len(cfg.MCPServers))
	for n := range cfg.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, mcpView(cfg, n))
	}
	return out
}

func mcpView(cfg *config.Config, name string) mcpServerView {
	spec := cfg.MCPServers[name]
	v := mcpServerView{Name: name, Enabled: spec.IsEnabled(), Spec: spec, Secrets: []mcpSecretView{}}
	add := func(usage, env string) {
		_, set := os.LookupEnv(env)
		v.Secrets = append(v.Secrets, mcpSecretView{Env: env, Set: set, Usage: usage})
	}
	for _, k := range sortedKeys(spec.SecretEnv) {
		add("env:"+k, spec.SecretEnv[k])
	}
	for _, k := range sortedKeys(spec.SecretHeaders) {
		add("header:"+k, spec.SecretHeaders[k])
	}
	srv, agents, projects := cfg.MCPBoundBy(name)
	v.Bound = mcpBindingView{Server: srv, Agents: nonNilStrings(agents), Projects: nonNilStrings(projects)}
	return v
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// GET /v1/mcp-servers
func (s *Server) handleListMCPServers(c *rux.Context) {
	var cfg *config.Config
	if s.projects != nil {
		cfg = s.projects.Config()
	}
	c.JSON(http.StatusOK, map[string]any{"servers": mcpViews(cfg)})
}

// GET /v1/mcp-servers/{name}
func (s *Server) handleGetMCPServer(c *rux.Context) {
	var cfg *config.Config
	if s.projects != nil {
		cfg = s.projects.Config()
	}
	name := c.Param("name")
	if cfg == nil {
		writeError(c, http.StatusNotFound, "MCP server not found", name)
		return
	}
	if _, ok := cfg.MCPServers[name]; !ok {
		writeError(c, http.StatusNotFound, "MCP server not found", name)
		return
	}
	c.JSON(http.StatusOK, mcpView(cfg, name))
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// POST /v1/mcp-servers/{name}/test — probe the entry from the SERVER's machine (its
// environment supplies the secrets): handshake + tools/list. A person's action; worker
// and job credentials are refused (it starts a process here).
func (s *Server) handleTestMCPServer(c *rux.Context) {
	if callerKindFromCtx(c) == callerKindWorker {
		writeError(c, http.StatusForbidden, "not permitted for this caller", "worker tokens cannot run MCP probes")
		return
	}
	if _, isJob := jobCallerFromCtx(c); isJob {
		writeError(c, http.StatusForbidden, "not permitted for this caller", "job credentials cannot run MCP probes")
		return
	}
	var cfg *config.Config
	if s.projects != nil {
		cfg = s.projects.Config()
	}
	name := c.Param("name")
	spec, ok := config.MCPServerConfig{}, false
	if cfg != nil {
		spec, ok = cfg.MCPServers[name]
	}
	if !ok {
		writeError(c, http.StatusNotFound, "MCP server not found", name)
		return
	}
	ctx, cancel := context.WithTimeout(c.Req.Context(), mcpprobe.DefaultTimeout)
	defer cancel()
	res, err := mcpprobe.Probe(ctx, spec, os.LookupEnv, "gofer")
	if err != nil {
		c.JSON(http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{"ok": true, "result": res})
}

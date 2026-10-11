package config

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/inhere/gofer/internal/util"
)

// MCP server registry (MCP-06, design 2026-10-11-mcp-registry-design.md): MCP servers
// declared once under the top-level `mcp_servers`, bound to jobs through
// server / agent / project / job lists (EffectiveMCP), and injected per agent type at
// execution. Secrets are never written here: secret_env / secret_headers hold the
// NAMES of environment variables, read on the machine that runs the job.

// MCP server transport types.
const (
	MCPTypeStdio = "stdio"
	MCPTypeHTTP  = "http"
)

// MCP injection formats an agent declares with mcp_format.
const (
	MCPFormatACP         = "acp"          // session/new mcpServers (acp-agent)
	MCPFormatClaudeJSON  = "claude-json"  // <result_dir>/mcp.json in Claude's mcpServers shape + mcp_args
	MCPFormatCodexConfig = "codex-config" // `-c mcp_servers.<name>.*` overrides
)

// MCPReservedName is the entry gofer itself injects for the steward and leader jobs.
const MCPReservedName = "gofer"

// MCPServerConfig is one registry entry.
type MCPServerConfig struct {
	// Desc is a one-line description shown in lists.
	Desc string `yaml:"desc,omitempty" json:"desc,omitempty"`
	// Type is stdio (default) or http.
	Type string `yaml:"type,omitempty" json:"type,omitempty"`
	// Command / Args / Env start a stdio server. Args may reference a secret_env
	// variable as {{env:NAME}}.
	Command string            `yaml:"command,omitempty" json:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty" json:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	// URL / Headers reach an http server. URL may reference {{env:NAME}} as well.
	URL     string            `yaml:"url,omitempty" json:"url,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	// SecretEnv maps a variable the MCP process sees to the environment variable the
	// value is read from (on the executing machine). SecretHeaders does the same for
	// http headers.
	SecretEnv     map[string]string `yaml:"secret_env,omitempty" json:"secret_env,omitempty"`
	SecretHeaders map[string]string `yaml:"secret_headers,omitempty" json:"secret_headers,omitempty"`
	// Tools optionally limits the tools an agent may call (agents that can express it).
	Tools []string `yaml:"tools,omitempty" json:"tools,omitempty"`
	// Enabled pauses the entry without deleting it (nil = on). A disabled entry is
	// skipped wherever it is bound.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// IsEnabled reports whether the entry is on.
func (m MCPServerConfig) IsEnabled() bool { return m.Enabled == nil || *m.Enabled }

// EffectiveType is Type with the stdio default applied.
func (m MCPServerConfig) EffectiveType() string {
	if m.Type == "" {
		return MCPTypeStdio
	}
	return m.Type
}

// SecretVars lists the environment variables the entry reads its secrets from, sorted
// and deduplicated.
func (m MCPServerConfig) SecretVars() []string {
	set := map[string]bool{}
	for _, v := range m.SecretEnv {
		set[v] = true
	}
	for _, v := range m.SecretHeaders {
		set[v] = true
	}
	return slices.Sorted(maps.Keys(set))
}

var (
	mcpNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// mcpEnvRefPattern is the {{env:NAME}} placeholder allowed in args and url.
	mcpEnvRefPattern = regexp.MustCompile(`\{\{env:([A-Za-z_][A-Za-z0-9_]*)\}\}`)
)

// MCPEnvRefs returns the secret_env keys a string references as {{env:NAME}}.
func MCPEnvRefs(s string) []string {
	var out []string
	for _, m := range mcpEnvRefPattern.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// ReplaceMCPEnvRefs rewrites every {{env:NAME}} in s with repl(NAME).
func ReplaceMCPEnvRefs(s string, repl func(name string) string) string {
	return mcpEnvRefPattern.ReplaceAllStringFunc(s, func(m string) string {
		return repl(mcpEnvRefPattern.FindStringSubmatch(m)[1])
	})
}

// MCPInjectedEnvName is the variable a secret is handed to the agent process under:
// GOFER_MCP_<SERVER>_<KEY>, upper-cased, every other character turned into '_'.
func MCPInjectedEnvName(server, key string) string {
	clean := func(s string) string {
		var b strings.Builder
		for _, r := range strings.ToUpper(s) {
			if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
				b.WriteRune(r)
			} else {
				b.WriteByte('_')
			}
		}
		return b.String()
	}
	return "GOFER_MCP_" + clean(server) + "_" + clean(key)
}

// ValidMCPFormat reports whether f is a known mcp_format ("" = none).
func ValidMCPFormat(f string) bool {
	switch f {
	case "", MCPFormatACP, MCPFormatClaudeJSON, MCPFormatCodexConfig:
		return true
	}
	return false
}

// ValidateMCPServers checks the registry and every binding list that names it.
func (c *Config) ValidateMCPServers() error {
	for _, name := range slices.Sorted(maps.Keys(c.MCPServers)) {
		if err := validateMCPServer(name, c.MCPServers[name]); err != nil {
			return err
		}
	}
	check := func(where string, names []string) error {
		for _, n := range names {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if _, ok := c.MCPServers[n]; !ok {
				return fmt.Errorf("%s: unknown MCP server %q (declare it under mcp_servers)", where, n)
			}
		}
		return nil
	}
	if err := check("server.mcp", c.Server.MCP); err != nil {
		return err
	}
	for _, k := range slices.Sorted(maps.Keys(c.Agents)) {
		a := c.Agents[k]
		if !ValidMCPFormat(a.MCPFormat) {
			return fmt.Errorf("agents.%s.mcp_format: %q is not one of %s, %s, %s", k, a.MCPFormat, MCPFormatACP, MCPFormatClaudeJSON, MCPFormatCodexConfig)
		}
		if err := check("agents."+k+".mcp", a.MCP); err != nil {
			return err
		}
	}
	for _, k := range slices.Sorted(maps.Keys(c.Projects)) {
		if err := check("projects."+k+".mcp", c.Projects[k].MCP); err != nil {
			return err
		}
	}
	return nil
}

func validateMCPServer(name string, m MCPServerConfig) error {
	where := "mcp_servers." + name
	if !mcpNamePattern.MatchString(name) {
		return fmt.Errorf("%s: invalid name (letters, digits, '.', '_', '-'; at most 64)", where)
	}
	if strings.EqualFold(name, MCPReservedName) {
		return fmt.Errorf("%s: %q is reserved for gofer's own MCP server", where, MCPReservedName)
	}
	switch m.EffectiveType() {
	case MCPTypeStdio:
		if strings.TrimSpace(m.Command) == "" {
			return fmt.Errorf("%s: a stdio server needs command", where)
		}
		if m.URL != "" || len(m.Headers) > 0 || len(m.SecretHeaders) > 0 {
			return fmt.Errorf("%s: url / headers / secret_headers apply to type: http only", where)
		}
	case MCPTypeHTTP:
		if strings.TrimSpace(m.URL) == "" {
			return fmt.Errorf("%s: an http server needs url", where)
		}
		if m.Command != "" || len(m.Args) > 0 {
			return fmt.Errorf("%s: command / args apply to type: stdio only", where)
		}
	default:
		return fmt.Errorf("%s.type: %q is not stdio or http", where, m.Type)
	}
	for k, v := range m.SecretEnv {
		if !envNamePattern.MatchString(k) || !envNamePattern.MatchString(v) {
			return fmt.Errorf("%s.secret_env: %q: %q must map a variable name to an environment variable name", where, k, v)
		}
		if _, dup := m.Env[k]; dup {
			return fmt.Errorf("%s: %q is in both env and secret_env", where, k)
		}
	}
	for k, v := range m.SecretHeaders {
		if strings.TrimSpace(k) == "" || !envNamePattern.MatchString(v) {
			return fmt.Errorf("%s.secret_headers: %q: %q must map a header to an environment variable name", where, k, v)
		}
		if _, dup := m.Headers[k]; dup {
			return fmt.Errorf("%s: header %q is in both headers and secret_headers", where, k)
		}
	}
	refs := MCPEnvRefs(m.URL)
	for _, a := range m.Args {
		refs = append(refs, MCPEnvRefs(a)...)
	}
	for _, r := range refs {
		if _, ok := m.SecretEnv[r]; !ok {
			return fmt.Errorf("%s: {{env:%s}} is not declared in secret_env", where, r)
		}
	}
	return nil
}

// EffectiveMCP resolves the MCP servers bound to one job: the union of server.mcp,
// agents.<agent>.mcp, projects.<project>.mcp and the request's names, first
// appearance first, disabled entries dropped. Like EffectiveSkills, disable
// (--no-mcp) and an exec agent return nil. Unknown names are kept so the caller can
// reject them with the name.
func (c *Config) EffectiveMCP(projectKey, agentKey string, requested []string, disable bool, agentType string) []string {
	if disable || agentType == execAgentType {
		return nil
	}
	var server, agent, project []string
	if c != nil {
		server = c.Server.MCP
		if a, ok := c.Agents[agentKey]; ok {
			agent = a.MCP
		}
		if p, ok := c.Projects[projectKey]; ok {
			project = p.MCP
		}
	}
	n := util.CapSum(len(server), len(agent), len(project), len(requested))
	if n == 0 {
		return nil
	}
	out := make([]string, 0, n)
	seen := make(map[string]struct{}, n)
	for _, level := range [4][]string{server, agent, project, requested} {
		for _, name := range level {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			if c != nil {
				if m, ok := c.MCPServers[name]; ok && !m.IsEnabled() {
					continue
				}
			}
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// MCPBoundBy reports where registry entry name is bound: server.mcp, and the agents
// and projects listing it (sorted).
func (c *Config) MCPBoundBy(name string) (server bool, agents, projects []string) {
	if c == nil {
		return false, nil, nil
	}
	server = slices.Contains(c.Server.MCP, name)
	for _, k := range slices.Sorted(maps.Keys(c.Agents)) {
		if slices.Contains(c.Agents[k].MCP, name) {
			agents = append(agents, k)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(c.Projects)) {
		if slices.Contains(c.Projects[k].MCP, name) {
			projects = append(projects, k)
		}
	}
	return server, agents, projects
}

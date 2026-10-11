package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidateMCPServers(t *testing.T) {
	off := false
	ok := &Config{
		MCPServers: map[string]MCPServerConfig{
			"pg":  {Command: "npx", Args: []string{"server-pg", "{{env:PG_URL}}"}, SecretEnv: map[string]string{"PG_URL": "GOFER_PG"}},
			"api": {Type: "http", URL: "https://x/mcp", SecretHeaders: map[string]string{"Authorization": "API_AUTH"}},
			"old": {Command: "x", Enabled: &off},
		},
		Server:   ServerConfig{MCP: []string{"pg"}},
		Agents:   map[string]AgentConfig{"a": {MCP: []string{"api"}, MCPFormat: MCPFormatClaudeJSON, MCPArgs: []string{"--mcp-config", "{{mcp_config_file}}"}}},
		Projects: map[string]ProjectConfig{"p": {MCP: []string{"old"}}},
	}
	if err := ok.ValidateMCPServers(); err != nil {
		t.Fatalf("valid registry rejected: %v", err)
	}
	for name, tc := range map[string]struct {
		cfg  Config
		want string
	}{
		"reserved":     {Config{MCPServers: map[string]MCPServerConfig{"gofer": {Command: "x"}}}, "reserved"},
		"bad name":     {Config{MCPServers: map[string]MCPServerConfig{"a b": {Command: "x"}}}, "invalid name"},
		"no command":   {Config{MCPServers: map[string]MCPServerConfig{"x": {}}}, "needs command"},
		"no url":       {Config{MCPServers: map[string]MCPServerConfig{"x": {Type: "http"}}}, "needs url"},
		"bad type":     {Config{MCPServers: map[string]MCPServerConfig{"x": {Type: "sse", URL: "u"}}}, "not stdio or http"},
		"stdio url":    {Config{MCPServers: map[string]MCPServerConfig{"x": {Command: "c", URL: "u"}}}, "type: http only"},
		"undeclared":   {Config{MCPServers: map[string]MCPServerConfig{"x": {Command: "c", Args: []string{"{{env:K}}"}}}}, "not declared in secret_env"},
		"secret value": {Config{MCPServers: map[string]MCPServerConfig{"x": {Command: "c", SecretEnv: map[string]string{"K": "abc=def"}}}}, "secret_env"},
		"dup env":      {Config{MCPServers: map[string]MCPServerConfig{"x": {Command: "c", Env: map[string]string{"K": "1"}, SecretEnv: map[string]string{"K": "V"}}}}, "both env and secret_env"},
		"unknown bind": {Config{Projects: map[string]ProjectConfig{"p": {MCP: []string{"nope"}}}}, `projects.p.mcp: unknown MCP server "nope"`},
		"bad format":   {Config{Agents: map[string]AgentConfig{"a": {MCPFormat: "toml"}}}, "mcp_format"},
	} {
		if err := tc.cfg.ValidateMCPServers(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err=%v, want %q", name, err, tc.want)
		}
	}
}

func TestEffectiveMCP(t *testing.T) {
	off := false
	cfg := &Config{
		MCPServers: map[string]MCPServerConfig{"s": {}, "a": {}, "p": {}, "j": {}, "off": {Enabled: &off}},
		Server:     ServerConfig{MCP: []string{"s"}},
		Agents:     map[string]AgentConfig{"claude": {MCP: []string{"a", "s"}}},
		Projects:   map[string]ProjectConfig{"proj": {MCP: []string{"p", "off"}}},
	}
	if got := cfg.EffectiveMCP("proj", "claude", []string{" j ", "a", ""}, false, "cli-agent"); !reflect.DeepEqual(got, []string{"s", "a", "p", "j"}) {
		t.Fatalf("union = %v", got)
	}
	if got := cfg.EffectiveMCP("proj", "claude", []string{"j"}, true, "cli-agent"); got != nil {
		t.Fatalf("--no-mcp = %v", got)
	}
	if got := cfg.EffectiveMCP("proj", "exec", nil, false, "exec"); got != nil {
		t.Fatalf("exec = %v", got)
	}
	if got := (*Config)(nil).EffectiveMCP("p", "a", []string{"x"}, false, ""); !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("nil config = %v", got)
	}
	if got := MCPInjectedEnvName("pg-readonly", "PG_URL"); got != "GOFER_MCP_PG_READONLY_PG_URL" {
		t.Fatalf("env name = %s", got)
	}
}

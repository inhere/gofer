package agent

import (
	"reflect"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// codexAgent / claudeAgent mirror the built-in SystemInject defaults (registry.go):
// codex steers via `-c key=value`, claude via `--append-system-prompt`.
var (
	codexAgent  = config.AgentConfig{SystemInject: []string{"-c", "developer_instructions={{system_prompt}}"}}
	claudeAgent = config.AgentConfig{SystemInject: []string{"--append-system-prompt", "{{system_prompt}}"}}
)

// TestMcpEnvInjectArgsCodex: a codex agent + non-empty env renders one
// `-c mcp_servers.gofer.env.<KEY>=<VALUE>` pair per entry, keys sorted for a
// deterministic argv (gap①, issue 7z6j).
func TestMcpEnvInjectArgsCodex(t *testing.T) {
	got := McpEnvInjectArgs(codexAgent, map[string]string{
		"GOFER_AGENT_ROLE": "supervisor",
		"A":                "1",
	})
	want := []string{
		"-c", "mcp_servers.gofer.env.A=1",
		"-c", "mcp_servers.gofer.env.GOFER_AGENT_ROLE=supervisor",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("codex mcp env inject = %#v, want %#v", got, want)
	}
}

// TestMcpEnvInjectArgsEmptyEnv: a codex agent with no env injects nothing (a plain
// codex job must not be polluted with `-c`).
func TestMcpEnvInjectArgsEmptyEnv(t *testing.T) {
	if got := McpEnvInjectArgs(codexAgent, nil); got != nil {
		t.Fatalf("empty env should inject nil, got %#v", got)
	}
	if got := McpEnvInjectArgs(codexAgent, map[string]string{}); got != nil {
		t.Fatalf("empty env map should inject nil, got %#v", got)
	}
}

// TestMcpEnvInjectArgsNonCodex: a non-codex agent (claude `--append-system-prompt`)
// is never touched even with env present — the judge is the codex-style `-c` form.
func TestMcpEnvInjectArgsNonCodex(t *testing.T) {
	if got := McpEnvInjectArgs(claudeAgent, map[string]string{"X": "1"}); got != nil {
		t.Fatalf("non-codex agent should inject nil, got %#v", got)
	}
	// An agent with no SystemInject at all is likewise untouched.
	if got := McpEnvInjectArgs(config.AgentConfig{}, map[string]string{"X": "1"}); got != nil {
		t.Fatalf("agent without SystemInject should inject nil, got %#v", got)
	}
}

// TestMcpEnvInjectArgsCustomName: AgentConfig.McpServerName overrides the default
// `gofer` block name in the rendered `-c mcp_servers.<name>.env.…` path.
func TestMcpEnvInjectArgsCustomName(t *testing.T) {
	named := codexAgent
	named.McpServerName = "gofer2"
	got := McpEnvInjectArgs(named, map[string]string{"K": "v"})
	want := []string{"-c", "mcp_servers.gofer2.env.K=v"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("custom mcp server name = %#v, want %#v", got, want)
	}
}

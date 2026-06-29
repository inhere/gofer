package agent

import (
	"sort"

	"github.com/inhere/gofer/internal/config"
)

// McpServerNameDefault is the codex config block name of the gofer MCP server
// (`[mcp_servers.<name>]` in codex config.toml). It is the server whose per-server
// env we override via `-c mcp_servers.<name>.env.<KEY>=<VALUE>` (see
// McpEnvInjectArgs). The convention is "gofer"; an agent may override it with
// AgentConfig.McpServerName when its codex config uses a different block name.
const McpServerNameDefault = "gofer"

// usesCodexConfigOverride reports whether the agent steers via codex-style
// `-c <dotted.path>=<value>` config overrides — the prerequisite for the
// `-c mcp_servers.<name>.env.<KEY>=<VALUE>` injection done by McpEnvInjectArgs.
//
// We key the判据 off the agent's SystemInject template (whose codex built-in
// default is `["-c", "developer_instructions=…"]`, registry.go) rather than the
// command string, for two reasons: (1) codex behaviours are already keyed on the
// SystemInject default in builtinSessionDefaults, so this stays consistent; (2) it
// is testable with a harmless `echo`/wrapper command (tests need not invoke a real
// codex binary). The signal is precise: an agent whose CLI accepts `-c key=value`
// TOML overrides is exactly one that accepts the mcp_servers env override.
func usesCodexConfigOverride(ac config.AgentConfig) bool {
	return len(ac.SystemInject) > 0 && ac.SystemInject[0] == "-c"
}

// McpEnvInjectArgs renders codex `-c mcp_servers.<name>.env.<KEY>=<VALUE>` config
// overrides that push each env entry onto the gofer MCP child that codex spawns
// (gap①, issue 7z6j). codex starts MCP stdio servers with a SANITISED env and does
// NOT inherit the codex process env, so env injected into the codex process
// (c0f355a's runReq.Env → cmd.Env) never reaches the MCP child. Routing the same
// entries through codex's per-server `-c` override makes them land on the MCP
// child's env, so e.g. `--role supervisor`'s GOFER_AGENT_ROLE=supervisor lets the
// child's gofer MCP self-register role=supervisor.
//
// It returns nil unless the agent uses codex-style `-c` config overrides AND env
// is non-empty — so a plain codex job (no role.env) is unaffected, and non-codex
// agents (e.g. claude's `--append-system-prompt`) are never touched. Keys are
// sorted for a deterministic rendered command. Each entry yields TWO argv elements
// (`-c`, `mcp_servers.<name>.env.<KEY>=<VALUE>`), mirroring SystemInject: argv is
// preserved element-wise and never shell-joined (SR403); codex parses the `-c`
// value as TOML with a raw-literal fallback, so a bare value survives intact.
//
// Secret boundary: only the caller-supplied env (job.Env / role.env) is passed
// here. The MCP connection token must live in codex config.toml
// (`[mcp_servers.<name>].env`) — it is NEVER rendered here, so no secret enters
// the persisted rendered command (SR403/SR805).
func McpEnvInjectArgs(ac config.AgentConfig, env map[string]string) []string {
	if len(env) == 0 || !usesCodexConfigOverride(ac) {
		return nil
	}
	name := ac.McpServerName
	if name == "" {
		name = McpServerNameDefault
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(env)*2)
	for _, k := range keys {
		out = append(out, "-c", "mcp_servers."+name+".env."+k+"="+env[k])
	}
	return out
}

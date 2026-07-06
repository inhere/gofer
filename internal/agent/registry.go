// Package agent provides the agent registry, command-template rendering, the
// adapter that turns a job request into an executable argv, and a detect probe.
// See plan §9 (P3), §6.1 (AgentConfig) and §11 (security: keep argv, never
// build a shell string).
package agent

import (
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/inhere/gofer/internal/config"
)

// Agent type identifiers (AgentConfig.Type).
const (
	// TypeCLIAgent renders Command + Args templates with the job prompt.
	TypeCLIAgent = "cli-agent"
	// TypeExec runs a request-supplied argv verbatim (no prompt/template).
	TypeExec = "exec"
)

// ExecAgentKey is the reserved key of the built-in exec agent. It is always
// available without a config declaration, mirroring the built-in "local"
// runner. Being built-in does NOT bypass a project's allowed_agents allowlist
// (see CheckAllowed and plan §11).
const ExecAgentKey = "exec"

// builtinExecAgent is the implicit exec agent returned by Get("exec") when the
// config does not declare it. It needs no external CLI.
func builtinExecAgent() config.AgentConfig {
	return config.AgentConfig{Type: TypeExec}
}

// Registry exposes the agents declared in a loaded config plus the built-in
// exec agent. It is read-only over *config.Config.
//
// The config is held behind an atomic.Pointer so SIGHUP-driven hot-reload (C3)
// can atomically swap it without locking the read paths. Each read takes one
// snapshot (cfg.Load()) so a concurrent Reload cannot tear a single call.
type Registry struct {
	cfg atomic.Pointer[config.Config]
}

// NewRegistry builds an agent registry over cfg.
func NewRegistry(cfg *config.Config) *Registry {
	r := &Registry{}
	r.cfg.Store(cfg)
	return r
}

// Reload atomically swaps the registry's config to newCfg (C3 hot-reload). It
// is safe to call concurrently with any read path.
func (r *Registry) Reload(newCfg *config.Config) { r.cfg.Store(newCfg) }

// Get returns the agent config for key. The built-in "exec" agent resolves even
// when the config does not declare it. The second return is false for an
// unknown key.
func (r *Registry) Get(key string) (config.AgentConfig, bool) {
	return ResolveAgent(r.cfg.Load(), key)
}

// ResolveAgent resolves an agent config for key against a config snapshot, with
// the same built-in "exec" semantics as Registry.Get. It lets callers that
// already hold a config snapshot (e.g. job.Service.validate) resolve an agent
// from that exact snapshot rather than the registry's (possibly concurrently
// reloaded) pointer.
func ResolveAgent(cfg *config.Config, key string) (config.AgentConfig, bool) {
	if cfg != nil {
		if a, ok := cfg.Agents[key]; ok {
			// An explicit exec entry is honoured but normalised to Type=exec so a
			// bare `exec:` block (no type) still behaves as the built-in.
			if key == ExecAgentKey && a.Type == "" {
				a.Type = TypeExec
			}
			return applySessionDefaults(key, a), true
		}
	}
	if key == ExecAgentKey {
		return builtinExecAgent(), true
	}
	return config.AgentConfig{}, false
}

// builtinSessionDefaults holds the实测内置 session 配置（session-capture §6.4），
// 按 agent 名兜底。仅当某 agent 的对应 session 字段未显式配置时才填充（显式配置覆盖
// 内置）。claude 用注入模式（gofer 生成 uuid → --session-id），codex 用捕获模式
// （默认输出头部 `session id:`），resume 是整条 argv 模板（{{session_id}}/{{prompt}}）。
// G031：仅含通用 agent（claude/codex）默认，不含任何业务相关信息。
var builtinSessionDefaults = map[string]config.AgentConfig{
	"claude": {
		SessionInject: []string{"--session-id", "{{session_id}}"},
		SessionResume: []string{"--resume", "{{session_id}}", "-p", "{{prompt}}"},
		// E35: claude injects a resident system prompt via --append-system-prompt
		// (kept its own argv element so a multi-word prompt is never re-tokenised).
		SystemInject: []string{"--append-system-prompt", "{{system_prompt}}"},
	},
	"codex": {
		SessionCapture: `session id:\s*([0-9a-f-]+)`,
		SessionResume:  []string{"exec", "resume", "{{session_id}}", "{{prompt}}"},
		// E35 (实测定稿 2026-06-29, codex-cli 0.142): codex has NO --append-system-prompt
		// argv flag; the per-invocation steering channel is `-c developer_instructions=<p>`
		// (a config override — `instructions` works too; verified recognised via
		// --strict-config). Behaviourally confirmed: codex honours the injected prompt (a
		// marker token forced by it appears in the reply; a no-inject control does not), and
		// the value is robust — codex parses the `-c` value as TOML and falls back to a raw
		// literal, so quotes / `=` / `[...]` / real newlines in a role prompt all survive.
		// Render keeps `developer_instructions={{system_prompt}}` ONE argv element (no shell
		// re-tokenise). `developer_instructions` (the developer message) is the right layer
		// for role/persona steering and only fires when a role/system_prompt is set, so plain
		// codex jobs are unaffected. `codex exec resume <sid>` restores it natively (like
		// claude), so ResumeJob does NOT re-inject (see resume.go).
		SystemInject: []string{"-c", "developer_instructions={{system_prompt}}"},
	},
}

// applySessionDefaults fills an agent's unset session fields from the built-in
// defaults for that agent name (session-capture §6.4). Each of the three session
// fields is filled INDEPENDENTLY and ONLY when empty, so an explicit config value
// always wins (no overwrite). Agents without a built-in default are returned
// unchanged. The input is a copy (value receiver upstream), so this never mutates
// the loaded config.
func applySessionDefaults(key string, a config.AgentConfig) config.AgentConfig {
	def, ok := builtinSessionDefaultFor(key, a)
	if !ok {
		return a
	}
	if len(a.SessionInject) == 0 {
		a.SessionInject = def.SessionInject
	}
	if a.SessionCapture == "" {
		a.SessionCapture = def.SessionCapture
	}
	if len(a.SessionResume) == 0 {
		a.SessionResume = def.SessionResume
	}
	if len(a.SystemInject) == 0 {
		a.SystemInject = def.SystemInject
	}
	return a
}

func builtinSessionDefaultFor(key string, a config.AgentConfig) (config.AgentConfig, bool) {
	if def, ok := builtinSessionDefaults[key]; ok {
		return def, true
	}
	if !a.Interactive {
		return config.AgentConfig{}, false
	}
	command := strings.ToLower(filepath.Base(a.Command))
	if strings.HasSuffix(command, ".exe") {
		command = strings.TrimSuffix(command, ".exe")
	}
	def, ok := builtinSessionDefaults[command]
	return def, ok
}

// Names returns all agent keys (config-declared plus the built-in exec), sorted
// for stable output. The built-in exec is included even if not declared.
func (r *Registry) Names() []string {
	seen := map[string]bool{}
	if cfg := r.cfg.Load(); cfg != nil {
		for k := range cfg.Agents {
			seen[k] = true
		}
	}
	seen[ExecAgentKey] = true

	names := make([]string, 0, len(seen))
	for k := range seen {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// List returns every agent keyed by name (config-declared plus built-in exec).
func (r *Registry) List() map[string]config.AgentConfig {
	out := map[string]config.AgentConfig{}
	for _, name := range r.Names() {
		a, _ := r.Get(name)
		out[name] = a
	}
	return out
}

// Package agent provides the agent registry, command-template rendering, the
// adapter that turns a job request into an executable argv, and a detect probe.
// See plan §9 (P3), §6.1 (AgentConfig) and §11 (security: keep argv, never
// build a shell string).
package agent

import (
	"sort"
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
			return a, true
		}
	}
	if key == ExecAgentKey {
		return builtinExecAgent(), true
	}
	return config.AgentConfig{}, false
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

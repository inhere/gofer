// Package agent provides the agent registry, command-template rendering, the
// adapter that turns a job request into an executable argv, and a detect probe.
// See plan §9 (P3), §6.1 (AgentConfig) and §11 (security: keep argv, never
// build a shell string).
package agent

import (
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
// The config AND the availability cache are held behind ONE atomic.Pointer so
// SIGHUP-driven hot-reload (C3) can atomically swap both without locking the read
// paths. Each read takes one snapshot so a concurrent Reload cannot tear a single
// call — and, because the two live in the same snapshot, a reader can never pair a
// new config with the previous config's detect results.
type Registry struct {
	snap atomic.Pointer[registrySnapshot]
}

// registrySnapshot is the atomically swapped unit: a config plus the availability
// results of THE detect pass that resolved it (agent.Resolve's second return).
//
// avail == nil means COLD: this registry was assembled without a detect pass
// (NewRegistry / Reload — the core-less paths in internal/commands, and tests).
// A cold cache is warmed lazily by Availability, never read as "everything is
// unavailable" — that would be a false negative, i.e. an installed agent silently
// disappearing from /v1/agents and the MCP tool.
//
// A non-nil (even empty) avail means SEEDED: a Detector ran and its verdict stands
// as-is; a key it did not report is unavailable (Detector contract, see resolve.go).
type registrySnapshot struct {
	cfg   *config.Config
	avail map[string]DetectResult
}

// NewRegistry builds an agent registry over cfg with a COLD availability cache.
// Callers that already ran a detect pass (core.Build, via agent.Resolve) must use
// NewRegistryWith so the results are cached instead of re-probed per request.
func NewRegistry(cfg *config.Config) *Registry {
	r := &Registry{}
	r.snap.Store(&registrySnapshot{cfg: cfg}) // avail == nil: cold
	return r
}

// NewRegistryWith builds an agent registry over cfg and SEEDS its availability
// cache with avail — the second return of agent.Resolve, i.e. the results of the
// one detect pass that already ran for this config.
//
// This is what keeps GET /v1/agents and the MCP ListAgents tool free of child
// processes: both are read paths over this cache, and ListAgents in particular is a
// tool an agent calls repeatedly. Passing a nil avail here still counts as seeded
// (a Detector ran and reported nothing — e.g. NoopDetector in tests); pass through
// NewRegistry to declare that no detect pass ran at all.
func NewRegistryWith(cfg *config.Config, avail map[string]DetectResult) *Registry {
	if avail == nil {
		avail = map[string]DetectResult{} // seeded-but-empty, NOT cold (see registrySnapshot)
	}
	r := &Registry{}
	r.snap.Store(&registrySnapshot{cfg: cfg, avail: avail})
	return r
}

// Reload atomically swaps the registry's config to newCfg (C3 hot-reload), leaving
// the availability cache COLD (lazily re-probed). Callers on the reload path already
// hold fresh detect results (core.ReloadWith) and must use ReloadWith instead.
func (r *Registry) Reload(newCfg *config.Config) { r.snap.Store(&registrySnapshot{cfg: newCfg}) }

// ReloadWith atomically swaps BOTH the config and the availability cache (C3
// hot-reload). The two are swapped as one snapshot, so a reader concurrent with the
// swap sees either the old config with its old detect results or the new config with
// its new ones — never a mix.
//
// This is how a newly installed CLI becomes visible: the reload runs one fresh detect
// pass (core.ReloadWith → agent.Resolve) and its results land here.
func (r *Registry) ReloadWith(newCfg *config.Config, avail map[string]DetectResult) {
	if avail == nil {
		avail = map[string]DetectResult{} // seeded-but-empty, NOT cold (see registrySnapshot)
	}
	r.snap.Store(&registrySnapshot{cfg: newCfg, avail: avail})
}

// Get returns the agent config for key. The built-in "exec" agent resolves even
// when the config does not declare it. The second return is false for an
// unknown key.
func (r *Registry) Get(key string) (config.AgentConfig, bool) {
	return ResolveAgent(r.config(), key)
}

// config returns the config of the current snapshot (nil-safe).
func (r *Registry) config() *config.Config {
	if s := r.snap.Load(); s != nil {
		return s.cfg
	}
	return nil
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
		SessionInject:            []string{"--session-id", "{{session_id}}"},
		SessionResume:            []string{"--resume", "{{session_id}}", "-p", "{{prompt}}"},
		SessionResumeInteractive: []string{"--resume", "{{session_id}}"}, // 交互:进 TUI，无 -p
		// E35: claude injects a resident system prompt via --append-system-prompt
		// (kept its own argv element so a multi-word prompt is never re-tokenised).
		SystemInject: []string{"--append-system-prompt", "{{system_prompt}}"},
	},
	"codex": {
		SessionCapture:           `session id:\s*([0-9a-f-]+)`,
		SessionResume:            []string{"exec", "resume", "{{session_id}}", "{{prompt}}"},
		SessionResumeInteractive: []string{"resume", "{{session_id}}"}, // ⚠️ 实测确认 codex 交互 resume 命令
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
	if len(a.SessionResumeInteractive) == 0 {
		a.SessionResumeInteractive = def.SessionResumeInteractive
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
	command := strings.ToLower(commandBase(a.Command))
	if strings.HasSuffix(command, ".exe") {
		command = strings.TrimSuffix(command, ".exe")
	}
	def, ok := builtinSessionDefaults[command]
	return def, ok
}

func commandBase(command string) string {
	command = strings.TrimSpace(command)
	if i := strings.LastIndexAny(command, `/\`); i >= 0 {
		return command[i+1:]
	}
	return command
}

// Names returns all agent keys (config-declared plus the built-in exec), sorted
// for stable output. The built-in exec is included even if not declared.
func (r *Registry) Names() []string { return agentNames(r.config()) }

// agentNames lists the agent keys of a config snapshot (declared plus built-in
// exec), sorted. Taking the snapshot as an argument lets a caller that must not
// re-load the pointer mid-call (Availability) stay on ONE snapshot.
func agentNames(cfg *config.Config) []string {
	seen := map[string]bool{}
	if cfg != nil {
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

// Availability returns the availability of EVERY agent in the registry WITHOUT
// spawning a child process per call: it serves the results of the ONE detect pass
// that already ran for the current config (core.Build / core.ReloadWith →
// agent.Resolve → NewRegistryWith/ReloadWith).
//
// This is the read path GET /v1/agents and the MCP ListAgents tool sit on. Both used
// to run a live probe per agent PER REQUEST — ListAgents especially, which an agent
// calls over and over, and whose cost scales with the agent count that P2's template
// injection is about to grow.
//
// Freshness: the cache turns over with the config snapshot, so a CLI installed after
// the process started shows up on the next reload (SIGHUP / `gofer worker reload`),
// which re-runs the detect pass. It does NOT self-refresh on a timer — availability
// is a display/report fact, never an admission gate.
//
// Two invariants keep this cache from EVER producing a false negative (an installed
// agent reported as missing — which is how an agent silently vanishes from a caps
// view):
//   - A COLD cache (registry built outside core: the core-less CLI paths, tests) is
//     probed live once and memoized — never read as "everything unavailable".
//   - An exec-type agent is available by construction (no external CLI to find), so it
//     is answered from lookPathProbe regardless of what the cache holds — a hermetic
//     Detector (NoopDetector) must not be able to report the BUILT-IN exec agent gone.
//
// The returned map is a fresh copy; callers may keep or mutate it freely.
func (r *Registry) Availability() map[string]DetectResult {
	s := r.snap.Load()
	if s == nil {
		return map[string]DetectResult{}
	}
	if s.avail == nil {
		s = r.warm(s)
	}

	names := agentNames(s.cfg)
	out := make(map[string]DetectResult, len(names))
	for _, name := range names {
		ac, ok := ResolveAgent(s.cfg, name)
		if !ok {
			continue
		}
		if ac.Type == TypeExec {
			// Built-in, no CLI to look for: decided by the same rule every live probe
			// path shares (lookPathProbe), never by the cache. Costs no syscall.
			out[name] = lookPathProbe(ac)
			continue
		}
		out[name] = s.avail[name] // a key the detect pass never reported is unavailable
	}
	return out
}

// warm probes a COLD snapshot once and memoizes the result into the registry, so a
// registry assembled without a detect pass (NewRegistry) still answers Availability
// from a cache after the first call instead of re-probing forever.
//
// It runs ONE batch detect (parallel, bounded by detectBudget), not a serial probe per
// agent. The memoization is a CompareAndSwap: a Reload that lands mid-probe wins the
// pointer and this stale result is dropped, while this call still returns the snapshot
// it actually probed (never a torn mix).
func (r *Registry) warm(cold *registrySnapshot) *registrySnapshot {
	names := agentNames(cold.cfg)
	candidates := make(map[string]config.AgentConfig, len(names))
	for _, name := range names {
		if ac, ok := ResolveAgent(cold.cfg, name); ok {
			candidates[name] = ac
		}
	}
	avail := DefaultDetector().Detect(candidates)
	if avail == nil {
		avail = map[string]DetectResult{}
	}
	warmed := &registrySnapshot{cfg: cold.cfg, avail: avail}
	r.snap.CompareAndSwap(cold, warmed)
	return warmed
}

// Package config defines the gofer configuration model and the
// loader/writer that resolve, decode, default and persist it. See plan §6.1.
package config

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/acp"
	"github.com/inhere/gofer/internal/tunnel"
)

// Default values used during config loading. See plan §6.1.
const (
	DefaultAddr           = "0.0.0.0:8765"
	DefaultExchangeSubdir = "tmp"
	DefaultResultSubdir   = "gofer"
)

// Config is the top-level gofer configuration. Unknown top-level
// keys present in the source file are preserved on write (see writer.go).
type Config struct {
	Server   ServerConfig             `yaml:"server,omitempty"`
	Log      LogConfig                `yaml:"log,omitempty"`
	Storage  StorageConfig            `yaml:"storage,omitempty"`
	Projects map[string]ProjectConfig `yaml:"projects,omitempty"`
	Agents   map[string]AgentConfig   `yaml:"agents,omitempty"`
	Runners  map[string]RunnerConfig  `yaml:"runners,omitempty"`
	// Roles are named E35 role presets (reviewer/bugfix/…): a base agent + a
	// resident system_prompt + optional default project/tags. `job run --role` /
	// `gofer_run_job(role=)` resolve a role to fill those request fields (design
	// §8.5). Rules/context-file mounting is E11 territory, out of scope here.
	Roles map[string]RoleConfig `yaml:"roles,omitempty"`
	// Supervisor is the OPTIONAL E25 layered-answerer config (design §8.3-8.4). nil
	// (absent) or Enabled=false means no answerer runs — pending interactions wait
	// for a human (the conservative default). Serve constructs supervisor.Service +
	// starts its poller only when Enabled.
	Supervisor *SupervisorConfig `yaml:"supervisor,omitempty"`
	// Presence tunes the E36 driver-agent presence registry / mailbox TTLs and prune
	// cadence. All fields optional; unset (<=0) keeps the package defaults (90s online
	// TTL / 24h message TTL / 60s prune), so an absent `presence:` block changes nothing.
	Presence PresenceConfig `yaml:"presence,omitempty"`
	// Schedule tunes the AUTO-02 cron schedule sweeper. All fields are optional;
	// serve applies conservative defaults when unset.
	Schedule ScheduleConfig `yaml:"schedule,omitempty"`
	// Wakeup tunes the JOB-09 wakeups (event subscriptions / timers that resume a
	// finished job). An absent block keeps the documented default (7 days), so the
	// feature is always on and there is no enable switch to get wrong.
	Wakeup WakeupConfig `yaml:"wakeup,omitempty"`
	// Session tunes the terminal session relay (SESS-01): when a stopping agent
	// session is allowed to wait for a web reply without the human flipping its
	// switch. See SessionConfig.
	Session SessionConfig `yaml:"session,omitempty"`
	// Pty tunes the WEB-03 pty relay's text transcript (PTY-01 §四). An absent
	// block keeps the documented 4MB tail cap, so the transcript is always on and
	// there is no enable switch to get wrong.
	Pty PtyConfig `yaml:"pty,omitempty"`

	// injectedAgents records the Agents keys that were MATERIALIZED AT RUNTIME from
	// the built-in agent templates (agent.Resolve) instead of being authored by the
	// operator. Runtime-only state: unexported, so it is never (de)serialized, and
	// render() strips these keys from Agents before any save.
	//
	// Why this must exist (P2 T0-A): the *Config a Core holds is the SAME pointer the
	// project registry writes back through (project.Registry.save -> config.Save). With
	// no mark, a single "add project" from the web console would freeze every detected
	// template into the operator's config.yaml — promoting it to an explicitly declared
	// agent, which by the iron rule is never detect-gated again. The config would then
	// keep claiming an agent after the CLI is uninstalled, and the whole point of
	// detect-gating the templates would be cancelled by our own write path.
	//
	// Written once by agent.Resolve BEFORE the config is published to any atomic
	// snapshot pointer, and read-only afterwards — so it adds no concurrent write to
	// the shared-snapshot invariant.
	injectedAgents map[string]bool
}

// LogConfig controls the optional rotating JSONL sink.
type LogConfig struct {
	File       string `yaml:"file,omitempty"`
	Dir        string `yaml:"dir,omitempty"`
	MaxSizeMB  int    `yaml:"max_size_mb,omitempty"`
	MaxAgeDays int    `yaml:"max_age_days,omitempty"`
	MaxBackups int    `yaml:"max_backups,omitempty"`
}

// MarkInjectedAgents records which Agents keys were materialized at runtime from a
// built-in template. A nil/empty set clears the mark. agent.Resolve is the only
// intended caller; it is exported solely because the templates live in another
// package.
func (c *Config) MarkInjectedAgents(keys map[string]bool) {
	if c == nil {
		return
	}
	out := make(map[string]bool, len(keys))
	for k, v := range keys {
		if v {
			out[k] = true
		}
	}
	if len(out) == 0 {
		c.injectedAgents = nil
		return
	}
	c.injectedAgents = out
}

// InjectedAgents returns a copy of the runtime-injected agent keys (nil if none).
// These keys are NOT operator configuration and must never be persisted.
func (c *Config) InjectedAgents() map[string]bool {
	if c == nil || len(c.injectedAgents) == 0 {
		return nil
	}
	out := make(map[string]bool, len(c.injectedAgents))
	for k := range c.injectedAgents {
		out[k] = true
	}
	return out
}

// IsInjectedAgent reports whether key was materialized from a built-in template
// rather than declared by the operator.
func (c *Config) IsInjectedAgent(key string) bool {
	if c == nil {
		return false
	}
	return c.injectedAgents[key]
}

// UnmarkInjectedAgent clears the runtime-injected mark of ONE key, so a definition the
// operator has just written for that key is treated as configuration and survives the
// save (config.writer's withoutInjectedAgents strips every marked key).
//
// It is the "declare it explicitly" half of the template escape hatch: editing
// `claude` from the console must produce a `claude:` block in the file, not a write
// that looks successful and is stripped again. Only the config write API calls it, on
// the CLONE it owns.
func (c *Config) UnmarkInjectedAgent(key string) {
	if c == nil || c.injectedAgents == nil || !c.injectedAgents[key] {
		return
	}
	delete(c.injectedAgents, key)
	if len(c.injectedAgents) == 0 {
		c.injectedAgents = nil
	}
}

// Clone returns a copy safe for the P3 copy-on-write write transaction
// (core.Core.Update / reloadLocked): the struct is shallow-copied, then the
// Projects, Agents and injectedAgents maps are deep-copied ONE level so a
// mutation on the clone never touches a map a concurrent reader still holds.
//
// One-level deep is enough because every P3 runtime mutation is a WHOLE-VALUE
// change: project set/delete (Registry.Add/Remove) replaces or drops a whole
// ProjectConfig, and agent.Resolve deletes/inserts whole AgentConfig entries on
// the Agents map — neither edits a value's inner slice in place. So the value
// structs (and the slices they carry, e.g. AllowedAgents) may stay shared with
// the source; only the map headers must be private to the clone. The Agents map
// MUST be deep-copied for the same reason the Projects map is: reloadLocked runs
// agent.Resolve on the clone, which delete()s the previously-injected keys — on a
// shallow-shared map that would tear a running Submit's snapshot.
//
// Deliberately NOT deep-copied (D-MED-7): Storage, Runners, Roles, Supervisor,
// Presence, Schedule. No P3 runtime write path mutates them, so sharing them with
// the source is safe. Extending the write transaction to any of them REQUIRES
// widening this Clone first — today it makes a structural guarantee for "project
// whole-value add/remove" (plus the agent re-resolve that rides every reload) only,
// not for arbitrary mutation.
//
// The Server block IS deep-copied (one level, same rule) since WEB-04③ V1.1: the
// console can edit it through Core.Update, so its pointers/slices/maps must be
// private to the clone like Projects/Agents are — see cloneServer.
func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}
	clone := *c
	clone.Server = cloneServer(c.Server)
	if c.Projects != nil {
		p := make(map[string]ProjectConfig, len(c.Projects))
		for k, v := range c.Projects {
			p[k] = v
		}
		clone.Projects = p
	}
	if c.Agents != nil {
		a := make(map[string]AgentConfig, len(c.Agents))
		for k, v := range c.Agents {
			a[k] = v
		}
		clone.Agents = a
	}
	if c.injectedAgents != nil {
		m := make(map[string]bool, len(c.injectedAgents))
		for k, v := range c.injectedAgents {
			m[k] = v
		}
		clone.injectedAgents = m
	}
	return &clone
}

// cloneServer copies the Server block one level deep: every pointer, slice and map
// a writer could mutate THROUGH (as opposed to replacing wholesale) gets its own
// allocation, so a mutation on a clone can never be observed by a concurrent reader
// holding the previous generation.
//
// The scalar-only sub-blocks (runner_probe, governance, xfer) need nothing beyond
// the struct copy. Callers must not "optimize" this by dropping the pointer copies:
// `next.Server.Retry.MaxAttempts = 3` on a shared pointer would silently edit the
// live config that in-flight Submit calls are reading.
func cloneServer(sc ServerConfig) ServerConfig {
	out := sc
	out.Callers = slices.Clone(sc.Callers)
	if sc.Workers != nil {
		out.Workers = make(map[string]WorkerAuthConfig, len(sc.Workers))
		for k, w := range sc.Workers {
			w.Labels = slices.Clone(w.Labels)
			out.Workers[k] = w
		}
	}
	out.WebEnabled = clonePtr(sc.WebEnabled)
	out.JobRecoverWindowSec = clonePtr(sc.JobRecoverWindowSec)
	out.AutoResumeMax = clonePtr(sc.AutoResumeMax)
	out.DirLock = clonePtr(sc.DirLock)
	out.StallTimeoutSec = clonePtr(sc.StallTimeoutSec)
	out.Metrics.Enabled = clonePtr(sc.Metrics.Enabled)
	if sc.Notification != nil {
		n := *sc.Notification
		n.AllowHosts = slices.Clone(sc.Notification.AllowHosts)
		n.Webhooks = make([]WebhookConfig, len(sc.Notification.Webhooks))
		for i, w := range sc.Notification.Webhooks {
			w.Events = slices.Clone(w.Events)
			w.Projects = slices.Clone(w.Projects)
			n.Webhooks[i] = w
		}
		out.Notification = &n
	}
	if sc.Retry != nil {
		r := *sc.Retry
		r.BackoffSec = slices.Clone(sc.Retry.BackoffSec)
		r.OnExitCodes = slices.Clone(sc.Retry.OnExitCodes)
		out.Retry = &r
	}
	if sc.AgentFallback != nil {
		f := *sc.AgentFallback
		f.OnFailure = clonePtr(sc.AgentFallback.OnFailure)
		out.AgentFallback = &f
	}
	if sc.AgentHealth != nil {
		h := *sc.AgentHealth
		out.AgentHealth = &h
	}
	return out
}

// clonePtr allocates a fresh pointee (nil in, nil out) for cloneServer.
func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// ScheduleConfig controls the AUTO-02 cron sweeper cadence and missed-run policy.
type ScheduleConfig struct {
	SweepIntervalSec int `yaml:"sweep_interval_sec,omitempty"`
	MissGraceSec     int `yaml:"miss_grace_sec,omitempty"`
}

// WakeupConfig is the top-level `wakeup:` block (JOB-09, design §五.1). Only the
// TTL lives here: a wakeup's shape is per-registration, and the sweeper rides the
// existing `schedule:` cadence rather than owning a second interval.
type WakeupConfig struct {
	// TTLSec is how long a wakeup stays armed before the sweeper disables it
	// (0/unset => DefaultWakeupTTLSec, 7 days). A wakeup nobody consumed is a leak
	// in the sweeper's query set, so it expires on its own.
	TTLSec int `yaml:"ttl_sec,omitempty"`
}

// DefaultWakeupTTLSec is the 7-day wakeup lifetime used when `wakeup.ttl_sec` is
// unset (design §五 决策 6).
const DefaultWakeupTTLSec = 7 * 24 * 3600

// WakeupTTL resolves the configured wakeup lifetime; a non-positive value (absent
// block, or an explicit 0 that validate would reject anyway) yields the default.
func (c *Config) WakeupTTL() time.Duration {
	if c == nil || c.Wakeup.TTLSec <= 0 {
		return DefaultWakeupTTLSec * time.Second
	}
	return time.Duration(c.Wakeup.TTLSec) * time.Second
}

// PresenceConfig tunes the E36 presence/mailbox runtime (design §9 / §12 收尾). Every
// field is an OPTIONAL override in seconds; <=0 means "use the built-in default"
// (applied by presence.Service / the serve prune loop, the single source of truth).
// These are read at serve start; changing them needs a restart (not SIGHUP-live).
type PresenceConfig struct {
	// TTLSec: a driver agent is online while last_seen is within this window (default 90s).
	TTLSec int `yaml:"ttl_sec,omitempty"`
	// MessageTTLSec: how long an unread message lives before prune may drop it (default 24h).
	MessageTTLSec int `yaml:"message_ttl_sec,omitempty"`
	// PruneIntervalSec: presence/inbox prune sweeper cadence (default 60s).
	PruneIntervalSec int `yaml:"prune_interval_sec,omitempty"`
}

// SupervisorConfig configures the E25 answerer (design §8.3-8.4 / §11). Defaults
// (interval/max_rounds/escalate_to) are applied in supervisor.NewService, so a
// minimal `supervisor: {enabled: true}` is valid. AllowPromptRegex is the
// auto-answer whitelist: EMPTY means nothing is auto-answered (escalate-only) — the
// honest, opt-in-only default (design §11).
type SupervisorConfig struct {
	Enabled          bool     `yaml:"enabled,omitempty"`
	IntervalSec      int      `yaml:"interval_sec,omitempty"`
	AutoAnswer       bool     `yaml:"auto_answer,omitempty"`
	EscalateTo       string   `yaml:"escalate_to,omitempty"`
	MaxRoundsPerJob  int      `yaml:"max_rounds_per_job,omitempty"`
	AllowPromptRegex []string `yaml:"allow_prompt_regex,omitempty"`
	// OwnerAnswerTimeoutSec bounds how long an interaction may sit escalated to its
	// owner (L1) before the router falls it back past the owner to the global sup
	// (L2) — the owner is会话式 and may have ended without answering (design §8.2,
	// supervisor-routing P2.1). <=0 applies the default (300s) in NewService.
	OwnerAnswerTimeoutSec int `yaml:"owner_answer_timeout_sec,omitempty"`
	// DesiredSupervisors is the event-driven reconciler's CONCURRENCY CAP (y5wt): at most
	// this many ACTIVE (queued/running/pending_interaction) role=supervisor jobs run at once.
	// Dispatch is ON DEMAND, not resident — serve spawns a sup only when there is pending
	// sup-bound work (CountSupPendingDemand>0) and fewer than this many are active, so an idle
	// server spawns ZERO sups (zero claude cost). 1 is the usual value. 0 (default) DISABLES
	// the reconciler (opt-in). >0 requires a roles.supervisor preset (sources the job's agent /
	// system_prompt / env, incl. GOFER_AGENT_ROLE=supervisor).
	DesiredSupervisors int `yaml:"desired_supervisors,omitempty"`
	// ReconcileRunner is the runner the reconciler submits sup jobs to (empty => "local").
	// ReconcileIntervalSec is the BACKSTOP demand-poll cadence (<=0 => 60s default): dispatch
	// is normally wake-driven (the answerer signals on an escalation with no reachable sup),
	// and this periodic CountSupPendingDemand poll only covers a lost wake / a serve restart
	// with pending work — a cheap DB count, so it never needs to be a hot loop.
	ReconcileRunner      string `yaml:"reconcile_runner,omitempty"`
	ReconcileIntervalSec int    `yaml:"reconcile_interval_sec,omitempty"`
	// ReconcilePrompt is the kickoff prompt the reconciler passes to each on-demand sup job
	// (a cli-agent like codex REQUIRES a non-empty prompt — adapter.go). Empty => a built-in
	// supervisor mission (serve.defaultSupReconcilePrompt: peek inbox, answer low-risk, punt
	// high-risk to a human, exit when drained). Guardrails come from roles.supervisor.system_prompt
	// (--append-system-prompt); this is just the "begin supervising" turn.
	ReconcilePrompt string `yaml:"reconcile_prompt,omitempty"`
	// ReconcileJobTimeoutSec is the per-sup-job timeout the reconciler sets. Under event-driven
	// dispatch a healthy sup drains the demand and EXITS early, so this is really a HUNG-sup cap
	// (a wedged sup is force-terminated within it, freeing the active-sup gate for the next
	// on-demand spawn): <=0 => the job-timeout ceiling configured for roles.supervisor.project
	// (Config.EffectiveMaxTimeoutSec — server.max_job_timeout_sec, or that project's
	// max_timeout_sec, else 1h), so raising the ceiling also lifts this cap. Lower it only to
	// recycle a wedged sup sooner.
	ReconcileJobTimeoutSec int `yaml:"reconcile_job_timeout_sec,omitempty"`
}

// RoleConfig is one named role preset (design §8.5). Agent is the base CLI agent
// the role runs on; SystemPrompt is injected via the agent's SystemInject template
// (claude --append-system-prompt). Project/Tags are optional request defaults the
// role fills when the caller leaves them empty.
type RoleConfig struct {
	Agent        string   `yaml:"agent,omitempty"`
	SystemPrompt string   `yaml:"system_prompt,omitempty"`
	Project      string   `yaml:"project,omitempty"`
	Tags         []string `yaml:"tags,omitempty"`
	// Env is an OPTIONAL per-role env preset merged into the job's process env
	// (JobRequest.Env) at submit time, so `--role supervisor` can inject e.g.
	// GOFER_AGENT_ROLE=supervisor into the agent process without a dedicated
	// codex-sup agent (the spawned gofer MCP child inherits it and self-registers
	// role=supervisor, P3). Role.Env fills DEFAULTS — an explicit per-job env value
	// for the same key wins. 勿放 secret：值会随 job.Env 落 request_json（SR403/SR805），
	// secret 应走 agent.env / K8s secret（不落 request_json）。
	Env map[string]string `yaml:"env,omitempty"`
}

// ServerConfig holds HTTP server and auth settings.
type ServerConfig struct {
	Addr            string `yaml:"addr,omitempty"`
	Token           string `yaml:"token,omitempty"`
	TokenEnv        string `yaml:"token_env,omitempty"`
	AllowEmptyToken bool   `yaml:"allow_empty_token,omitempty"`
	// PathView selects which project path the GOFER PROCESS uses as its execution
	// root (E29/D10): "host" (default, empty) => host_path; "container" =>
	// container_path (falling back to host_path when container_path is empty). It
	// is an EXPLICIT operator switch — gofer does NOT self-detect being in a
	// container (no /.dockerenv probing). All gofer-process-side paths (SafeJoin /
	// ExchangeDir / ResultBaseDir / Validate / overlay read dir) go through
	// Config.ExecPath; E21 host-side actions always use host_path (not this).
	PathView string `yaml:"path_view,omitempty"`
	// Callers is the optional multi-caller auth set (C2): each entry maps a
	// bearer token to a caller id stamped onto submitted jobs for audit /
	// per-caller filtering. The legacy single Token/TokenEnv stays valid (treated
	// as caller id "default"); revocation = remove the caller + reload (C3).
	Callers []CallerConfig `yaml:"callers,omitempty"`
	// WebEnabled is a pointer so that "unset" (nil) can default to true while an
	// explicit web_enabled:false disables the embedded web console (see
	// IsWebEnabled and applyDefaults).
	WebEnabled *bool `yaml:"web_enabled,omitempty"`
	// WebDir 指向磁盘上的 web SPA 构建目录（dev：serve --web-dir）。非空则服务从该目录
	// 读取，而非嵌入的 dist；空则用嵌入版。不入持久化配置的常规路径，仅运行期设置。
	WebDir string `yaml:"web_dir,omitempty"`
	// Workers is the per-worker auth/binding set (ws-worker, §7 / review #1):
	// each entry registers a legitimate worker identity keyed by worker_id and
	// binds it to a token. A `register` frame whose worker_id does not match the
	// presented token's bound worker is rejected (hub.Accept). per-worker token
	// is MVP-mandatory: even allow_empty_token does not waive the binding.
	Workers map[string]WorkerAuthConfig `yaml:"workers,omitempty"`
	// RunnerProbe tunes the peer-http active health probe (C6/P4): how often each
	// peer-http runner's /health is polled and the per-probe timeout. Unset =>
	// defaults (30s interval / 5s timeout). The probe only runs when at least one
	// peer-http runner is configured (zero behaviour change otherwise).
	RunnerProbe RunnerProbeConfig `yaml:"runner_probe,omitempty"`
	// Notification is the E14 webhook outbound config (design §5.5). It is a
	// pointer so "unset" (nil) cleanly disables all notification (the serve
	// delivery sweeper does not even start) — zero behaviour change for any config
	// without a `notification` block. When present it lists the webhook targets,
	// the outbound host allowlist and the retry cap.
	Notification *NotificationConfig `yaml:"notification,omitempty"`
	// Metrics is the E16 Prometheus /metrics policy (design §6.2). Enabled is a
	// pointer so "unset" (nil) defaults to ENABLED (the endpoint is mounted) while
	// an explicit enabled:false drops it. Token, when non-empty, re-adds a Bearer
	// check on /metrics (default empty = unauthenticated scrape, guarded by the
	// intranet admission boundary, SR202).
	Metrics MetricsConfig `yaml:"metrics,omitempty"`
	// Governance is the E17 per-caller quota / rate-limit global fallback (design
	// §7.1). It is a pure additive block: an existing config with no `governance`
	// key has all-zero defaults, which means "unlimited" everywhere (向后兼容). A
	// per-caller override on a CallerConfig (> 0) takes precedence; otherwise the
	// governance default applies (see CallerConcurrencyLimit / CallerRate).
	Governance GovernanceConfig `yaml:"governance,omitempty"`
	// WebBaseURL is the address a human uses to reach this console from the
	// outside (e.g. https://gofer.example.com). Only outbound notifications need
	// it — the server cannot derive it from its listen address behind a proxy or
	// a LAN IP. Empty = notifications carry no link.
	WebBaseURL string `yaml:"web_base_url,omitempty"`
	// MaxJobTimeoutSec is the server-wide CEILING on a job's timeout_sec (bd
	// h-aii-s9ck). 0/unset => DefaultMaxJobTimeoutSec (1h), which is exactly the
	// value this clamp used to be hard-coded to, so every existing config behaves
	// as before. It is a ceiling, not a default: a request above it is clamped and
	// the clamp is REPORTED (job.JobResult.RequestedTimeoutSec/TimeoutClamped, plus
	// a CLI warning) so a long job can no longer be truncated silently. Raise it for
	// long-running work; a project overrides it in either direction via
	// ProjectConfig.MaxTimeoutSec (see EffectiveMaxTimeoutSec).
	MaxJobTimeoutSec int `yaml:"max_job_timeout_sec,omitempty"`
	// JobRecoverWindowSec is the RECOV-01 worker reconnect window in seconds: how
	// long a worker's in-flight jobs are held in `recovering` after its connection
	// drops, before they are failed with worker_lost. It is a POINTER so "unset"
	// (nil → DefaultJobRecoverWindowSec, 120s) is distinguishable from an explicit
	// `job_recover_window_sec: 0`, which DISABLES recovery entirely (a disconnect
	// fails the in-flight jobs at once — the pre-RECOV-01 behaviour). Same
	// unset≠zero reasoning as WebEnabled. See Config.JobRecoverWindow.
	JobRecoverWindowSec *int `yaml:"job_recover_window_sec,omitempty"`
	// AutoResumeMax counts automatic session continuations, independently of RetryPolicy.
	// Unset defaults to one; an explicit zero disables automatic resume.
	AutoResumeMax *int `yaml:"auto_resume_max,omitempty"`
	// Retry is the deployment-wide default job retry policy (R2/AUTO-03, design
	// §二.2): a failed job is re-run by the serve sweeper after this policy's
	// backoff, at most MaxAttempts times. A pointer so an absent block means OFF —
	// upgrading gofer never starts re-running anybody's failures (the pre-R2
	// behaviour). The nearer layers (projects.<k>.retry, agents.<k>.retry,
	// JobRequest.Retry / `job run --retry`) each replace it wholesale; see
	// Config.EffectiveRetryPolicy.
	Retry *RetryPolicy `yaml:"retry,omitempty"`
	// AgentFallback is the SUP-01 P3 failover policy (design §一). A pointer so an
	// absent block means the defaults (transfer after a failure ON, submit-time
	// substitution OFF) without any behaviour change for a config that never
	// mentions it — and so the transfer only ever applies to a job that actually
	// resolved a candidate list.
	AgentFallback *AgentFallbackConfig `yaml:"agent_fallback,omitempty"`
	// AgentHealth is the SUP-01 P3 health window/thresholds (design §一). A pointer;
	// an absent block resolves to the documented defaults. It only classifies
	// reported health — nothing in the job path consults it unless pre_dispatch is on.
	AgentHealth *AgentHealthConfig `yaml:"agent_health,omitempty"`
	// Xfer is the XFER-01 transfer policy (design §一.1): the per-transfer byte
	// ceiling and how long a staged/finished transfer survives in the staging area.
	// An absent block (all zero) keeps the documented defaults (256MB / 24h) — the
	// capability is always on, so there is no enable switch to get wrong.
	Xfer XferConfig `yaml:"xfer,omitempty"`
	// DirLock is the JOB-11 same-directory serialization switch. A POINTER so an
	// explicit `dir_lock: false` (every job shares its working directory — the
	// pre-JOB-11 behaviour) is distinguishable from "unset", which defaults to ON
	// (see EffectiveDirLock). Turning it off is the operator's escape hatch for a
	// deployment where the jobs are known not to touch one tree.
	DirLock *bool `yaml:"dir_lock,omitempty"`
	// StallTimeoutSec is the AUTO-05 output-stall window in seconds: a running
	// non-interactive agent job that produces no output for that long is killed and
	// failed as stalled. A POINTER so "unset" (→ DefaultStallTimeoutSec, 900s) is
	// distinguishable from an explicit 0, which turns the watchdog off everywhere.
	// See EffectiveStallTimeoutSec for the full resolution (request > agent > server;
	// exec jobs are off unless someone asks for a window).
	StallTimeoutSec *int `yaml:"stall_timeout_sec,omitempty"`
}

// XferConfig is the server.xfer block (XFER-01, design §一.1/§一.2). Every field is
// optional and resolved at ASSEMBLY time (core.Build), not here: internal/xfer owns
// the defaults and depends on this package, so this one must not import it back.
type XferConfig struct {
	// MaxBytes caps ONE transfer's payload (0 => 256MB).
	MaxBytes int64 `yaml:"max_bytes,omitempty"`
	// TTLSec is how long a staged/finished transfer is kept before the prune sweep
	// expires it and drops its staging directory (0 => 24h).
	TTLSec int `yaml:"ttl_sec,omitempty"`
	// CollectMaxBytes caps what ONE JOB's `--collect` brings back in total
	// (0 => 1GB). Each collected file is still bounded by MaxBytes: this is the sum,
	// so a job cannot fill the server's disk with a hundred just-under-the-cap files.
	// Files beyond it are skipped and listed in the job's xfer.skipped.
	CollectMaxBytes int64 `yaml:"collect_max_bytes,omitempty"`
}

// EffectiveAutoResumeMax preserves the distinction between omitted and explicit zero.
func (sc *ServerConfig) EffectiveAutoResumeMax() int {
	if sc == nil || sc.AutoResumeMax == nil {
		return 1
	}
	return *sc.AutoResumeMax
}

// AgentFallbackConfig is the server.agent_fallback block (SUP-01 P3): the two
// switches of the failover machinery. Both default to the value an operator would
// expect from the design's decision 1 — the transfer runs, the substitution does not.
type AgentFallbackConfig struct {
	// OnFailure moves a job to the next candidate agent after a TRANSIENT failure
	// the same agent cannot continue (SUP-01 P3). Unset means ON: configuring
	// fallback_agents is the opt-in, and the transfer then needs no second switch. A
	// pointer so an explicit false (e.g. an incident where every自动转移 must be off)
	// is distinguishable from "not configured".
	OnFailure *bool `yaml:"on_failure,omitempty"`
	// PreDispatch substitutes a DEGRADED agent at submit time instead of waiting for
	// it to fail (SUP-01 P3). Default OFF: silently running "not the agent I asked
	// for" is a surprising thing to do by default.
	PreDispatch bool `yaml:"pre_dispatch,omitempty"`
}

// AgentHealthConfig is the server.agent_health block (SUP-01 P3): the window and
// thresholds `degraded` is computed from. Zero/unset fields take the defaults below.
type AgentHealthConfig struct {
	// WindowSec bounds the aggregation (0/unset => DefaultAgentHealthWindowSec).
	WindowSec int `yaml:"window_sec,omitempty"`
	// DegradedAfter is how many transient failures inside the window make an agent
	// degraded (0/unset => DefaultAgentDegradedAfter).
	DegradedAfter int `yaml:"degraded_after,omitempty"`
	// RecoverAfterOK is how many successes after the last transient failure restore
	// the agent (0/unset => DefaultAgentRecoverAfterOK).
	RecoverAfterOK int `yaml:"recover_after_ok,omitempty"`
}

// Agent health defaults (SUP-01 P3): an hour is long enough to see "the provider is
// down right now" and short enough that yesterday's outage does not colour today.
const (
	DefaultAgentHealthWindowSec = 3600
	DefaultAgentDegradedAfter   = 3
	DefaultAgentRecoverAfterOK  = 1
)

// AgentFallbackOnFailure resolves whether a transient failure is transferred to the
// next candidate agent. Default ON (design decision 1): a candidate list is the
// opt-in, and an operator who wrote one wants it used.
func (c *Config) AgentFallbackOnFailure() bool {
	if c == nil || c.Server.AgentFallback == nil || c.Server.AgentFallback.OnFailure == nil {
		return true
	}
	return *c.Server.AgentFallback.OnFailure
}

// AgentFallbackPreDispatch resolves whether Submit substitutes a degraded agent
// before dispatching. Default OFF (design decision 1).
func (c *Config) AgentFallbackPreDispatch() bool {
	return c != nil && c.Server.AgentFallback != nil && c.Server.AgentFallback.PreDispatch
}

// EffectiveDirLock resolves the JOB-11 same-directory serialization switch: an
// explicit `server.dir_lock` wins, an unset one means ON (a deployment that never
// mentioned it gets the safe behaviour — adjacent writable agent jobs queue instead
// of editing one tree at once). Turning it off makes every job shared.
func (c *Config) EffectiveDirLock() bool {
	if c == nil || c.Server.DirLock == nil {
		return true
	}
	return *c.Server.DirLock
}

// DefaultStallTimeoutSec is the AUTO-05 output-stall window an unset configuration
// gets: 15 minutes of total silence from a running agent job means the provider
// stream hung (or the CLI is waiting for an interaction nobody will ever send), and
// waiting for the job's own deadline (default 1h) wastes the whole slot.
const DefaultStallTimeoutSec = 900

// EffectiveStallTimeoutSec resolves the AUTO-05 output-stall window in seconds for a
// job, or 0 for "do not watch it":
//
//	interactive job                     → 0 (a human drives it; silence is normal)
//	request (--stall-timeout)           → its value, 0 turning the watchdog off
//	agents.<key>.stall_timeout_sec      → its value, 0 turning the watchdog off
//	agentType == "exec"                 → 0 (builds and test suites are silent by nature)
//	server.stall_timeout_sec            → its value, 0 turning the watchdog off
//	otherwise                           → DefaultStallTimeoutSec
//
// agentType comes from agent.ResolveAgent at the call site (this package cannot
// import internal/agent without a cycle), and agentKey is the RESOLVED agent of the
// job so a role/template-filled agent is resolved on the agent that actually runs.
func (c *Config) EffectiveStallTimeoutSec(agentKey, agentType string, requested *int, interactive bool) int {
	if interactive {
		return 0
	}
	if requested != nil {
		return *requested
	}
	if c != nil {
		if a, ok := c.Agents[agentKey]; ok && a.StallTimeoutSec != nil {
			return *a.StallTimeoutSec
		}
	}
	if agentType == "exec" {
		return 0
	}
	if c != nil && c.Server.StallTimeoutSec != nil {
		return *c.Server.StallTimeoutSec
	}
	return DefaultStallTimeoutSec
}

// EffectiveAgentHealth resolves the health window/thresholds, filling unset fields
// with the defaults. A non-positive value reads as unset (there is no meaningful
// "zero-length window" or "zero failures means degraded").
func (c *Config) EffectiveAgentHealth() AgentHealthConfig {
	h := AgentHealthConfig{WindowSec: DefaultAgentHealthWindowSec, DegradedAfter: DefaultAgentDegradedAfter, RecoverAfterOK: DefaultAgentRecoverAfterOK}
	if c == nil || c.Server.AgentHealth == nil {
		return h
	}
	if c.Server.AgentHealth.WindowSec > 0 {
		h.WindowSec = c.Server.AgentHealth.WindowSec
	}
	if c.Server.AgentHealth.DegradedAfter > 0 {
		h.DegradedAfter = c.Server.AgentHealth.DegradedAfter
	}
	if c.Server.AgentHealth.RecoverAfterOK > 0 {
		h.RecoverAfterOK = c.Server.AgentHealth.RecoverAfterOK
	}
	return h
}

// AgentFallbacksFor resolves the ordered candidate list for an agent in a project:
// the project's agent_fallbacks override when it names that agent, else the agent's
// own fallback_agents. It reads configuration only — whether a candidate is
// ADMITTED (project allowed_agents) is decided at submit time, where the project and
// the request are both known.
func (c *Config) AgentFallbacksFor(projectKey, agentKey string) []string {
	if c == nil {
		return nil
	}
	if p, ok := c.Projects[projectKey]; ok {
		if list, ok := p.AgentFallbacks[agentKey]; ok {
			return list
		}
	}
	return c.Agents[agentKey].FallbackAgents
}

// PtyConfig is the pty relay's transcript block (PTY-01 §四).
type PtyConfig struct {
	// TranscriptMaxBytes caps the de-ANSI'd text transcript kept for one
	// interactive pty session (<result_dir>/pty.txt). It bounds the FILE too, not
	// just memory: the sink keeps the tail. 0 / unset => the documented 4MB.
	TranscriptMaxBytes int64 `yaml:"transcript_max_bytes,omitempty"`
}

// DefaultPtyTranscriptMaxBytes is the transcript tail cap when
// pty.transcript_max_bytes is unset (PTY-01 decision 3: the transcript is on by
// default, so the cap is the only knob).
const DefaultPtyTranscriptMaxBytes = 4 << 20

// EffectivePtyTranscriptMaxBytes resolves the transcript cap in bytes.
func (c *Config) EffectivePtyTranscriptMaxBytes() int64 {
	if c == nil || c.Pty.TranscriptMaxBytes <= 0 {
		return DefaultPtyTranscriptMaxBytes
	}
	return c.Pty.TranscriptMaxBytes
}

// SessionConfig tunes the terminal session relay's automatic arming (R2). Both
// keys are POINTERS so "unset" (nil → the default below) is distinguishable from
// an explicit `0`, which turns that criterion OFF — the per-session switch is
// then the only gate for it. Same unset≠zero reasoning as JobRecoverWindowSec.
type SessionConfig struct {
	// AutoRelayIdleSec is the keyboard idle threshold in seconds: when the hooks
	// report the machine has seen no keyboard/mouse input for at least this
	// long, a stopping session waits for a web reply even though nobody flipped
	// its switch (and is released as soon as the human is back).
	AutoRelayIdleSec *int `yaml:"auto_relay_idle_sec,omitempty"`
	// AutoRelayTurnSec is the fallback threshold in seconds measured from the
	// session's last HUMAN input: on a terminal whose idle cannot be probed at
	// all (a container without X11 keeps reporting idle=-1), a session that has
	// seen no human input for this long waits anyway. It exists because the
	// keyboard lives on the host while the hook runs in the container.
	AutoRelayTurnSec *int `yaml:"auto_relay_turn_sec,omitempty"`
	// InjectCommands is the foreground-process whitelist of the tmux delivery
	// path (session relay §9.1 A, `gofer session say --deliver`): a reply is typed
	// into a pane only when the CLI sitting there is one of these. Empty keeps the
	// built-in list (claude|codex|omp|node|gemini|opencode) — the guard exists
	// because the human may have that pane in an editor by the time the web sends
	// a message.
	InjectCommands []string `yaml:"inject_commands,omitempty"`
	// TakeoverInputDelayMs is path B's priming delay (design §9.1 B): how long
	// the resumed TUI is given to settle — after its FIRST output and then with no
	// new output for this long — before the web reply is typed into it. Unset
	// keeps the default; 0 types the reply as soon as the TUI has drawn anything.
	TakeoverInputDelayMs *int `yaml:"takeover_input_delay_ms,omitempty"`
	// AutoRelaySkipWhenSupervising (SUP-01 D, bd h-aii-s2v4) keeps the auto rules
	// from arming a session while its caller still has jobs in flight: whoever
	// drives that caller is watching a job, not away, and an armed Stop would
	// block the hook (and the job's completion notice) until they came back.
	// Pointer so unset means ON and an explicit false opts out; it only ever
	// applies to `auto` — `on` stays the human's explicit "wait for me".
	AutoRelaySkipWhenSupervising *bool `yaml:"auto_relay_skip_when_supervising,omitempty"`
	// SupervisingWindowSec is how far back that gate looks for the caller's live
	// jobs (default DefaultSessionSupervisingWindowSec; 0 = no window, every live
	// job counts). It bounds a stale job no human is actually watching any more.
	SupervisingWindowSec *int `yaml:"supervising_window_sec,omitempty"`
}

// DefaultSessionAutoRelayIdleSec is the idle-detection auto-arm threshold used
// when session.auto_relay_idle_sec is unset: 5 minutes away from the keyboard is
// long enough to mean "not coming back in a moment".
const DefaultSessionAutoRelayIdleSec = 300

// DefaultSessionAutoRelayTurnSec is the last-human-input fallback threshold used
// when session.auto_relay_turn_sec is unset: 15 minutes of silence from the
// human in this session.
const DefaultSessionAutoRelayTurnSec = 900

// EffectiveAutoRelayIdleSec resolves the keyboard idle threshold (seconds) from
// the top-level `session.auto_relay_idle_sec` (the only spelling since v0.48;
// the pre-R2 `server.session_auto_relay_idle_sec` alias is now a load error, see
// RejectRemovedKeys). Unset keeps the default; 0 = that criterion disabled.
func (c *Config) EffectiveAutoRelayIdleSec() int {
	if c == nil || c.Session.AutoRelayIdleSec == nil {
		return DefaultSessionAutoRelayIdleSec
	}
	return *c.Session.AutoRelayIdleSec
}

// EffectiveAutoRelayTurnSec resolves the last-human-input fallback threshold
// (seconds); 0 = that criterion disabled.
func (c *Config) EffectiveAutoRelayTurnSec() int {
	if c == nil || c.Session.AutoRelayTurnSec == nil {
		return DefaultSessionAutoRelayTurnSec
	}
	return *c.Session.AutoRelayTurnSec
}

// DefaultSessionSupervisingWindowSec is how far back the supervision gate looks
// for the caller's live jobs when session.supervising_window_sec is unset: two
// hours is longer than any job a human watches in one sitting, and short enough
// that a forgotten job does not keep their sessions unarmed forever.
const DefaultSessionSupervisingWindowSec = 7200

// EffectiveAutoRelaySkipWhenSupervising resolves the SUP-01 D gate: true (the
// default) keeps the auto rules from arming a session whose caller has live jobs.
func (c *Config) EffectiveAutoRelaySkipWhenSupervising() bool {
	if c == nil || c.Session.AutoRelaySkipWhenSupervising == nil {
		return true
	}
	return *c.Session.AutoRelaySkipWhenSupervising
}

// EffectiveSessionSupervisingWindowSec resolves the gate's look-back window in
// seconds; 0 = no window (every live job of the caller counts).
func (c *Config) EffectiveSessionSupervisingWindowSec() int {
	if c == nil || c.Session.SupervisingWindowSec == nil {
		return DefaultSessionSupervisingWindowSec
	}
	return *c.Session.SupervisingWindowSec
}

// DefaultSessionTakeoverInputDelayMs is path B's priming delay when
// session.takeover_input_delay_ms is unset: a terminal that has stopped
// painting for 1.5s has drawn its prompt, and typing earlier would race the
// TUI's own startup output.
const DefaultSessionTakeoverInputDelayMs = 1500

// EffectiveSessionTakeoverInputDelayMs resolves path B's quiet window in
// milliseconds (see SessionConfig.TakeoverInputDelayMs).
func (c *Config) EffectiveSessionTakeoverInputDelayMs() int {
	if c == nil || c.Session.TakeoverInputDelayMs == nil {
		return DefaultSessionTakeoverInputDelayMs
	}
	return *c.Session.TakeoverInputDelayMs
}

// GovernanceConfig is the E17 global fallback for per-caller quotas (design
// §7.1). It applies to a caller only when that caller has not set its own
// override (CallerConfig.MaxConcurrentJobs / RateLimit). All fields default to 0
// = unlimited, so a config with no `governance` block keeps the legacy
// no-throttle behaviour.
type GovernanceConfig struct {
	// DefaultCallerMaxConcurrent caps how many jobs a caller may run at once when
	// the caller has no own MaxConcurrentJobs. 0 = unlimited.
	DefaultCallerMaxConcurrent int `yaml:"default_caller_max_concurrent,omitempty"`
	// DefaultRateLimit is the per-second submit rate (token-bucket refill) when the
	// caller has no own RateLimit. 0 = unlimited (no rate gating).
	DefaultRateLimit float64 `yaml:"default_rate_limit,omitempty"`
	// DefaultRateBurst is the token-bucket capacity when the caller has no own
	// RateBurst. <= 0 falls back to max(1, ceil(rate)) at use time (CallerRate).
	DefaultRateBurst int `yaml:"default_rate_burst,omitempty"`
	// RequireAnswerCapability gates interaction answer/punt to callers with
	// can_answer:true. false = any authenticated caller may answer (legacy).
	RequireAnswerCapability bool `yaml:"require_answer_capability,omitempty"`
	// RequireAdminCapability gates config/project edits to callers with
	// can_admin:true. false = any authenticated caller may edit config (legacy).
	RequireAdminCapability bool `yaml:"require_admin_capability,omitempty"`
	// RequireAttachCapability gates interactive attach to callers with
	// can_attach:true. false = any authenticated caller may attach (legacy).
	RequireAttachCapability bool `yaml:"require_attach_capability,omitempty"`
	RequireTunnelCapability bool `yaml:"require_tunnel_capability,omitempty"`
	// AttachOrigins is the Origin allowlist for attach websocket requests.
	AttachOrigins []string `yaml:"attach_origins,omitempty"`
}

// MetricsConfig is the E16 Prometheus /metrics policy (design §6.2). It is a
// minimal additive block: an existing config with no `metrics` key keeps the
// endpoint enabled and unauthenticated (IsEnabled defaults nil→true).
type MetricsConfig struct {
	// Enabled gates the /metrics endpoint. Unset (nil) defaults to true; an
	// explicit enabled:false drops the route entirely.
	Enabled *bool `yaml:"enabled,omitempty"`
	// Token, when non-empty, requires `Authorization: Bearer <token>` on /metrics
	// (for environments that want authenticated scraping). Empty = no auth.
	Token string `yaml:"token,omitempty"`
}

// IsEnabled reports whether the /metrics endpoint should be mounted. Unset (nil)
// defaults to true; an explicit enabled:false disables it.
func (m MetricsConfig) IsEnabled() bool { return m.Enabled == nil || *m.Enabled }

// NotificationConfig is the E14 webhook outbound policy (design §5.5/§5.7). It
// holds every configured webhook target plus the shared outbound-safety knobs:
// AllowHosts is the host allowlist a webhook URL must match, AllowHTTP relaxes
// the https-only default (for local testing only), and MaxAttempts caps the
// retry backoff. The delivery sweeper only runs when this is non-nil AND has at
// least one webhook (see serve startDeliveryLoop).
type NotificationConfig struct {
	Webhooks    []WebhookConfig `yaml:"webhooks,omitempty"`
	AllowHosts  []string        `yaml:"allow_hosts,omitempty"`  // outbound host allowlist (SR904)
	AllowHTTP   bool            `yaml:"allow_http,omitempty"`   // default false => https-only
	MaxAttempts int             `yaml:"max_attempts,omitempty"` // <= 0 => DefaultMaxAttempts
}

// WebhookConfig is one E14 outbound webhook target (design §5.5). Events is the
// subscribed trigger set (omit => the default set job.terminal + interaction.created);
// SecretEnv names the env var holding the HMAC secret (SR403, never inlined);
// Projects restricts the webhook to those project keys (omit => all projects).
// Webhook kinds (OBS-07a): the outbound adapter a webhook speaks. The list lives
// here, next to the yaml field it validates, because internal/notify imports
// this package (the reverse would be an import cycle).
const (
	WebhookKindGeneric  = "generic"
	WebhookKindDingTalk = "dingtalk"
	WebhookKindFeishu   = "feishu"
)

// ValidWebhookKind reports whether k names a supported adapter ("" = generic).
func ValidWebhookKind(k string) bool {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "", WebhookKindGeneric, WebhookKindDingTalk, WebhookKindFeishu:
		return true
	}
	return false
}

type WebhookConfig struct {
	URL    string   `yaml:"url,omitempty"`
	Events []string `yaml:"events,omitempty"`
	// SecretEnv names the env var holding the shared secret. Its meaning follows
	// Kind: generic → HMAC of the body in the X-Gofer-Signature header; dingtalk /
	// feishu → the bot's 加签 secret (empty when the bot uses keyword or IP
	// allow-list security instead).
	SecretEnv string   `yaml:"secret_env,omitempty"`
	Projects  []string `yaml:"projects,omitempty"`
	// Kind selects the outbound adapter (OBS-07a; see WebhookKind* / ValidWebhookKind): "" / generic keeps the original
	// `{event, job}` JSON contract; dingtalk / feishu render the provider's own bot
	// message so a group robot shows a readable card.
	Kind string `yaml:"kind,omitempty"`
}

// DefaultMaxAttempts is the delivery retry cap used when NotificationConfig
// .MaxAttempts is unset (<= 0): the backoff table has 5 steps, so 6 attempts
// (initial + 5 retries) exhausts it before the delivery is marked failed.
const DefaultMaxAttempts = 6

// EffectiveMaxAttempts returns the configured retry cap, defaulting to
// DefaultMaxAttempts when unset (<= 0).
func (n *NotificationConfig) EffectiveMaxAttempts() int {
	if n != nil && n.MaxAttempts > 0 {
		return n.MaxAttempts
	}
	return DefaultMaxAttempts
}

// RunnerProbeConfig is the YAML form of the peer-http health-probe policy (C6/P4
// §6). Both fields default when <= 0: 30s interval, 5s per-probe timeout. It is a
// pure additive block — an existing config with no runner_probe key probes at the
// defaults.
type RunnerProbeConfig struct {
	IntervalSeconds int `yaml:"interval_seconds,omitempty"`
	TimeoutSeconds  int `yaml:"timeout_seconds,omitempty"`
}

// ProbeInterval returns the peer-http probe cadence, defaulting to 30s when the
// configured interval is <= 0.
func (p RunnerProbeConfig) ProbeInterval() time.Duration {
	if p.IntervalSeconds > 0 {
		return time.Duration(p.IntervalSeconds) * time.Second
	}
	return 30 * time.Second
}

// ProbeTimeout returns the per-probe timeout, defaulting to 5s when the
// configured timeout is <= 0.
func (p RunnerProbeConfig) ProbeTimeout() time.Duration {
	if p.TimeoutSeconds > 0 {
		return time.Duration(p.TimeoutSeconds) * time.Second
	}
	return 5 * time.Second
}

// WorkerAuthConfig registers one legitimate worker identity on the server side
// (ws-worker §7 / review #1). Token is the literal bearer token; TokenEnv reads
// it from the named environment variable instead (so the secret stays out of the
// config file). The worker_id (the map key) is used as the caller id for jobs it
// runs. Labels are display/scheduling hints only (WP4 auto-scheduling).
type WorkerAuthConfig struct {
	Token    string   `yaml:"token,omitempty"`
	TokenEnv string   `yaml:"token_env,omitempty"`
	Labels   []string `yaml:"labels,omitempty"`
}

// CallerConfig identifies one authenticated submitter (C2). Token is the literal
// bearer token; TokenEnv reads it from the named environment variable instead
// (so secrets stay out of the config file). ID is recorded on the caller's jobs.
type CallerConfig struct {
	ID       string `yaml:"id,omitempty"`
	Token    string `yaml:"token,omitempty"`
	TokenEnv string `yaml:"token_env,omitempty"`
	// CanAnswer permits this caller to answer/punt interactions when
	// governance.require_answer_capability is enabled.
	CanAnswer bool `yaml:"can_answer,omitempty"`
	// CanAdmin permits this caller to edit config/projects when
	// governance.require_admin_capability is enabled.
	CanAdmin bool `yaml:"can_admin,omitempty"`
	// CanAttach permits this caller to attach to interactive sessions when
	// governance.require_attach_capability is enabled.
	CanAttach bool `yaml:"can_attach,omitempty"`
	// CanTunnel permits TCP tunnel connections when governance requires it.
	CanTunnel bool `yaml:"can_tunnel,omitempty"`
	// E17 per-caller quota overrides (design §7.1). Each 0/empty value falls back to
	// the server.governance default; if that is also 0 the dimension is unlimited
	// (向后兼容). A value > 0 wins over the governance default.
	MaxConcurrentJobs int     `yaml:"max_concurrent_jobs,omitempty"` // 同时在跑上限(信号量排队语义,超额排队不拒)
	RateLimit         float64 `yaml:"rate_limit,omitempty"`          // 每秒提交请求数(令牌桶速率); 0 = 不限
	RateBurst         int     `yaml:"rate_burst,omitempty"`          // 桶容量(突发); <=0 时取 max(1, ceil(RateLimit))
}

// CallerConcurrencyLimit resolves the effective per-caller concurrent-jobs cap
// (E17, design §7.2): the caller's own MaxConcurrentJobs (> 0) wins, else the
// server.governance default, else 0 (unlimited). An empty callerID skips the
// per-caller lookup and uses the governance default directly.
func (sc *ServerConfig) CallerConcurrencyLimit(callerID string) int {
	if callerID != "" {
		for _, cc := range sc.Callers {
			if cc.ID == callerID && cc.MaxConcurrentJobs > 0 {
				return cc.MaxConcurrentJobs
			}
		}
	}
	return sc.Governance.DefaultCallerMaxConcurrent
}

// CallerCanAnswer reports whether callerID has the can_answer capability bit.
// The governance gate itself is checked by callers of this helper.
func (sc *ServerConfig) CallerCanAnswer(callerID string) bool {
	if callerID != "" {
		for _, cc := range sc.Callers {
			if cc.ID == callerID {
				return cc.CanAnswer
			}
		}
	}
	return false
}

// CallerCanAdmin reports whether callerID has the can_admin capability bit.
// The governance gate itself is checked by callers of this helper.
func (sc *ServerConfig) CallerCanAdmin(callerID string) bool {
	if callerID != "" {
		for _, cc := range sc.Callers {
			if cc.ID == callerID {
				return cc.CanAdmin
			}
		}
	}
	return false
}

// CallerCanAttach reports whether callerID has the can_attach capability bit.
// The governance gate itself is checked by callers of this helper.
func (sc *ServerConfig) CallerCanAttach(callerID string) bool {
	if callerID != "" {
		for _, cc := range sc.Callers {
			if cc.ID == callerID {
				return cc.CanAttach
			}
		}
	}
	return false
}

// CallerCanTunnel reports whether callerID has tunnel capability.
func (sc *ServerConfig) CallerCanTunnel(callerID string) bool {
	if callerID == "" {
		return false
	}
	for _, cc := range sc.Callers {
		if cc.ID == callerID {
			return cc.CanTunnel
		}
	}
	return false
}

// CallerRate resolves the effective per-caller submit rate (E17, design §7.3):
// the caller's own RateLimit (> 0) wins, else the governance DefaultRateLimit,
// else 0 (no rate gating). burst follows the same caller→governance precedence;
// when the resolved burst is <= 0 (and rps > 0) it defaults to max(1, ceil(rps))
// so a configured rate always has a usable bucket. An empty callerID uses the
// governance defaults directly.
func (sc *ServerConfig) CallerRate(callerID string) (rps float64, burst int) {
	if callerID != "" {
		for _, cc := range sc.Callers {
			if cc.ID == callerID {
				if cc.RateLimit > 0 {
					rps = cc.RateLimit
				}
				if cc.RateBurst > 0 {
					burst = cc.RateBurst
				}
				break
			}
		}
	}
	if rps <= 0 {
		rps = sc.Governance.DefaultRateLimit
	}
	if burst <= 0 {
		burst = sc.Governance.DefaultRateBurst
	}
	if rps <= 0 {
		return 0, 0 // no rate gating; burst is irrelevant.
	}
	if burst <= 0 {
		burst = int(math.Ceil(rps))
		if burst < 1 {
			burst = 1
		}
	}
	return rps, burst
}

// IsWebEnabled reports whether the web console should be mounted. Unset (nil)
// defaults to true; an explicit web_enabled:false disables it.
func (sc ServerConfig) IsWebEnabled() bool { return sc.WebEnabled == nil || *sc.WebEnabled }

// StorageConfig holds defaults for the per-project exchange/result subdirs and
// an optional global store root. When Root is empty (default), each project
// stores results under its own exchange subdir; when Root is set it becomes a
// global store keyed by project (see ResultBaseDir in internal/project).
type StorageConfig struct {
	DefaultExchangeSubdir string `yaml:"default_exchange_subdir,omitempty"`
	DefaultResultSubdir   string `yaml:"default_result_subdir,omitempty"`
	Root                  string `yaml:"root,omitempty"`
	// DBPath is the optional explicit path to the SQLite metadata database. When
	// empty it is resolved by ResolveDBPath from Root / the config dir.
	DBPath string `yaml:"db_path,omitempty"`
	// Retention bounds how many terminal jobs (and their logs) are kept; the
	// periodic prune in serve enforces it. Unset (all fields <= 0) disables prune.
	Retention RetentionConfig `yaml:"retention,omitempty"`
	Cast      CastConfig      `yaml:"cast,omitempty"`
}

const castDefaultTTLHours = 24
const castMaxTTLHours = 168 // 7d 上限

// castPlaintextMaxTTLHours caps plaintext (unencrypted) cast retention: a
// plaintext recording may carry keystrokes (tokens/passwords), so it is only
// allowed a short TTL. Reuses castDefaultTTLHours (24h). Longer retention
// requires encryption (loader combination check, D-P3-5 / K4).
const castPlaintextMaxTTLHours = castDefaultTTLHours

// castMinSecretBytes is the minimum decoded length of the cast encryption key
// (256-bit) — HKDF is NOT a password KDF, so a short passphrase must be
// rejected. The decode+length check runs at serve start (T4, D-P3-4); this
// constant is placed here so both loader and serve share one source of truth.
const castMinSecretBytes = 32

type CastConfig struct {
	// Enabled turns cast recording on. It defaults to false (opt-in, G023): an
	// existing config that never wrote `enabled` decodes to false, records
	// nothing and stays zero-regression. It is the single "is recording on"
	// predicate (handler sink, prune-loop gate, recording_uri) — RetentionTTLHours
	// no longer expresses "disabled".
	Enabled           bool                 `yaml:"enabled,omitempty"`
	RetentionTTLHours int                  `yaml:"retention_ttl_hours,omitempty"` // 0=用默认 24（仅 Enabled 时）
	Encryption        CastEncryptionConfig `yaml:"encryption,omitempty"`
}

type CastEncryptionConfig struct {
	Enabled bool   `yaml:"enabled,omitempty"`
	KeyEnv  string `yaml:"key_env,omitempty"` // 从环境变量取 key，不进项目文件
}

// RetentionConfig is the YAML form of the job retention policy enforced by the
// serve prune loop (design §13 SP5). All fields default to 0 (disabled): with no
// retention configured the server never prunes (zero behaviour change).
type RetentionConfig struct {
	// MaxAgeDays, when > 0, prunes terminal jobs older than this many days.
	MaxAgeDays int `yaml:"max_age_days,omitempty"`
	// MaxCount, when > 0, keeps only the newest MaxCount terminal jobs.
	MaxCount int `yaml:"max_count,omitempty"`
	// IntervalMinutes is the prune cadence; <= 0 falls back to a default (60m) in
	// the serve loop. Only consulted when MaxAgeDays or MaxCount is > 0.
	IntervalMinutes int `yaml:"prune_interval_minutes,omitempty"`
	// WorkflowMaxAgeDays is the INDEPENDENT workflow retention age (P1, design §5.4
	// / D22): when > 0, terminal workflows (done/failed/cancelled) older than this
	// many days are pruned along with their step-jobs and workflow_events. When 0 it
	// falls back to MaxAgeDays (WorkflowMaxAge), so a single job age policy also
	// bounds workflows; set it explicitly to keep workflows longer/shorter than jobs.
	WorkflowMaxAgeDays int `yaml:"workflow_max_age_days,omitempty"`
}

// Enabled reports whether any retention bound is set (so the serve prune loop
// should run). The interval alone does not enable prune. Workflow retention rides
// the same loop, so WorkflowMaxAgeDays also enables it (a config that only sets
// workflow retention still runs the prune loop).
func (r RetentionConfig) Enabled() bool {
	return r.MaxAgeDays > 0 || r.MaxCount > 0 || r.WorkflowMaxAgeDays > 0
}

// WorkflowMaxAge converts the effective workflow retention age into a Duration: the
// explicit WorkflowMaxAgeDays when > 0, else a fallback to MaxAgeDays (jobs). 0
// days => 0 (no workflow age bound). The job package maps this onto a
// jobstore.WorkflowRetentionPolicy (config stays jobstore-free).
func (r RetentionConfig) WorkflowMaxAge() time.Duration {
	days := r.WorkflowMaxAgeDays
	if days <= 0 {
		days = r.MaxAgeDays
	}
	if days > 0 {
		return time.Duration(days) * 24 * time.Hour
	}
	return 0
}

// MaxAge converts MaxAgeDays into a time.Duration (0 days => 0, i.e. no age
// bound). The job package maps this onto a jobstore.RetentionPolicy — config
// stays free of a jobstore dependency so it remains a leaf imported everywhere.
func (r RetentionConfig) MaxAge() time.Duration {
	if r.MaxAgeDays > 0 {
		return time.Duration(r.MaxAgeDays) * 24 * time.Hour
	}
	return 0
}

// PruneInterval returns the prune cadence, defaulting to 60 minutes when the
// configured interval is <= 0.
func (r RetentionConfig) PruneInterval() time.Duration {
	if r.IntervalMinutes > 0 {
		return time.Duration(r.IntervalMinutes) * time.Minute
	}
	return 60 * time.Minute
}

// ProjectConfig describes a single registered project. ExchangeSubdir and
// ResultSubdir may be empty; they fall back to the storage defaults at resolve
// time (see ResolvedExchangeSubdir/ResolvedResultSubdir).
type ProjectConfig struct {
	HostPath       string   `yaml:"host_path,omitempty"`
	ContainerPath  string   `yaml:"container_path,omitempty"`
	ExchangeSubdir string   `yaml:"exchange_subdir,omitempty"`
	ResultSubdir   string   `yaml:"result_subdir,omitempty"`
	DefaultAgent   string   `yaml:"default_agent,omitempty"`
	AllowedAgents  []string `yaml:"allowed_agents,omitempty"`
	// AgentFallbacks overrides an agent's fallback_agents list for THIS project
	// (SUP-01 P3): keyed by the failing agent, the value is the ordered candidate
	// list. A key present here REPLACES the agent-level list (it does not merge), so
	// a project can narrow, reorder or disable (empty list) a transfer per agent.
	AgentFallbacks map[string][]string `yaml:"agent_fallbacks,omitempty"`
	// AllowInteractive is the project's interactive-job switch (AGT-02 §2) and the
	// ONLY project-level gate: the removed legacy narrowing list has no successor, and a
	// project that wants to exclude an agent simply gives it no interactive mode. It is a
	// pointer so "unset" stays distinguishable from an explicit false: an explicit
	// allow_interactive:false must keep interactive jobs rejected, and nil (the zero
	// value of a project built in code, e.g. by the worker
	// policy projection) reads as false, never as "inherit". See IsInteractiveAllowed.
	AllowInteractive  *bool    `yaml:"allow_interactive,omitempty"`
	AllowedRunners    []string `yaml:"allowed_runners,omitempty"`
	AllowExec         bool     `yaml:"allow_exec,omitempty"`
	MaxConcurrentJobs int      `yaml:"max_concurrent_jobs,omitempty"`
	// MaxTimeoutSec overrides the job-timeout ceiling for THIS project (bd
	// h-aii-s9ck). 0/unset => inherit server.max_job_timeout_sec (or its default);
	// a non-zero value REPLACES it in EITHER direction — raising it above the
	// server value (one project runs 2h builds without lifting the ceiling for
	// everyone) or lowering it (a tenant that must not exceed 10m). See
	// Config.EffectiveMaxTimeoutSec, the single source of truth for the resolution.
	MaxTimeoutSec int `yaml:"max_timeout_sec,omitempty"`
	// CaptureDiff toggles E12 git-diff capture (job-outcomes-audit, P3). It is a
	// pointer so "unset" (nil) can default to "on when cwd is a git work tree"
	// while an explicit capture_diff:false disables it outright. nil/true defer to
	// captureDiff's own is-git probe (a non-git cwd naturally yields no diff).
	CaptureDiff *bool `yaml:"capture_diff,omitempty"`
	// NotifyEnabled gates E14 webhook delivery for this project (design §5.5). It
	// is a pointer so "unset" (nil) defaults to ENABLED while an explicit
	// notify_enabled:false suppresses all notification for the project's jobs
	// (no deliveries are enqueued). nil/true => notification on.
	NotifyEnabled *bool `yaml:"notify_enabled,omitempty"`
	// WorktreeDefault turns WT-01 managed worktrees ON for every job of this project
	// (a per-job --worktree is then redundant). The resolved decision rides the
	// request, so a worker executes exactly what the submitter decided.
	WorktreeDefault bool `yaml:"worktree_default,omitempty"`
	// RequireReview (GATE-01 S3) turns人工验收 ON for every job of this project: a job
	// whose agent finishes normally parks in `needs_review` until a human accepts or
	// rejects it. A per-job --review is then redundant, and a workflow step may still
	// override it explicitly in either direction (StepSpec.Review). Default off, so
	// upgrading never strands a project's jobs on a human.
	RequireReview bool `yaml:"require_review,omitempty"`
	// Approval is the project's run-time approval gate (GATE-01 §1): how an
	// acp-agent's session/request_permission is answered. Nil means the defaults,
	// and the default mode is off (= the pre-GATE behaviour: the agent's own
	// permission handling decides, gofer auto-allows) so upgrading cannot strand
	// existing jobs on a human. See ApprovalPolicy.
	Approval *ApprovalConfig `yaml:"approval,omitempty"`
	// Verify is the project's DEFAULT verify step (SUP-01 B): an argv run after the
	// agent finishes normally, on the executing machine, in the job's cwd/env — the
	// machine-checkable acceptance an agent's own report cannot provide. It applies
	// to every job of the project unless the caller passes --no-verify, and it needs
	// allow_exec (the argv comes from the submitter, the same trust surface as an
	// exec job's). Empty = no default. See JobRequest.Verify.
	Verify []string `yaml:"verify,omitempty"`
	// VerifyTimeoutSec bounds that step (SUP-01 B): 0/unset => DefaultVerifyTimeoutSec.
	// It is independent of the job's own timeout — a hung test suite must not be able
	// to eat the agent's whole budget — and a step that exceeds it fails the job.
	VerifyTimeoutSec int `yaml:"verify_timeout_sec,omitempty"`
	// Retry is this project's default job retry policy (R2/AUTO-03, design §二.2): it
	// applies to every job of the project, overriding agents.<k>.retry and
	// server.retry (a nearer layer still wins: JobRequest.Retry / `job run --retry`).
	// Nil = the project says nothing. An explicit `max_attempts: 1` here switches
	// retry OFF for the project even when the server default enables it. See
	// Config.EffectiveRetryPolicy.
	Retry *RetryPolicy `yaml:"retry,omitempty"`
}

// ApprovalConfig is a project's approval gate for ACP permission requests
// (docs/design/2026-09-17-acp-agent-and-approval-gate-design.md §二.1).
//
// It only applies to acp-agent jobs: the ACP `session/request_permission` request is
// the gate's input. `mode` decides — off auto-allows exactly as S0 did, ask asks a
// human unless the tool call's kind is auto_allowed, strict asks for everything.
// An agent may only TIGHTEN this (Config.EffectiveApproval).
type ApprovalConfig struct {
	// Mode is off|ask|strict ("" => off).
	Mode string `yaml:"mode,omitempty"`
	// AutoAllowKinds lists the ACP ToolKinds approved without asking under mode=ask
	// ("" => DefaultApprovalAutoAllowKinds).
	AutoAllowKinds []string `yaml:"auto_allow_kinds,omitempty"`
	// AskKinds lists the ACP ToolKinds that always ask ("" => DefaultApprovalAskKinds).
	// It takes precedence over AutoAllowKinds, so a kind in both lists asks.
	AskKinds []string `yaml:"ask_kinds,omitempty"`
	// TimeoutSec bounds how long a pending approval waits for an answer ("" / <=0 =>
	// DefaultApprovalTimeoutSec). The job's own timeout still applies on top.
	TimeoutSec int `yaml:"timeout_sec,omitempty"`
	// OnTimeout is reject|allow — what the agent is told when nobody answers in time
	// ("" => reject).
	OnTimeout string `yaml:"on_timeout,omitempty"`
	// RememberAllowAlways makes a human's allow_always answer cover the SAME tool
	// kind for the rest of the job, so one approval does not become twenty prompts.
	// A pointer so "unset" defaults to true while an explicit false asks every time.
	RememberAllowAlways *bool `yaml:"remember_allow_always,omitempty"`
}

// Approval modes (ApprovalConfig.Mode, ACPConfig.PermissionPolicy).
const (
	// ApprovalOff auto-allows every permission request (the S0 behaviour, and the
	// default: upgrading gofer must not park every job on a human).
	ApprovalOff = "off"
	// ApprovalAsk auto-allows the auto_allow_kinds and asks a human for the rest.
	ApprovalAsk = "ask"
	// ApprovalStrict asks a human for every permission request.
	ApprovalStrict = "strict"
	// ApprovalAutoAllow is the AGENT-level spelling of "no tightening"
	// (agents.<key>.acp.permission_policy). It is deliberately distinct from
	// ApprovalOff at the agent level: an agent may never turn the gate OFF, only
	// decline to raise it.
	ApprovalAutoAllow = "auto_allow"
)

// Approval on-timeout outcomes (ApprovalConfig.OnTimeout).
const (
	// ApprovalOnTimeoutReject answers a timed-out request with a reject option (or a
	// cancellation when the agent offers none) — the default: an unanswered request
	// must not become an approval.
	ApprovalOnTimeoutReject = "reject"
	// ApprovalOnTimeoutAllow answers a timed-out request with allow_once.
	ApprovalOnTimeoutAllow = "allow"
)

// DefaultApprovalTimeoutSec is how long a pending approval waits for an answer when
// the project does not say (30min — long enough for a human to notice the card, short
// enough that a forgotten job does not hold the agent forever).
const DefaultApprovalTimeoutSec = 1800

// Approval tool kinds approved/asked by default. Together they cover the whole ACP
// vocabulary; a kind in NEITHER list (a future protocol addition) asks.
var (
	// DefaultApprovalAutoAllowKinds are read-only, side-effect-free tool calls.
	DefaultApprovalAutoAllowKinds = []string{
		acp.ToolKindRead, acp.ToolKindSearch, acp.ToolKindThink, acp.ToolKindFetch,
	}
	// DefaultApprovalAskKinds are the kinds that mutate the workspace or run code.
	DefaultApprovalAskKinds = []string{
		acp.ToolKindEdit, acp.ToolKindDelete, acp.ToolKindMove,
		acp.ToolKindExecute, acp.ToolKindOther, acp.ToolKindSwitchMode,
	}
)

// WithDefaults returns the policy with every unset field resolved, so a consumer
// (the runner) never has to know the defaults. Slices are copied: the returned value
// shares no backing array with the config.
func (a ApprovalConfig) WithDefaults() ApprovalConfig {
	if a.Mode == "" {
		a.Mode = ApprovalOff
	}
	if a.AutoAllowKinds == nil {
		a.AutoAllowKinds = append([]string(nil), DefaultApprovalAutoAllowKinds...)
	}
	if a.AskKinds == nil {
		a.AskKinds = append([]string(nil), DefaultApprovalAskKinds...)
	}
	if a.TimeoutSec <= 0 {
		a.TimeoutSec = DefaultApprovalTimeoutSec
	}
	if a.OnTimeout == "" {
		a.OnTimeout = ApprovalOnTimeoutReject
	}
	if a.RememberAllowAlways == nil {
		t := true
		a.RememberAllowAlways = &t
	}
	return a
}

// AllowsAlways reports the remember_allow_always switch (unset = true).
func (a ApprovalConfig) AllowsAlways() bool {
	return a.RememberAllowAlways == nil || *a.RememberAllowAlways
}

// AutoAllow reports whether a tool call of this kind needs no approval. It is asked
// of a RESOLVED policy (WithDefaults/ApprovalPolicy): ask_kinds wins over
// auto_allow_kinds, and a kind named by neither list is asked about — fail closed, so
// a new protocol kind can never slip through unapproved.
func (a ApprovalConfig) AutoAllow(kind string) bool {
	for _, k := range a.AskKinds {
		if k == kind {
			return false
		}
	}
	for _, k := range a.AutoAllowKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// ApprovalPolicy returns the project's approval policy with the defaults applied
// (nil block => the default policy).
func (p ProjectConfig) ApprovalPolicy() ApprovalConfig {
	var a ApprovalConfig
	if p.Approval != nil {
		a = *p.Approval
	}
	return a.WithDefaults()
}

// EffectiveApproval resolves the approval policy that governs a job of this project
// run by this agent: the project's policy, TIGHTENED — never relaxed — by the agent's
// acp.permission_policy (design §GATE-01.1). The agent knob therefore has three
// effects and no others: auto_allow (or unset) leaves the project policy alone, ask
// raises off to ask, strict raises anything to strict. An unknown project/agent
// resolves to the default (off) policy.
func (c *Config) EffectiveApproval(projectKey, agent string) ApprovalConfig {
	pol := ProjectConfig{}.ApprovalPolicy()
	if c == nil {
		return pol
	}
	if p, ok := c.Projects[projectKey]; ok {
		pol = p.ApprovalPolicy()
	}
	if a, ok := c.Agents[agent]; ok && a.ACP != nil {
		if rank := approvalRank(a.ACP.PermissionPolicy); rank > approvalRank(pol.Mode) {
			pol.Mode = approvalModeByRank(rank)
		}
	}
	return pol
}

// approvalRank orders the modes by strictness so "only tighten" is a max().
func approvalRank(mode string) int {
	switch mode {
	case ApprovalAsk:
		return 1
	case ApprovalStrict:
		return 2
	default: // "", off, auto_allow, and anything unknown (validated at load)
		return 0
	}
}

// approvalModeByRank is approvalRank's inverse for the ranks it produces.
func approvalModeByRank(rank int) string {
	switch rank {
	case 1:
		return ApprovalAsk
	case 2:
		return ApprovalStrict
	default:
		return ApprovalOff
	}
}

// IsNotifyEnabled reports whether E14 webhook delivery is enabled for the
// project. Unset (nil) defaults to true; an explicit notify_enabled:false
// suppresses it.
func (p ProjectConfig) IsNotifyEnabled() bool { return p.NotifyEnabled == nil || *p.NotifyEnabled }

// IsInteractiveAllowed reports whether the project permits interactive (pty) jobs.
// allow_interactive is the ONLY source: unset (nil) means "no interactive jobs", so
// the default is closed and a project built in code with no switch set can never open
// interactive submission by accident. The legacy narrowing list (AGT-02) is gone: a
// yaml still carrying interactive_allowed_agents fails the load (config.RejectRemovedKeys)
// rather than being half-read into this switch. It is the
// shared derivation of server admission (job.validate) and the POLICY push
// (core.projectToPolicy), which is why it lives on the type, not in a loader.
func (p ProjectConfig) IsInteractiveAllowed() bool {
	return p.AllowInteractive != nil && *p.AllowInteractive
}

// ArgList is an argv template list that distinguishes UNSET (nil) from SET BUT
// EMPTY (`[]`). AgentConfig.InteractiveArgs needs the distinction because the empty
// list is a value, not an absence: AGT-02 defines `args` + `interactive_args: []` as
// the dual-mode agent whose interactive launch is the CLI's bare TUI, and the
// length-based `omitempty` a plain []string gets would silently drop the key on
// every write-back — turning a dual-mode agent into a batch-only one (bd h-aii-kd57).
//
// goccy/go-yaml consults IsZeroer for `omitempty`, so only nil is "unset" here.
type ArgList []string

// IsZero implements goccy/go-yaml's IsZeroer: nil is unset (`omitempty` drops the
// key), an allocated empty list is a written value (encoded as `[]`).
func (a ArgList) IsZero() bool { return a == nil }

// AgentConfig describes a configurable CLI agent. Detect is refined in P3; P2
// only needs it to decode cleanly.
type AgentConfig struct {
	Type    string   `yaml:"type,omitempty"`
	Command string   `yaml:"command,omitempty"`
	Args    []string `yaml:"args,omitempty"`
	// InteractiveArgs defines argv for interactive mode. Four combinations: args with {{prompt}} only=batch; InteractiveArgs non-nil only=interactive; both=dual-mode; neither=mode-less (except exec, which is batch by definition).
	InteractiveArgs ArgList           `yaml:"interactive_args,omitempty"`
	Env             map[string]string `yaml:"env,omitempty"`
	AllowRawCmd     bool              `yaml:"allow_raw_cmd,omitempty"`
	// Interactive 兼容别名：仅交互、args 即交互 argv。
	Interactive bool         `yaml:"interactive,omitempty"`
	NoRawCmd    bool         `yaml:"no_raw_cmd,omitempty"`
	Detect      DetectConfig `yaml:"detect,omitempty"`
	// SessionInject 注入模式 argv 模板（模式①，首选）。非空 => 提交时 gofer 生成 uuid
	// 渲染追加到 argv，立即知 id、无需解析输出。{{session_id}} 占位（session-capture §6.4）。
	SessionInject []string `yaml:"session_inject,omitempty"`
	// SessionCapture 捕获模式正则（模式②，兜底），第一个**非空**捕获组 = session_id
	// （可写多分支，各分支各占一个捕获组，只有命中的那个非空：如 omp 的 ndjson 会话行
	// 与 TUI 退出横幅）。仅当 SessionInject 为空时使用（注入优先于捕获）。
	SessionCapture string `yaml:"session_capture,omitempty"`
	// SessionResume resume 的整条 agent argv 模板（非追加 flag），{{session_id}}/{{prompt}}
	// 占位。供 `gofer job resume`（P2）拼接续接命令。
	SessionResume []string `yaml:"session_resume,omitempty"`
	// SessionResumeInteractive 是 pty/交互会话续接的 argv 模板（区别于非交互 SessionResume）。
	// 交互会话进 TUI 续接，不用非交互的一次性 flag（claude 的 -p / codex 的 exec）。
	// 仅当源 job 是 interactive 时由 ResumeJob 选用；未配置则回退到内置默认。
	SessionResumeInteractive []string `yaml:"session_resume_interactive,omitempty"`
	// TransientErrorPatterns override the complete built-in list, including when empty.
	// Matching is case insensitive; nil selects the command's built-in defaults.
	TransientErrorPatterns []string `yaml:"transient_error_patterns,omitempty"`
	// FallbackAgents is this agent's ordered candidate list for a TRANSIENT failure
	// that its own session cannot continue (SUP-01 P3): the first candidate that the
	// project admits takes the work over. Empty = no transfer (the default, so an
	// existing config is unaffected). A project may override the list per agent with
	// agent_fallbacks; `job run --fallback` overrides both for one job. Candidates
	// must be declared agents that can run in batch mode (validated at load).
	FallbackAgents []string `yaml:"fallback_agents,omitempty"`
	// ReadOnlyArgs is the argv a `job run --read-only` appends to a cli-agent's argv
	// (both the batch and the interactive shape, at the end like AgentArgs) so the
	// sandbox is the CLI's own. Unset means "use the built-in table for this agent"
	// (agent.applyReadOnlyDefaults: codex `-s read-only`, claude `--permission-mode
	// plan`); an agent with no built-in and no configured value cannot run read-only
	// at all and a --read-only submit is refused (bd h-aii-0ql3). An acp-agent has no
	// argv suffix — its read-only mode is the protocol's, see acp.modes.read_only.
	ReadOnlyArgs []string `yaml:"read_only_args,omitempty"`
	// SystemInject 是 per-agent 的 system prompt 注入 argv 模板（E35 角色，类比
	// SessionInject）。非空 + 请求带 system_prompt 时，submit 渲染 {{system_prompt}}
	// 追加到 argv（如 claude `--append-system-prompt <p>`）。保 argv 结构、不 shell
	// 拼接（SR403）。claude 有内置默认（applySystemDefaults），codex 留空待实测。
	SystemInject []string `yaml:"system_inject,omitempty"`
	// OutputFormat 是采集该 agent stdout 的格式（bd h-aii-rpky）：""/"text"（默认）
	// 逐字落盘；"ndjson" 表示 stdout 是逐行 JSON 事件流（omp --mode json、claude
	// --output-format stream-json），落盘时按 NDJSONKeep 过滤掉逐 token 增量事件，
	// 否则日志体积膨胀 10~30 倍且 web 实时日志不可读（过滤实现见
	// internal/runner/ndjsonfilter）。
	OutputFormat string `yaml:"output_format,omitempty"`
	// NDJSONKeep 是事件类型白名单（仅 OutputFormat=="ndjson" 时有意义）：无点号的条目
	// 匹配顶层 `type`，带点号的条目按嵌套路径取（如 `message.role` 命中该路径存在非空
	// 字符串的行）。留空且未命中内置默认 = 不过滤。内置默认见 agent.applyOutputDefaults。
	NDJSONKeep []string `yaml:"ndjson_keep,omitempty"`
	// NDJSONRaw 为 true 时把原始（未过滤）行另存 <result_dir>/stdout.raw.log，仅排障用
	// （体积与过滤前一致，默认关闭）。
	NDJSONRaw bool `yaml:"ndjson_raw,omitempty"`
	// NDJSONEventsTo 选择紧凑事件行的落点（bd h-aii-525u，仅 ndjson 时有意义）：
	// "stderr"（默认）像 codex 那样把过程事件写进 stderr、stdout 只留最终答复；
	// "stdout" 保持一期落点（事件写到 stdout，答复追加其后）。
	NDJSONEventsTo string `yaml:"ndjson_events_to,omitempty"`
	// NDJSONStdout 决定 stdout 承载什么（仅 ndjson 时有意义）："assistant_text"（omp 默认）
	// 写全部非空 assistant 消息（过程叙述 + 最终答复，空行分隔）；"final_text"（claude 默认）
	// 只写最终答复（omp 取最后一条 assistant 消息，claude 取 result.result）——注意 omp 的
	// "最后一条"在收尾多一回合（todo 提醒、后台任务通知）时会丢掉真正的汇报（bd h-aii-lvo9），
	// 所以 omp 不再默认它；"events" = 一期行为：stdout 即紧凑事件流，不提取答复。
	NDJSONStdout string `yaml:"ndjson_stdout,omitempty"`
	// NDJSONStdoutPath 是最终答复所在的 JSON 路径（点号分隔，如 result.result），给内置
	// 投影器认不出的 agent 用；显式配置覆盖内置的答复提取规则。
	NDJSONStdoutPath string `yaml:"ndjson_stdout_path,omitempty"`
	// NDJSONFields 按事件类型覆盖投影输出的事件内容（如
	// `ndjson_fields: {turn_end: [type, usage, model]}`）：该类型的行只带这些路径
	// （点号分隔，取最后一段作键）；对投影器默认丢弃的类型也生效。
	NDJSONFields map[string][]string `yaml:"ndjson_fields,omitempty"`
	// McpServerName 是该 agent（codex）config.toml 里 gofer MCP server 的块名
	// （`[mcp_servers.<name>]`）。gap①(issue 7z6j)：codex 启动 MCP stdio 子进程用净化
	// env、不透传 codex 进程 env，故 role.env 注入 codex 进程对 MCP 子进程无效；改经
	// codex `-c mcp_servers.<name>.env.<KEY>=<VALUE>` 覆盖 MCP server env，使 sup 的
	// gofer MCP 自注册 role=supervisor。约定默认 `gofer`（agent.McpServerNameDefault），
	// 仅当 codex config 改了块名时才需配置此项。
	McpServerName string `yaml:"mcp_server_name,omitempty"`
	// ACP is the type=acp-agent sub-block (protocol-level settings). Ignored by
	// every other agent type.
	ACP *ACPConfig `yaml:"acp,omitempty"`
	// MaxConcurrent caps how many jobs of THIS agent may run at once (JOB-11).
	// 0/unset = unlimited, i.e. exactly the pre-JOB-11 behaviour. Like the project
	// and caller caps it QUEUES the excess (the job stays `queued`), it never
	// rejects: a saturated agent is a throughput limit, not an admission error.
	MaxConcurrent int `yaml:"max_concurrent,omitempty"`
	// StallTimeoutSec overrides the AUTO-05 output-stall window for THIS agent
	// (AUTO-05): nil = inherit server.stall_timeout_sec, 0 = never watch this agent's
	// jobs, N = kill a job of this agent after N silent seconds. A pointer because
	// "off" and "unset" are different decisions.
	StallTimeoutSec *int `yaml:"stall_timeout_sec,omitempty"`
	// Retry is this agent's default job retry policy (R2/AUTO-03, design §二.2): it
	// applies to jobs that run THIS agent, overriding server.retry and being
	// overridden by projects.<k>.retry / JobRequest.Retry. Nil = this agent says
	// nothing (the layer below answers). See Config.EffectiveRetryPolicy.
	Retry *RetryPolicy `yaml:"retry,omitempty"`
}

// ACPConfig is the acp-agent's protocol-level configuration
// (docs/design/2026-09-17-acp-agent-and-approval-gate-design.md §一.1).
type ACPConfig struct {
	// Modes maps a gofer-level mode to the agent's own mode id, e.g.
	// `read_only: "ask"`. It is PARSED in S0 and driven (session/set_mode) in S2.
	Modes map[string]string `yaml:"modes,omitempty"`
	// PermissionPolicy selects how session/request_permission is answered.
	// Unset/`auto_allow` (S0) approves automatically, preferring an allow_once
	// option; `ask`/`strict` belong to the S1 approval gate, and the runner refuses
	// them rather than silently auto-approving.
	PermissionPolicy string `yaml:"permission_policy,omitempty"`
	// LoadSession declares whether a resume may try session/load on this agent.
	// nil/true = try (the protocol negotiates the real capability at initialize and a
	// refusal fails the job); explicit false = "don't even try": `job resume` and the
	// automatic continuation refuse the source up front (ErrResumeUnsupported) instead
	// of submitting a job the agent cannot serve.
	LoadSession *bool `yaml:"load_session,omitempty"`
	// MCPServers are advertised to the agent in session/new (stdio transport). S0
	// passes them through verbatim; empty means the agent gets none.
	MCPServers []ACPMCPServerConfig `yaml:"mcp_servers,omitempty"`
	// LogThoughts keeps the agent's thinking (agent_thought_chunk) in the job's logs:
	// the runner coalesces a thought stream into ONE compact line on stderr plus ONE
	// acp.jsonl line (bd h-aii-7kja ②). unset/true = keep them, explicit false = drop
	// them entirely, for an operator whose logs are dominated by the agent thinking
	// out loud.
	LogThoughts *bool `yaml:"log_thoughts,omitempty"`
}

// LogsThoughts reports whether this agent's thinking is kept in the job's logs: unset
// means yes (the coalesced line is small), an explicit false means never.
func (a *ACPConfig) LogsThoughts() bool {
	return a == nil || a.LogThoughts == nil || *a.LogThoughts
}

// AllowsLoadSession reports whether a resume may try session/load on this agent:
// unset means yes (the protocol negotiates it), an explicit false means "don't try".
// A nil ACPConfig has no acp-agent settings at all and therefore allows the attempt.
func (a *ACPConfig) AllowsLoadSession() bool {
	return a == nil || a.LoadSession == nil || *a.LoadSession
}

// ACPMCPServerConfig is one MCP server handed to an acp-agent at session/new.
type ACPMCPServerConfig struct {
	Name    string            `yaml:"name"`
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
}

// Agent stdout capture formats (AgentConfig.OutputFormat).
const (
	// OutputFormatText captures the agent's stdout verbatim. It is the default,
	// so an agent that does not ask for structured capture is never filtered.
	OutputFormatText = "text"
	// OutputFormatNDJSON declares the agent's stdout a JSON-Lines event stream to
	// be compacted at capture time (bd h-aii-rpky). See AgentConfig.NDJSONKeep.
	OutputFormatNDJSON = "ndjson"
)

// Where the compacted event lines go (AgentConfig.NDJSONEventsTo), and what the
// stdout stream carries (AgentConfig.NDJSONStdout). The capture projects a
// structured agent stream onto the two logs (bd h-aii-525u): the process events
// belong on stderr, the agent's final answer on stdout.
const (
	// NDJSONEventsStderr writes the events to stderr.log — the default, and the
	// shape codex users expect: stdout is the answer, stderr is the process log.
	NDJSONEventsStderr = "stderr"
	// NDJSONEventsStdout keeps the phase-1 destination (events on stdout.log).
	NDJSONEventsStdout = "stdout"
	// NDJSONStdoutFinalText writes only the agent's final answer to stdout.log.
	NDJSONStdoutFinalText = "final_text"
	// NDJSONStdoutAssistantText writes every non-empty assistant message (narration
	// and final answer, blank-line separated) — the omp default since v0.47, because
	// a harness-injected trailing turn otherwise hides the real report behind a
	// one-line reply (bd h-aii-lvo9).
	NDJSONStdoutAssistantText = "assistant_text"
	// NDJSONStdoutEvents writes the compact event stream to stdout.log and skips
	// answer extraction: the pre-525u capture, byte for byte.
	NDJSONStdoutEvents = "events"
)

// EffectiveOutputFormat is the configured capture format with the default
// applied: unset and "text" are the same thing.
func (a AgentConfig) EffectiveOutputFormat() string {
	if a.OutputFormat == "" {
		return OutputFormatText
	}
	return a.OutputFormat
}

// NDJSONOutput reports whether this agent's stdout is a structured NDJSON stream
// that must be compacted at capture time.
func (a AgentConfig) NDJSONOutput() bool { return a.OutputFormat == OutputFormatNDJSON }

// DetectConfig is the agent availability probe. Placeholder in P2, refined P3.
type DetectConfig struct {
	Command string   `yaml:"command,omitempty"`
	Args    []string `yaml:"args,omitempty"`
}

// RunnerConfig describes an execution location. peer-http fields are decoded in
// P2 but only used by the peer runner in P7. For type=worker (ws-worker), the
// runner targets a single registered worker identified by WorkerID; one
// worker-runner = one worker (dynamic routing is WP4 scheduling, not WP1).
type RunnerConfig struct {
	Type     string `yaml:"type,omitempty"`
	BaseURL  string `yaml:"base_url,omitempty"`
	TokenEnv string `yaml:"token_env,omitempty"`
	// WorkerID is the worker this runner dispatches to (type=worker only). It
	// must match a server.workers entry.
	WorkerID string `yaml:"worker_id,omitempty"`
}

// The built-in in-process runner needs no `runners:` declaration and has TWO
// accepted spellings on any INPUT surface:
//
//   - "local"  — the canonical key: what the wire, /v1/runners, the jobs.runner
//     column and the web console carry, and what a project's
//     allowed_runners lists.
//   - "server" — the human-facing spelling the CLI advertises (`--runner`'s
//     default: "server = server-local"), picked because on a WORKER
//     host "local" means that worker's own runner.
//
// They are one runner, so an input boundary that receives the alias must
// translate it (NormalizeRunnerName) rather than reject a caller who used the
// documented spelling; allowlist checks accept either spelling.
const (
	// BuiltinLocalRunner is the canonical key of the in-process runner.
	BuiltinLocalRunner = "local"
	// BuiltinLocalRunnerAlias is the CLI/public spelling of that same runner.
	BuiltinLocalRunnerAlias = "server"
)

// IsBuiltinLocalRunnerName reports whether name spells the built-in in-process
// runner (either the canonical key or the CLI alias).
func IsBuiltinLocalRunnerName(name string) bool {
	name = strings.TrimSpace(name)
	return name == BuiltinLocalRunner || name == BuiltinLocalRunnerAlias
}

// NormalizeRunnerName maps a caller-supplied runner key onto the canonical key
// for that runner: the alias "server" becomes "local". Every other value (a
// configured runner key, or the empty string) is returned unchanged — an empty
// runner stays empty so "runner is required" is still reported for it.
//
// It is deliberately spelling-only: the declare-wins rule for a config that
// declares a runner literally named "server" lives with the caller that holds
// the config snapshot (see job.normalizeRunner).
func NormalizeRunnerName(name string) string {
	name = strings.TrimSpace(name)
	if name == BuiltinLocalRunnerAlias {
		return BuiltinLocalRunner
	}
	return name
}

// WorkerConfig is the top-level config for `gofer worker --config worker.yaml`
// (ws-worker §6). The worker runs jobs locally with its own project/agent/runner
// config and bridges log/status/result back over a single WebSocket to the hub.
type WorkerConfig struct {
	WorkerID      string                   `yaml:"worker_id,omitempty"`
	ServerLink    WorkerServerLink         `yaml:"server_link,omitempty"`
	Projects      map[string]ProjectConfig `yaml:"projects,omitempty"`
	Agents        map[string]AgentConfig   `yaml:"agents,omitempty"`
	Runners       map[string]RunnerConfig  `yaml:"runners,omitempty"`
	MaxConcurrent int                      `yaml:"max_concurrent,omitempty"`
	Labels        []string                 `yaml:"labels,omitempty"`
	Storage       StorageConfig            `yaml:"storage,omitempty"`
	Log           LogConfig                `yaml:"log,omitempty"`
	// Roots maps server-side logical path prefixes to this machine's host path
	// prefixes (P3 T2, design §10). Longest boundary-aligned From wins; see
	// MapRoot. Adding a root deliberately widens what this worker can run, so it
	// is a local-only knob (never remotely rewritten, never API-exposed, T2-C).
	Roots []WorkerRoot `yaml:"roots,omitempty"`
	// Guards are opt-in per-worker capability gates (P3 T2-D). See WorkerGuards.
	Guards WorkerGuards       `yaml:"guards,omitempty"`
	Tunnel WorkerTunnelConfig `yaml:"tunnel,omitempty"`
	// XferTimeoutSec bounds ONE inbound file transfer on this worker (XFER-01
	// protocol v9). 0/unset => DefaultWorkerXferTimeoutSec (10m). It is a
	// PROCESS-level setting like the rest of this block: it is read when the worker
	// starts serving, not on hot reload (the hub cannot know this worker's budget, so
	// the executing machine is the only place it can live).
	XferTimeoutSec int `yaml:"xfer_timeout_sec,omitempty"`
}

// DefaultWorkerXferTimeoutSec is a worker's default single-transfer deadline (10
// minutes). A transfer is not resumable, so a stalled one must be FAILED at its
// deadline rather than held open.
const DefaultWorkerXferTimeoutSec = 600

// EffectiveXferTimeout resolves worker.xfer_timeout_sec, defaulting to
// DefaultWorkerXferTimeoutSec.
func (w WorkerConfig) EffectiveXferTimeout() time.Duration {
	sec := w.XferTimeoutSec
	if sec <= 0 {
		sec = DefaultWorkerXferTimeoutSec
	}
	return time.Duration(sec) * time.Second
}

// WorkerTunnelConfig controls outbound TCP tunnels.
type WorkerTunnelConfig struct {
	Allow          []string `yaml:"allow,omitempty"`
	MaxConns       int      `yaml:"max_conns,omitempty"`
	DialTimeoutSec int      `yaml:"dial_timeout_sec,omitempty"`
}

// ValidateWorkerTunnel validates worker tunnel allowlist entries.
func ValidateWorkerTunnel(w WorkerConfig) error {
	if _, err := tunnel.ParseAllowlist(w.Tunnel.Allow); err != nil {
		return fmt.Errorf("worker.tunnel: %w", err)
	}
	return nil
}

// EffectiveMaxConns returns configured limit or default 8.
func (t WorkerTunnelConfig) EffectiveMaxConns() int {
	if t.MaxConns > 0 {
		return t.MaxConns
	}
	return 8
}

// DialTimeout returns configured timeout or default 5 seconds.
func (t WorkerTunnelConfig) DialTimeout() time.Duration {
	if t.DialTimeoutSec > 0 {
		return time.Duration(t.DialTimeoutSec) * time.Second
	}
	return 5 * time.Second
}

// Enabled reports whether any tunnel target is allowed.
func (t WorkerTunnelConfig) Enabled() bool { return len(t.Allow) > 0 }

// WorkerRoot maps a server-side logical path prefix (From) onto this worker's
// host path prefix (To). It is the ONLY declaration a worker needs to expose a
// tree for execution: matching + containment live in WorkerConfig.MapRoot.
type WorkerRoot struct {
	From string `yaml:"from"` // server-side logical path prefix
	To   string `yaml:"to"`   // this machine's host path prefix
}

// WorkerGuards are opt-in capability gates for the worker (P3 T2-D).
//
// The fields are *bool (not bool) on purpose, diverging from design §6.1's
// "default false": nil (field absent) means "do NOT tighten" — identical to
// today's behaviour where no guards exist and every project runs exec/pty. A
// bare bool zero value would flip to false and reject all exec/pty jobs the
// instant the binary is upgraded (violates acceptance 1). This mirrors the
// established *bool idiom in this repo (ProjectConfig.CaptureDiff /
// NotifyEnabled). Enforcement is therefore opt-in: set the field explicitly to
// tighten. (worker.example.yaml's explicit guards + doctor WARN are T6.)
type WorkerGuards struct {
	AllowExec        *bool `yaml:"allow_exec,omitempty"`
	AllowInteractive *bool `yaml:"allow_interactive,omitempty"`
}

// IsExecAllowed reports whether exec jobs are permitted. Unset (nil) => allowed
// (no extra tightening, T2-D); an explicit allow_exec:false rejects them.
func (g WorkerGuards) IsExecAllowed() bool { return g.AllowExec == nil || *g.AllowExec }

// IsInteractiveAllowed reports whether interactive/pty jobs are permitted. Unset
// (nil) => allowed (T2-D); an explicit allow_interactive:false rejects them.
func (g WorkerGuards) IsInteractiveAllowed() bool {
	return g.AllowInteractive == nil || *g.AllowInteractive
}

// WorkerServerLink describes how the worker reaches the hub. URLs may list
// MULTIPLE hub addresses (redundant entry points: VIPs of one hub, or several
// independent hubs); the worker rotates through them on a failed connect (C7,
// §5.2). Token/TokenEnv resolve the Bearer credential. Reconnect tunes the
// backoff + heartbeat timings (P3 §4).
type WorkerServerLink struct {
	URLs      []string        `yaml:"urls,omitempty"`
	TokenEnv  string          `yaml:"token_env,omitempty"`
	Token     string          `yaml:"token,omitempty"`
	Reconnect ReconnectConfig `yaml:"reconnect,omitempty"`
}

// ReconnectConfig is the worker's backoff + heartbeat policy for hub reconnection
// (C7/P3, §4). All fields default when <= 0:
//   - InitialBackoffMS: first-retry base wait (default 1000 = 1s).
//   - MaxBackoffMS: backoff cap (default 30000 = 30s); full-jitter strategy is
//     fixed (sleep = rand(0, min(max, initial*2^attempt))), so there is no jitter
//     knob.
//   - PingIntervalSec: heartbeat ping cadence (default 15s; symmetric with the hub).
//   - ReadDeadlineSec: single-read deadline / half-open detection (default 45s).
type ReconnectConfig struct {
	InitialBackoffMS int `yaml:"initial_backoff_ms,omitempty"`
	MaxBackoffMS     int `yaml:"max_backoff_ms,omitempty"`
	PingIntervalSec  int `yaml:"ping_interval_sec,omitempty"`
	ReadDeadlineSec  int `yaml:"read_deadline_sec,omitempty"`
}

// ExecPath returns the GOFER-PROCESS execution-root path for a project — the
// single source of truth every gofer-side path helper resolves against (E29/D10):
// when server.path_view is "container" AND the project sets a container_path, that
// container path is used; otherwise host_path. Default (path_view unset/"host")
// => host_path, so all existing execution behaviour is unchanged (D9).
//
// NOTE: E21 host-side actions (which run on the host bridge) always use host_path
// directly and must NOT route through ExecPath.
func (c *Config) ExecPath(p ProjectConfig) string {
	if c.Server.PathView == "container" && p.ContainerPath != "" {
		return p.ContainerPath
	}
	return p.HostPath
}

// ProjectAllowedAgents returns the allowed_agents list for projectKey. The
// second return is false when the project is not registered. Used by the agent
// package to enforce the per-project allowlist (plan §11).
func (c *Config) ProjectAllowedAgents(projectKey string) ([]string, bool) {
	p, ok := c.Projects[projectKey]
	if !ok {
		return nil, false
	}
	return p.AllowedAgents, true
}

// DefaultMaxJobTimeoutSec is the job-timeout ceiling used when NEITHER
// server.max_job_timeout_sec NOR the project's max_timeout_sec is set. It is the
// value the clamp was hard-coded to before it became configurable (bd h-aii-s9ck),
// so an existing config keeps its exact previous behaviour.
const DefaultMaxJobTimeoutSec = 3600

// DefaultVerifyTimeoutSec bounds a job's verify step when neither the request nor
// the project sets one (SUP-01 B). 10 minutes: a full test suite of a medium project
// fits, while a hung suite still ends the job well before an agent-sized budget
// would.
const DefaultVerifyTimeoutSec = 600

// EffectiveVerifyTimeoutSec resolves the deadline of a job's verify step for a
// project: the project's verify_timeout_sec, else the default. A key the config does
// not define (a worker-only project) resolves the default too — the step never ends
// instantly for lack of a row.
func (c *Config) EffectiveVerifyTimeoutSec(projectKey string) int {
	if p, ok := c.Projects[projectKey]; ok && p.VerifyTimeoutSec > 0 {
		return p.VerifyTimeoutSec
	}
	return DefaultVerifyTimeoutSec
}

// DefaultJobRecoverWindowSec is the RECOV-01 window applied when
// server.job_recover_window_sec is UNSET: how long a worker's in-flight jobs are
// held in `recovering` while the same worker process reconnects (e.g. a WSL /
// Docker / VPN blip). 120s covers a reconnect with exponential backoff plus a few
// retries; an explicit 0 disables recovery (Config.JobRecoverWindow returns 0).
const DefaultJobRecoverWindowSec = 120

// JobRecoverWindow resolves the RECOV-01 recovery window (design §一). Unset
// (nil) → DefaultJobRecoverWindowSec; an explicit value ≤ 0 → 0, meaning recovery
// is OFF and a worker disconnect fails its in-flight jobs immediately (the
// pre-RECOV-01 behaviour). It is the ONE resolver every consumer (hub, serve
// orphan reconciliation) reads, so the "unset vs 0" distinction lives in a single
// place instead of being re-derived from a raw int.
func (c *Config) JobRecoverWindow() time.Duration {
	if c == nil || c.Server.JobRecoverWindowSec == nil {
		return DefaultJobRecoverWindowSec * time.Second
	}
	if *c.Server.JobRecoverWindowSec <= 0 {
		return 0
	}
	return time.Duration(*c.Server.JobRecoverWindowSec) * time.Second
}

// EffectiveMaxTimeoutSec resolves the ONE ceiling a job in projectKey is clamped
// against (bd h-aii-s9ck): the project's max_timeout_sec when set, else the
// server's max_job_timeout_sec, else DefaultMaxJobTimeoutSec. A project override
// is a REPLACEMENT rather than a tightening, so it may exceed the server value.
// Callers: the job submit clamp (and its "clamped" report) and serve's supervisor
// reconciler — both must see the same number, hence one resolver on *Config.
func (c *Config) EffectiveMaxTimeoutSec(projectKey string) int {
	if p, ok := c.Projects[projectKey]; ok && p.MaxTimeoutSec > 0 {
		return p.MaxTimeoutSec
	}
	if c.Server.MaxJobTimeoutSec > 0 {
		return c.Server.MaxJobTimeoutSec
	}
	return DefaultMaxJobTimeoutSec
}

// ResolvedExchangeSubdir returns the effective exchange subdir for a project,
// falling back to the storage default (or the hard default) when unset.
func (c *Config) ResolvedExchangeSubdir(p ProjectConfig) string {
	if p.ExchangeSubdir != "" {
		return p.ExchangeSubdir
	}
	if c.Storage.DefaultExchangeSubdir != "" {
		return c.Storage.DefaultExchangeSubdir
	}
	return DefaultExchangeSubdir
}

// ResolvedResultSubdir returns the effective result subdir for a project,
// falling back to the storage default (or the hard default) when unset.
func (c *Config) ResolvedResultSubdir(p ProjectConfig) string {
	if p.ResultSubdir != "" {
		return p.ResultSubdir
	}
	if c.Storage.DefaultResultSubdir != "" {
		return c.Storage.DefaultResultSubdir
	}
	return DefaultResultSubdir
}

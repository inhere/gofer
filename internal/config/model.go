// Package config defines the gofer configuration model and the
// loader/writer that resolve, decode, default and persist it. See plan §6.1.
package config

import (
	"math"
	"time"
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
	Server   ServerConfig             `yaml:"server"`
	Storage  StorageConfig            `yaml:"storage"`
	Projects map[string]ProjectConfig `yaml:"projects"`
	Agents   map[string]AgentConfig   `yaml:"agents"`
	Runners  map[string]RunnerConfig  `yaml:"runners"`
	// Roles are named E35 role presets (reviewer/bugfix/…): a base agent + a
	// resident system_prompt + optional default project/tags. `job run --role` /
	// `gofer_run_job(role=)` resolve a role to fill those request fields (design
	// §8.5). Rules/context-file mounting is E11 territory, out of scope here.
	Roles map[string]RoleConfig `yaml:"roles"`
	// Supervisor is the OPTIONAL E25 layered-answerer config (design §8.3-8.4). nil
	// (absent) or Enabled=false means no answerer runs — pending interactions wait
	// for a human (the conservative default). Serve constructs supervisor.Service +
	// starts its poller only when Enabled.
	Supervisor *SupervisorConfig `yaml:"supervisor,omitempty"`
	// Presence tunes the E36 driver-agent presence registry / mailbox TTLs and prune
	// cadence. All fields optional; unset (<=0) keeps the package defaults (90s online
	// TTL / 24h message TTL / 60s prune), so an absent `presence:` block changes nothing.
	Presence PresenceConfig `yaml:"presence,omitempty"`
}

// PresenceConfig tunes the E36 presence/mailbox runtime (design §9 / §12 收尾). Every
// field is an OPTIONAL override in seconds; <=0 means "use the built-in default"
// (applied by presence.Service / the serve prune loop, the single source of truth).
// These are read at serve start; changing them needs a restart (not SIGHUP-live).
type PresenceConfig struct {
	// TTLSec: a driver agent is online while last_seen is within this window (default 90s).
	TTLSec int `yaml:"ttl_sec"`
	// MessageTTLSec: how long an unread message lives before prune may drop it (default 24h).
	MessageTTLSec int `yaml:"message_ttl_sec"`
	// PruneIntervalSec: presence/inbox prune sweeper cadence (default 60s).
	PruneIntervalSec int `yaml:"prune_interval_sec"`
}

// SupervisorConfig configures the E25 answerer (design §8.3-8.4 / §11). Defaults
// (interval/max_rounds/escalate_to) are applied in supervisor.NewService, so a
// minimal `supervisor: {enabled: true}` is valid. AllowPromptRegex is the
// auto-answer whitelist: EMPTY means nothing is auto-answered (escalate-only) — the
// honest, opt-in-only default (design §11).
type SupervisorConfig struct {
	Enabled          bool     `yaml:"enabled"`
	IntervalSec      int      `yaml:"interval_sec"`
	AutoAnswer       bool     `yaml:"auto_answer"`
	EscalateTo       string   `yaml:"escalate_to"`
	MaxRoundsPerJob  int      `yaml:"max_rounds_per_job"`
	AllowPromptRegex []string `yaml:"allow_prompt_regex"`
	// OwnerAnswerTimeoutSec bounds how long an interaction may sit escalated to its
	// owner (L1) before the router falls it back past the owner to the global sup
	// (L2) — the owner is会话式 and may have ended without answering (design §8.2,
	// supervisor-routing P2.1). <=0 applies the default (300s) in NewService.
	OwnerAnswerTimeoutSec int `yaml:"owner_answer_timeout_sec"`
}

// RoleConfig is one named role preset (design §8.5). Agent is the base CLI agent
// the role runs on; SystemPrompt is injected via the agent's SystemInject template
// (claude --append-system-prompt). Project/Tags are optional request defaults the
// role fills when the caller leaves them empty.
type RoleConfig struct {
	Agent        string   `yaml:"agent"`
	SystemPrompt string   `yaml:"system_prompt"`
	Project      string   `yaml:"project"`
	Tags         []string `yaml:"tags"`
	// Env is an OPTIONAL per-role env preset merged into the job's process env
	// (JobRequest.Env) at submit time, so `--role supervisor` can inject e.g.
	// GOFER_AGENT_ROLE=supervisor into the agent process without a dedicated
	// codex-sup agent (the spawned gofer MCP child inherits it and self-registers
	// role=supervisor, P3). Role.Env fills DEFAULTS — an explicit per-job env value
	// for the same key wins. 勿放 secret：值会随 job.Env 落 request_json（SR403/SR805），
	// secret 应走 agent.env / K8s secret（不落 request_json）。
	Env map[string]string `yaml:"env"`
}

// ServerConfig holds HTTP server and auth settings.
type ServerConfig struct {
	Addr            string `yaml:"addr"`
	Token           string `yaml:"token"`
	TokenEnv        string `yaml:"token_env"`
	AllowEmptyToken bool   `yaml:"allow_empty_token"`
	// PathView selects which project path the GOFER PROCESS uses as its execution
	// root (E29/D10): "host" (default, empty) => host_path; "container" =>
	// container_path (falling back to host_path when container_path is empty). It
	// is an EXPLICIT operator switch — gofer does NOT self-detect being in a
	// container (no /.dockerenv probing). All gofer-process-side paths (SafeJoin /
	// ExchangeDir / ResultBaseDir / Validate / overlay read dir) go through
	// Config.ExecPath; E21 host-side actions always use host_path (not this).
	PathView string `yaml:"path_view"`
	// Callers is the optional multi-caller auth set (C2): each entry maps a
	// bearer token to a caller id stamped onto submitted jobs for audit /
	// per-caller filtering. The legacy single Token/TokenEnv stays valid (treated
	// as caller id "default"); revocation = remove the caller + reload (C3).
	Callers []CallerConfig `yaml:"callers"`
	// WebEnabled is a pointer so that "unset" (nil) can default to true while an
	// explicit web_enabled:false disables the embedded web console (see
	// IsWebEnabled and applyDefaults).
	WebEnabled *bool `yaml:"web_enabled"`
	// Workers is the per-worker auth/binding set (ws-worker, §7 / review #1):
	// each entry registers a legitimate worker identity keyed by worker_id and
	// binds it to a token. A `register` frame whose worker_id does not match the
	// presented token's bound worker is rejected (hub.Accept). per-worker token
	// is MVP-mandatory: even allow_empty_token does not waive the binding.
	Workers map[string]WorkerAuthConfig `yaml:"workers"`
	// RunnerProbe tunes the peer-http active health probe (C6/P4): how often each
	// peer-http runner's /health is polled and the per-probe timeout. Unset =>
	// defaults (30s interval / 5s timeout). The probe only runs when at least one
	// peer-http runner is configured (zero behaviour change otherwise).
	RunnerProbe RunnerProbeConfig `yaml:"runner_probe"`
	// Notification is the E14 webhook outbound config (design §5.5). It is a
	// pointer so "unset" (nil) cleanly disables all notification (the serve
	// delivery sweeper does not even start) — zero behaviour change for any config
	// without a `notification` block. When present it lists the webhook targets,
	// the outbound host allowlist and the retry cap.
	Notification *NotificationConfig `yaml:"notification"`
	// Metrics is the E16 Prometheus /metrics policy (design §6.2). Enabled is a
	// pointer so "unset" (nil) defaults to ENABLED (the endpoint is mounted) while
	// an explicit enabled:false drops it. Token, when non-empty, re-adds a Bearer
	// check on /metrics (default empty = unauthenticated scrape, guarded by the
	// intranet admission boundary, SR202).
	Metrics MetricsConfig `yaml:"metrics"`
	// Governance is the E17 per-caller quota / rate-limit global fallback (design
	// §7.1). It is a pure additive block: an existing config with no `governance`
	// key has all-zero defaults, which means "unlimited" everywhere (向后兼容). A
	// per-caller override on a CallerConfig (> 0) takes precedence; otherwise the
	// governance default applies (see CallerConcurrencyLimit / CallerRate).
	Governance GovernanceConfig `yaml:"governance"`
}

// GovernanceConfig is the E17 global fallback for per-caller quotas (design
// §7.1). It applies to a caller only when that caller has not set its own
// override (CallerConfig.MaxConcurrentJobs / RateLimit). All fields default to 0
// = unlimited, so a config with no `governance` block keeps the legacy
// no-throttle behaviour.
type GovernanceConfig struct {
	// DefaultCallerMaxConcurrent caps how many jobs a caller may run at once when
	// the caller has no own MaxConcurrentJobs. 0 = unlimited.
	DefaultCallerMaxConcurrent int `yaml:"default_caller_max_concurrent"`
	// DefaultRateLimit is the per-second submit rate (token-bucket refill) when the
	// caller has no own RateLimit. 0 = unlimited (no rate gating).
	DefaultRateLimit float64 `yaml:"default_rate_limit"`
	// DefaultRateBurst is the token-bucket capacity when the caller has no own
	// RateBurst. <= 0 falls back to max(1, ceil(rate)) at use time (CallerRate).
	DefaultRateBurst int `yaml:"default_rate_burst"`
}

// MetricsConfig is the E16 Prometheus /metrics policy (design §6.2). It is a
// minimal additive block: an existing config with no `metrics` key keeps the
// endpoint enabled and unauthenticated (IsEnabled defaults nil→true).
type MetricsConfig struct {
	// Enabled gates the /metrics endpoint. Unset (nil) defaults to true; an
	// explicit enabled:false drops the route entirely.
	Enabled *bool `yaml:"enabled"`
	// Token, when non-empty, requires `Authorization: Bearer <token>` on /metrics
	// (for environments that want authenticated scraping). Empty = no auth.
	Token string `yaml:"token"`
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
	Webhooks    []WebhookConfig `yaml:"webhooks"`
	AllowHosts  []string        `yaml:"allow_hosts"`  // outbound host allowlist (SR904)
	AllowHTTP   bool            `yaml:"allow_http"`   // default false => https-only
	MaxAttempts int             `yaml:"max_attempts"` // <= 0 => DefaultMaxAttempts
}

// WebhookConfig is one E14 outbound webhook target (design §5.5). Events is the
// subscribed trigger set (omit => the default set job.terminal + interaction.created);
// SecretEnv names the env var holding the HMAC secret (SR403, never inlined);
// Projects restricts the webhook to those project keys (omit => all projects).
type WebhookConfig struct {
	URL       string   `yaml:"url"`
	Events    []string `yaml:"events"`
	SecretEnv string   `yaml:"secret_env"`
	Projects  []string `yaml:"projects"`
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
	IntervalSeconds int `yaml:"interval_seconds"`
	TimeoutSeconds  int `yaml:"timeout_seconds"`
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
	Token    string   `yaml:"token"`
	TokenEnv string   `yaml:"token_env"`
	Labels   []string `yaml:"labels"`
}

// CallerConfig identifies one authenticated submitter (C2). Token is the literal
// bearer token; TokenEnv reads it from the named environment variable instead
// (so secrets stay out of the config file). ID is recorded on the caller's jobs.
type CallerConfig struct {
	ID       string `yaml:"id"`
	Token    string `yaml:"token"`
	TokenEnv string `yaml:"token_env"`
	// E17 per-caller quota overrides (design §7.1). Each 0/empty value falls back to
	// the server.governance default; if that is also 0 the dimension is unlimited
	// (向后兼容). A value > 0 wins over the governance default.
	MaxConcurrentJobs int     `yaml:"max_concurrent_jobs"` // 同时在跑上限(信号量排队语义,超额排队不拒)
	RateLimit         float64 `yaml:"rate_limit"`          // 每秒提交请求数(令牌桶速率); 0 = 不限
	RateBurst         int     `yaml:"rate_burst"`          // 桶容量(突发); <=0 时取 max(1, ceil(RateLimit))
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
	DefaultExchangeSubdir string `yaml:"default_exchange_subdir"`
	DefaultResultSubdir   string `yaml:"default_result_subdir"`
	Root                  string `yaml:"root"`
	// DBPath is the optional explicit path to the SQLite metadata database. When
	// empty it is resolved by ResolveDBPath from Root / the config dir.
	DBPath string `yaml:"db_path"`
	// Retention bounds how many terminal jobs (and their logs) are kept; the
	// periodic prune in serve enforces it. Unset (all fields <= 0) disables prune.
	Retention RetentionConfig `yaml:"retention"`
}

// RetentionConfig is the YAML form of the job retention policy enforced by the
// serve prune loop (design §13 SP5). All fields default to 0 (disabled): with no
// retention configured the server never prunes (zero behaviour change).
type RetentionConfig struct {
	// MaxAgeDays, when > 0, prunes terminal jobs older than this many days.
	MaxAgeDays int `yaml:"max_age_days"`
	// MaxCount, when > 0, keeps only the newest MaxCount terminal jobs.
	MaxCount int `yaml:"max_count"`
	// IntervalMinutes is the prune cadence; <= 0 falls back to a default (60m) in
	// the serve loop. Only consulted when MaxAgeDays or MaxCount is > 0.
	IntervalMinutes int `yaml:"prune_interval_minutes"`
	// WorkflowMaxAgeDays is the INDEPENDENT workflow retention age (P1, design §5.4
	// / D22): when > 0, terminal workflows (done/failed/cancelled) older than this
	// many days are pruned along with their step-jobs and workflow_events. When 0 it
	// falls back to MaxAgeDays (WorkflowMaxAge), so a single job age policy also
	// bounds workflows; set it explicitly to keep workflows longer/shorter than jobs.
	WorkflowMaxAgeDays int `yaml:"workflow_max_age_days"`
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
	HostPath          string   `yaml:"host_path"`
	ContainerPath     string   `yaml:"container_path"`
	ExchangeSubdir    string   `yaml:"exchange_subdir"`
	ResultSubdir      string   `yaml:"result_subdir"`
	DefaultAgent      string   `yaml:"default_agent"`
	AllowedAgents     []string `yaml:"allowed_agents"`
	AllowedRunners    []string `yaml:"allowed_runners"`
	AllowExec         bool     `yaml:"allow_exec"`
	MaxConcurrentJobs int      `yaml:"max_concurrent_jobs"`
	// CaptureDiff toggles E12 git-diff capture (job-outcomes-audit, P3). It is a
	// pointer so "unset" (nil) can default to "on when cwd is a git work tree"
	// while an explicit capture_diff:false disables it outright. nil/true defer to
	// captureDiff's own is-git probe (a non-git cwd naturally yields no diff).
	CaptureDiff *bool `yaml:"capture_diff"`
	// NotifyEnabled gates E14 webhook delivery for this project (design §5.5). It
	// is a pointer so "unset" (nil) defaults to ENABLED while an explicit
	// notify_enabled:false suppresses all notification for the project's jobs
	// (no deliveries are enqueued). nil/true => notification on.
	NotifyEnabled *bool `yaml:"notify_enabled"`
}

// IsNotifyEnabled reports whether E14 webhook delivery is enabled for the
// project. Unset (nil) defaults to true; an explicit notify_enabled:false
// suppresses it.
func (p ProjectConfig) IsNotifyEnabled() bool { return p.NotifyEnabled == nil || *p.NotifyEnabled }

// AgentConfig describes a configurable CLI agent. Detect is refined in P3; P2
// only needs it to decode cleanly.
type AgentConfig struct {
	Type        string            `yaml:"type"`
	Command     string            `yaml:"command"`
	Args        []string          `yaml:"args"`
	Env         map[string]string `yaml:"env"`
	AllowRawCmd bool              `yaml:"allow_raw_cmd"`
	Detect      DetectConfig      `yaml:"detect"`
	// SessionInject 注入模式 argv 模板（模式①，首选）。非空 => 提交时 gofer 生成 uuid
	// 渲染追加到 argv，立即知 id、无需解析输出。{{session_id}} 占位（session-capture §6.4）。
	SessionInject []string `yaml:"session_inject"`
	// SessionCapture 捕获模式正则（模式②，兜底），第 1 个捕获组 = session_id。仅当
	// SessionInject 为空时使用（注入优先于捕获）。
	SessionCapture string `yaml:"session_capture"`
	// SessionResume resume 的整条 agent argv 模板（非追加 flag），{{session_id}}/{{prompt}}
	// 占位。供 `gofer job resume`（P2）拼接续接命令。
	SessionResume []string `yaml:"session_resume"`
	// SystemInject 是 per-agent 的 system prompt 注入 argv 模板（E35 角色，类比
	// SessionInject）。非空 + 请求带 system_prompt 时，submit 渲染 {{system_prompt}}
	// 追加到 argv（如 claude `--append-system-prompt <p>`）。保 argv 结构、不 shell
	// 拼接（SR403）。claude 有内置默认（applySystemDefaults），codex 留空待实测。
	SystemInject []string `yaml:"system_inject"`
	// McpServerName 是该 agent（codex）config.toml 里 gofer MCP server 的块名
	// （`[mcp_servers.<name>]`）。gap①(issue 7z6j)：codex 启动 MCP stdio 子进程用净化
	// env、不透传 codex 进程 env，故 role.env 注入 codex 进程对 MCP 子进程无效；改经
	// codex `-c mcp_servers.<name>.env.<KEY>=<VALUE>` 覆盖 MCP server env，使 sup 的
	// gofer MCP 自注册 role=supervisor。约定默认 `gofer`（agent.McpServerNameDefault），
	// 仅当 codex config 改了块名时才需配置此项。
	McpServerName string `yaml:"mcp_server_name"`
}

// DetectConfig is the agent availability probe. Placeholder in P2, refined P3.
type DetectConfig struct {
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
}

// RunnerConfig describes an execution location. peer-http fields are decoded in
// P2 but only used by the peer runner in P7. For type=worker (ws-worker), the
// runner targets a single registered worker identified by WorkerID; one
// worker-runner = one worker (dynamic routing is WP4 scheduling, not WP1).
type RunnerConfig struct {
	Type     string `yaml:"type"`
	BaseURL  string `yaml:"base_url"`
	TokenEnv string `yaml:"token_env"`
	// WorkerID is the worker this runner dispatches to (type=worker only). It
	// must match a server.workers entry.
	WorkerID string `yaml:"worker_id"`
}

// WorkerConfig is the top-level config for `gofer worker --config worker.yaml`
// (ws-worker §6). The worker runs jobs locally with its own project/agent/runner
// config and bridges log/status/result back over a single WebSocket to the hub.
type WorkerConfig struct {
	WorkerID      string                   `yaml:"worker_id"`
	ServerLink    WorkerServerLink         `yaml:"server_link"`
	Projects      map[string]ProjectConfig `yaml:"projects"`
	Agents        map[string]AgentConfig   `yaml:"agents"`
	Runners       map[string]RunnerConfig  `yaml:"runners"`
	MaxConcurrent int                      `yaml:"max_concurrent"`
	Labels        []string                 `yaml:"labels"`
	Storage       StorageConfig            `yaml:"storage"`
}

// WorkerServerLink describes how the worker reaches the hub. URLs may list
// MULTIPLE hub addresses (redundant entry points: VIPs of one hub, or several
// independent hubs); the worker rotates through them on a failed connect (C7,
// §5.2). Token/TokenEnv resolve the Bearer credential. Reconnect tunes the
// backoff + heartbeat timings (P3 §4).
type WorkerServerLink struct {
	URLs      []string        `yaml:"urls"`
	TokenEnv  string          `yaml:"token_env"`
	Token     string          `yaml:"token"`
	Reconnect ReconnectConfig `yaml:"reconnect"`
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
	InitialBackoffMS int `yaml:"initial_backoff_ms"`
	MaxBackoffMS     int `yaml:"max_backoff_ms"`
	PingIntervalSec  int `yaml:"ping_interval_sec"`
	ReadDeadlineSec  int `yaml:"read_deadline_sec"`
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

package httpapi

import (
	"net/http"
	"sort"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
)

type configView struct {
	Server     serverConfigView   `json:"server"`
	Storage    storageConfigView  `json:"storage"`
	Projects   []projectView      `json:"projects"`
	Agents     []configAgentView  `json:"agents"`
	Runners    []configRunnerView `json:"runners"`
	Roles      []configRoleView   `json:"roles"`
	Supervisor *supervisorView    `json:"supervisor,omitempty"`
	Presence   presenceConfigView `json:"presence"`
	Schedule   scheduleConfigView `json:"schedule"`
	// ServerPolicy / AgentPolicy publish the field policy table (WEB-04③ V1.1) keyed
	// by the FIELD NAME a write body carries. The console builds its edit forms from
	// them, so an input it offers is one the write endpoint accepts — the table is
	// consumed here and enforced there, never re-stated in the frontend.
	ServerPolicy map[string]fieldPolicyView `json:"server_policy"`
	AgentPolicy  map[string]fieldPolicyView `json:"agent_policy"`
}

// fieldPolicyView is one field's write contract as the console reads it (see
// config.FieldPolicy).
type fieldPolicyView struct {
	Editable        bool `json:"editable"`
	RestartRequired bool `json:"restart_required"`
	SecretRef       bool `json:"secret_ref,omitempty"`
}

type serverConfigView struct {
	Addr            string             `json:"addr"`
	PathView        string             `json:"path_view"`
	AllowEmptyToken bool               `json:"allow_empty_token"`
	WebEnabled      bool               `json:"web_enabled"`
	TokenSet        bool               `json:"token_set"`
	Governance      governanceView     `json:"governance"`
	Callers         []callerConfigView `json:"callers"`
	Workers         []workerConfigView `json:"workers"`
	RunnerProbe     runnerProbeView    `json:"runner_probe"`
	Notification    *notificationView  `json:"notification,omitempty"`
	Metrics         metricsConfigView  `json:"metrics"`
	// The edited-by-console knobs (WEB-04③ V1.1). Pointer fields are emitted as null
	// when unset, which is a DIFFERENT decision from 0 (inherit vs off) and is exactly
	// what the edit form has to send back.
	MaxJobTimeoutSec    int                 `json:"max_job_timeout_sec"`
	AutoResumeMax       *int                `json:"auto_resume_max"`
	StallTimeoutSec     *int                `json:"stall_timeout_sec"`
	JobRecoverWindowSec *int                `json:"job_recover_window_sec"`
	Retry               *config.RetryPolicy `json:"retry,omitempty"`
	// Skills is the deployment-wide default skill binding list (JOB-10 §一.3). It is
	// editable, so the console needs the value to prefill the input it writes back —
	// and the clear-on-omit rule means an absent field would read as "no bindings".
	Skills []string `json:"skills"`
	// CommentTrigger is the @-mention dispatch throttle (MCP-05 阶段 A). Both members
	// are pointers on the wire for the same reason they are pointers in the config:
	// null = the built-in default, 0 = that gate is off.
	CommentTrigger commentTriggerView `json:"comment_trigger"`
	// DirLock / AgentHealth are hot-editable (S2, 2026-09-23) and read per use, so the
	// console needs the raw values to prefill the inputs it writes back. Null means
	// "unset": for dir_lock the documented default is ON, for agent_health the
	// defaults are 1h/3/1 — neither is the same decision as an explicit zero block.
	DirLock     *bool                  `json:"dir_lock"`
	AgentHealth *serverAgentHealthView `json:"agent_health,omitempty"`
	// SkillLimits is the skill import size guard (JOB-10 §一.2). Editable as a whole
	// block, so the console needs the values to prefill the inputs it writes back.
	SkillLimits skillLimitsView `json:"skill_limits"`
	// JobEnvDenyList is the SEC-01 extra env denylist (server.job_env_denylist).
	// Editable, so the console reads it to prefill the input it writes back; it is the
	// keys ADDED to the built-in credential triple, never the triple itself (that is
	// not a knob a config file can turn off).
	JobEnvDenyList []string `json:"job_env_denylist"`
	// Tunnel is the TUN-03 hub-side tunnel visibility block. Editable as a whole block,
	// so the console needs the raw value to prefill the input it writes back (0 = the
	// 90s default, which is NOT the same decision as an explicit zero-length TTL).
	Tunnel serverTunnelView `json:"tunnel"`
}

// serverTunnelView is the server.tunnel block as the console edits it (TUN-03). Zero
// means tunnel.DefaultForwarderTTL (90s) rather than "expire immediately", so the
// value is echoed raw.
type serverTunnelView struct {
	ForwarderTTLSec int `json:"forwarder_ttl_sec"`
}

// skillLimitsView is the server.skill_limits block as the console edits it. Zero
// means "the store's documented default" (2MiB per file / 10MiB total) rather than
// "no files", so the values are echoed raw.
type skillLimitsView struct {
	MaxFileBytes  int64 `json:"max_file_bytes"`
	MaxTotalBytes int64 `json:"max_total_bytes"`
}

// serverAgentHealthView is the server.agent_health block as the console edits it
// (SUP-01 P3). It is not agentHealthView: that one is one AGENT's classified health,
// this one is the window/threshold policy every classification is computed from.
type serverAgentHealthView struct {
	WindowSec      int `json:"window_sec"`
	DegradedAfter  int `json:"degraded_after"`
	RecoverAfterOK int `json:"recover_after_ok"`
}

// commentTriggerView is the server.comment_trigger block as the console edits it.
type commentTriggerView struct {
	MinIntervalSec *int `json:"min_interval_sec"`
	MaxPerScope    *int `json:"max_per_scope"`
}

type governanceView struct {
	DefaultCallerMaxConcurrent int     `json:"default_caller_max_concurrent"`
	DefaultRateLimit           float64 `json:"default_rate_limit"`
	DefaultRateBurst           int     `json:"default_rate_burst"`
	RequireAnswerCapability    bool    `json:"require_answer_capability"`
	RequireAdminCapability     bool    `json:"require_admin_capability"`
	RequireAttachCapability    bool    `json:"require_attach_capability"`
}

type callerConfigView struct {
	ID                string  `json:"id"`
	TokenSet          bool    `json:"token_set"`
	CanAnswer         bool    `json:"can_answer"`
	CanAdmin          bool    `json:"can_admin"`
	MaxConcurrentJobs int     `json:"max_concurrent_jobs,omitempty"`
	RateLimit         float64 `json:"rate_limit,omitempty"`
	RateBurst         int     `json:"rate_burst,omitempty"`
}

type workerConfigView struct {
	ID       string   `json:"id"`
	TokenSet bool     `json:"token_set"`
	Labels   []string `json:"labels"`
}

type runnerProbeView struct {
	IntervalSeconds int `json:"interval_seconds"`
	TimeoutSeconds  int `json:"timeout_seconds"`
}

type metricsConfigView struct {
	Enabled  bool `json:"enabled"`
	TokenSet bool `json:"token_set"`
}

type notificationView struct {
	Webhooks    []webhookView `json:"webhooks"`
	AllowHosts  []string      `json:"allow_hosts"`
	AllowHTTP   bool          `json:"allow_http"`
	MaxAttempts int           `json:"max_attempts"`
	// Enabled is the master pause switch (S4). It is emitted as a plain bool (the
	// console edits it as a checkbox) while the WRITE side keeps it optional: an
	// omitted `enabled` in a patch leaves the configured value alone.
	Enabled bool `json:"enabled"`
}

type webhookView struct {
	URL    string   `json:"url"`
	Kind   string   `json:"kind"`
	Events []string `json:"events"`
	// Projects restricts the target to those project keys (empty = all).
	Projects []string `json:"projects"`
	// Enabled pauses this target without deleting it (S4).
	Enabled bool `json:"enabled"`
	// SecretSet reports whether a secret_env NAME is configured. The NAME itself is
	// deliberately never echoed (SR403 precedent: no read path in this API exposes an
	// env name), which is why a patch that omits secret_env INHERITS the current name
	// — see patchNotification.
	SecretSet bool `json:"secret_set"`
}

type storageConfigView struct {
	DefaultExchangeSubdir string        `json:"default_exchange_subdir"`
	DefaultResultSubdir   string        `json:"default_result_subdir"`
	Root                  string        `json:"root"`
	DBPath                string        `json:"db_path"`
	Retention             retentionView `json:"retention"`
	Cast                  castView      `json:"cast"`
}

// castView is the redacted cast recording config: it exposes whether recording
// and encryption are on and the retention TTL, but NEVER the key env name/value
// (SR403; D-P3-5 governance view does not echo the key).
type castView struct {
	Enabled           bool `json:"enabled"`
	RetentionTTLHours int  `json:"retention_ttl_hours"`
	EncryptionEnabled bool `json:"encryption_enabled"`
}

type retentionView struct {
	MaxAgeDays         int `json:"max_age_days"`
	MaxCount           int `json:"max_count"`
	IntervalMinutes    int `json:"prune_interval_minutes"`
	WorkflowMaxAgeDays int `json:"workflow_max_age_days"`
}

type configAgentView struct {
	Key            string           `json:"key"`
	Type           string           `json:"type"`
	Interactive    bool             `json:"interactive"`
	Command        string           `json:"command,omitempty"`
	Args           []string         `json:"args"`
	EnvKeys        []string         `json:"env_keys"`
	AllowRawCmd    bool             `json:"allow_raw_cmd"`
	Detect         detectConfigView `json:"detect"`
	SessionInject  []string         `json:"session_inject"`
	SessionCapture string           `json:"session_capture,omitempty"`
	SessionResume  []string         `json:"session_resume"`
	SystemInject   []string         `json:"system_inject"`
	McpServerName  string           `json:"mcp_server_name,omitempty"`
	// The rest of the editable field set (WEB-04③ V1.1), so the console's edit form can
	// prefill every input it is allowed to send. InteractiveArgs is deliberately NOT
	// omitempty and NOT nonNil()-ed: JSON `null` = batch-only, `[]` = interactive with
	// no extra argv (AGT-02), and the form must reproduce that distinction exactly.
	InteractiveArgs          []string            `json:"interactive_args"`
	ReadOnlyArgs             []string            `json:"read_only_args"`
	SessionResumeInteractive []string            `json:"session_resume_interactive"`
	TransientErrorPatterns   []string            `json:"transient_error_patterns"`
	FallbackAgents           []string            `json:"fallback_agents"`
	MaxConcurrent            int                 `json:"max_concurrent"`
	StallTimeoutSec          *int                `json:"stall_timeout_sec"`
	Retry                    *config.RetryPolicy `json:"retry,omitempty"`
	OutputFormat             string              `json:"output_format"`
	NDJSONKeep               []string            `json:"ndjson_keep"`
	NDJSONRaw                bool                `json:"ndjson_raw"`
	NDJSONEventsTo           string              `json:"ndjson_events_to"`
	NDJSONStdout             string              `json:"ndjson_stdout"`
	NDJSONStdoutPath         string              `json:"ndjson_stdout_path"`
	NDJSONFields             map[string][]string `json:"ndjson_fields"`
	ACP                      *acpConfigView      `json:"acp,omitempty"`
	Injected                 bool                `json:"injected,omitempty"`
	// Skills are this agent's own bindings (JOB-10 §一.3), like the server's list:
	// editable, so the view carries it for the console's form to prefill.
	Skills []string `json:"skills"`
	// CanSubmit / SubmitAgents are the SEC-01 submit gate (design §一.3). Editable,
	// and the agent write REPLACES the editable set — a body that omits a field clears
	// it — so a view that did not carry them would let the console's next unrelated
	// save silently revoke a submit grant (the JOB-10 `skills` incident, verbatim).
	CanSubmit    bool     `json:"can_submit"`
	SubmitAgents []string `json:"submit_agents"`
}

// acpConfigView is the acp-agent sub-block as the console edits it. It carries the two
// protocol settings a web form has any business changing (modes / permission_policy) —
// never anything secret (there is nothing secret in it).
type acpConfigView struct {
	Modes            map[string]string `json:"modes,omitempty"`
	PermissionPolicy string            `json:"permission_policy,omitempty"`
}

type detectConfigView struct {
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args"`
}

type configRunnerView struct {
	Key      string `json:"key"`
	Type     string `json:"type"`
	BaseURL  string `json:"base_url,omitempty"`
	TokenSet bool   `json:"token_set"`
	WorkerID string `json:"worker_id,omitempty"`
}

type configRoleView struct {
	Key          string   `json:"key"`
	Agent        string   `json:"agent"`
	SystemPrompt string   `json:"system_prompt,omitempty"`
	Project      string   `json:"project,omitempty"`
	Tags         []string `json:"tags"`
	EnvKeys      []string `json:"env_keys"`
}

// supervisorLeaderView is the `supervisor.leader` block (LEAD-02 — the S3 leftover: the
// block was configurable but unreadable over HTTP, so an operator could not tell from
// GET /v1/config whether leader rounds were even on). The numbers are the RESOLVED
// values (the accessors' defaults applied), i.e. what is in effect, while `agent` and
// `scopes` are as configured.
type supervisorLeaderView struct {
	Enabled           bool     `json:"enabled"`
	Agent             string   `json:"agent,omitempty"`
	Scopes            []string `json:"scopes"`
	MaxRoundsPerScope int      `json:"max_rounds_per_scope"`
	WakeDelaySec      int      `json:"wake_delay_sec"`
	OnMemberDone      bool     `json:"on_member_done"`
}

type supervisorView struct {
	Enabled                bool     `json:"enabled"`
	IntervalSec            int      `json:"interval_sec"`
	AutoAnswer             bool     `json:"auto_answer"`
	EscalateTo             string   `json:"escalate_to,omitempty"`
	MaxRoundsPerJob        int      `json:"max_rounds_per_job"`
	AllowPromptRegex       []string `json:"allow_prompt_regex"`
	OwnerAnswerTimeoutSec  int      `json:"owner_answer_timeout_sec"`
	DesiredSupervisors     int      `json:"desired_supervisors"`
	ReconcileRunner        string   `json:"reconcile_runner,omitempty"`
	ReconcileIntervalSec   int      `json:"reconcile_interval_sec"`
	ReconcilePrompt        string   `json:"reconcile_prompt,omitempty"`
	ReconcileJobTimeoutSec int      `json:"reconcile_job_timeout_sec"`
	// Leader is the leader-round block, absent when the config has none (the feature is
	// then off entirely — a plan's own switch cannot turn it on by itself).
	Leader *supervisorLeaderView `json:"leader,omitempty"`
}

type presenceConfigView struct {
	TTLSec           int `json:"ttl_sec"`
	MessageTTLSec    int `json:"message_ttl_sec"`
	PruneIntervalSec int `json:"prune_interval_sec"`
}

type scheduleConfigView struct {
	SweepIntervalSec int `json:"sweep_interval_sec"`
	MissGraceSec     int `json:"miss_grace_sec"`
}

// handleGetConfig returns the managed configuration as a redacted read-only view.
func (s *Server) handleGetConfig(c *rux.Context) {
	var cfg *config.Config
	if s.projects != nil {
		cfg = s.projects.Config()
	}
	c.JSON(http.StatusOK, buildConfigView(cfg))
}

func buildConfigView(cfg *config.Config) configView {
	policies := func() (map[string]fieldPolicyView, map[string]fieldPolicyView) {
		return policyViews("server"), policyViews("agents")
	}
	if cfg == nil {
		sp, ap := policies()
		return configView{
			Projects:     []projectView{},
			Agents:       []configAgentView{},
			Runners:      []configRunnerView{},
			Roles:        []configRoleView{},
			ServerPolicy: sp,
			AgentPolicy:  ap,
		}
	}
	sp, ap := policies()
	return configView{
		Server:       buildServerConfigView(cfg.Server),
		Storage:      buildStorageConfigView(cfg.Storage),
		Projects:     buildProjectViews(cfg.Projects),
		Agents:       buildAgentViews(cfg.Agents, cfg.InjectedAgents()),
		Runners:      buildRunnerViews(cfg.Runners),
		Roles:        buildRoleViews(cfg.Roles),
		Supervisor:   buildSupervisorView(cfg.Supervisor),
		ServerPolicy: sp,
		AgentPolicy:  ap,
		Presence: presenceConfigView{
			TTLSec:           cfg.Presence.TTLSec,
			MessageTTLSec:    cfg.Presence.MessageTTLSec,
			PruneIntervalSec: cfg.Presence.PruneIntervalSec,
		},
		Schedule: scheduleConfigView{
			SweepIntervalSec: cfg.Schedule.SweepIntervalSec,
			MissGraceSec:     cfg.Schedule.MissGraceSec,
		},
	}
}

// policyViews renders one section's field policies for the console, keyed by the
// field name a write body carries (see the configView comment).
func policyViews(section string) map[string]fieldPolicyView {
	policies := config.SectionPolicies(section)
	out := make(map[string]fieldPolicyView, len(policies))
	for name, fp := range policies {
		out[name] = fieldPolicyView{Editable: fp.Editable, RestartRequired: fp.RestartRequired, SecretRef: fp.SecretRef}
	}
	return out
}

func buildServerConfigView(sc config.ServerConfig) serverConfigView {
	return serverConfigView{
		Addr:            sc.Addr,
		PathView:        sc.PathView,
		AllowEmptyToken: sc.AllowEmptyToken,
		WebEnabled:      sc.IsWebEnabled(),
		TokenSet:        sc.Token != "" || sc.TokenEnv != "",
		Governance: governanceView{
			DefaultCallerMaxConcurrent: sc.Governance.DefaultCallerMaxConcurrent,
			DefaultRateLimit:           sc.Governance.DefaultRateLimit,
			DefaultRateBurst:           sc.Governance.DefaultRateBurst,
			RequireAnswerCapability:    sc.Governance.RequireAnswerCapability,
			RequireAdminCapability:     sc.Governance.RequireAdminCapability,
			RequireAttachCapability:    sc.Governance.RequireAttachCapability,
		},
		Callers: buildCallerViews(sc.Callers),
		Workers: buildWorkerViews(sc.Workers),
		RunnerProbe: runnerProbeView{
			IntervalSeconds: sc.RunnerProbe.IntervalSeconds,
			TimeoutSeconds:  sc.RunnerProbe.TimeoutSeconds,
		},
		Notification: buildNotificationView(sc.Notification),
		Metrics: metricsConfigView{
			Enabled:  sc.Metrics.IsEnabled(),
			TokenSet: sc.Metrics.Token != "",
		},
		MaxJobTimeoutSec:    sc.MaxJobTimeoutSec,
		AutoResumeMax:       sc.AutoResumeMax,
		StallTimeoutSec:     sc.StallTimeoutSec,
		JobRecoverWindowSec: sc.JobRecoverWindowSec,
		Retry:               sc.Retry,
		Skills:              nonNil(sc.Skills),
		// MCP-05: the @-mention throttle, editable — so the console needs the values to
		// prefill the inputs it writes back (nil = "unset", which is NOT 0 = "off").
		CommentTrigger: commentTriggerView{
			MinIntervalSec: sc.CommentTrigger.MinIntervalSec,
			MaxPerScope:    sc.CommentTrigger.MaxPerScope,
		},
		DirLock:     sc.DirLock,
		AgentHealth: buildAgentHealthView(sc.AgentHealth),
		SkillLimits: skillLimitsView{
			MaxFileBytes:  sc.SkillLimits.MaxFileBytes,
			MaxTotalBytes: sc.SkillLimits.MaxTotalBytes,
		},
		JobEnvDenyList: nonNil(sc.JobEnvDenyList),
		Tunnel:         serverTunnelView{ForwarderTTLSec: sc.Tunnel.ForwarderTTLSec},
	}
}

// buildAgentHealthView renders the agent-health block (nil = the block is unset and
// the defaults apply, which must stay distinguishable from a zeroed block).
func buildAgentHealthView(h *config.AgentHealthConfig) *serverAgentHealthView {
	if h == nil {
		return nil
	}
	return &serverAgentHealthView{
		WindowSec:      h.WindowSec,
		DegradedAfter:  h.DegradedAfter,
		RecoverAfterOK: h.RecoverAfterOK,
	}
}

func buildCallerViews(in []config.CallerConfig) []callerConfigView {
	out := make([]callerConfigView, 0, len(in))
	for _, cc := range in {
		out = append(out, callerConfigView{
			ID:                cc.ID,
			TokenSet:          cc.Token != "" || cc.TokenEnv != "",
			CanAnswer:         cc.CanAnswer,
			CanAdmin:          cc.CanAdmin,
			MaxConcurrentJobs: cc.MaxConcurrentJobs,
			RateLimit:         cc.RateLimit,
			RateBurst:         cc.RateBurst,
		})
	}
	return out
}

func buildWorkerViews(in map[string]config.WorkerAuthConfig) []workerConfigView {
	keys := sortedMapKeys(in)
	out := make([]workerConfigView, 0, len(keys))
	for _, k := range keys {
		wc := in[k]
		out = append(out, workerConfigView{
			ID:       k,
			TokenSet: wc.Token != "" || wc.TokenEnv != "",
			Labels:   nonNil(wc.Labels),
		})
	}
	return out
}

func buildNotificationView(n *config.NotificationConfig) *notificationView {
	if n == nil {
		return nil
	}
	out := &notificationView{
		Webhooks:    make([]webhookView, 0, len(n.Webhooks)),
		AllowHosts:  nonNil(n.AllowHosts),
		AllowHTTP:   n.AllowHTTP,
		MaxAttempts: n.MaxAttempts,
		Enabled:     n.IsEnabled(),
	}
	for _, wh := range n.Webhooks {
		out.Webhooks = append(out.Webhooks, webhookView{
			URL:       wh.URL,
			Kind:      wh.Kind,
			Events:    nonNil(wh.Events),
			Projects:  nonNil(wh.Projects),
			Enabled:   wh.IsEnabled(),
			SecretSet: wh.SecretEnv != "",
		})
	}
	return out
}

func buildStorageConfigView(sc config.StorageConfig) storageConfigView {
	return storageConfigView{
		DefaultExchangeSubdir: sc.DefaultExchangeSubdir,
		DefaultResultSubdir:   sc.DefaultResultSubdir,
		Root:                  sc.Root,
		DBPath:                sc.DBPath,
		Retention: retentionView{
			MaxAgeDays:         sc.Retention.MaxAgeDays,
			MaxCount:           sc.Retention.MaxCount,
			IntervalMinutes:    sc.Retention.IntervalMinutes,
			WorkflowMaxAgeDays: sc.Retention.WorkflowMaxAgeDays,
		},
		Cast: castView{
			Enabled:           sc.Cast.Enabled,
			RetentionTTLHours: sc.Cast.RetentionTTLHours,
			EncryptionEnabled: sc.Cast.Encryption.Enabled,
		},
	}
}

func buildProjectViews(projects map[string]config.ProjectConfig) []projectView {
	keys := sortedMapKeys(projects)
	out := make([]projectView, 0, len(keys))
	for _, k := range keys {
		out = append(out, projectViewOf(k, projects[k]))
	}
	return out
}

func buildAgentViews(agents map[string]config.AgentConfig, injected map[string]bool) []configAgentView {
	keys := sortedMapKeys(agents)
	out := make([]configAgentView, 0, len(keys))
	for _, k := range keys {
		ac := agents[k]
		var acp *acpConfigView
		if ac.ACP != nil {
			acp = &acpConfigView{Modes: ac.ACP.Modes, PermissionPolicy: ac.ACP.PermissionPolicy}
		}
		out = append(out, configAgentView{
			Key:            k,
			Type:           ac.Type,
			Interactive:    ac.Interactive,
			Command:        ac.Command,
			Args:           nonNil(ac.Args),
			EnvKeys:        sortedMapKeys(ac.Env),
			AllowRawCmd:    ac.AllowRawCmd,
			Detect:         detectConfigView{Command: ac.Detect.Command, Args: nonNil(ac.Detect.Args)},
			SessionInject:  nonNil(ac.SessionInject),
			SessionCapture: ac.SessionCapture,
			SessionResume:  nonNil(ac.SessionResume),
			SystemInject:   nonNil(ac.SystemInject),
			McpServerName:  ac.McpServerName,

			// nil stays null: it is what says "batch-only" (see the struct comment).
			InteractiveArgs:          ac.InteractiveArgs,
			ReadOnlyArgs:             nonNil(ac.ReadOnlyArgs),
			SessionResumeInteractive: nonNil(ac.SessionResumeInteractive),
			TransientErrorPatterns:   nonNil(ac.TransientErrorPatterns),
			FallbackAgents:           nonNil(ac.FallbackAgents),
			MaxConcurrent:            ac.MaxConcurrent,
			StallTimeoutSec:          ac.StallTimeoutSec,
			Retry:                    ac.Retry,
			OutputFormat:             ac.OutputFormat,
			NDJSONKeep:               nonNil(ac.NDJSONKeep),
			NDJSONRaw:                ac.NDJSONRaw,
			NDJSONEventsTo:           ac.NDJSONEventsTo,
			NDJSONStdout:             ac.NDJSONStdout,
			NDJSONStdoutPath:         ac.NDJSONStdoutPath,
			NDJSONFields:             ac.NDJSONFields,
			ACP:                      acp,
			Injected:                 injected[k],
			Skills:                   nonNil(ac.Skills),
			CanSubmit:                ac.CanSubmit,
			SubmitAgents:             nonNil(ac.SubmitAgents),
		})
	}
	return out
}

func buildRunnerViews(runners map[string]config.RunnerConfig) []configRunnerView {
	keys := sortedMapKeys(runners)
	out := make([]configRunnerView, 0, len(keys))
	for _, k := range keys {
		rc := runners[k]
		out = append(out, configRunnerView{
			Key:      k,
			Type:     rc.Type,
			BaseURL:  rc.BaseURL,
			TokenSet: rc.TokenEnv != "",
			WorkerID: rc.WorkerID,
		})
	}
	return out
}

func buildRoleViews(roles map[string]config.RoleConfig) []configRoleView {
	keys := sortedMapKeys(roles)
	out := make([]configRoleView, 0, len(keys))
	for _, k := range keys {
		rc := roles[k]
		out = append(out, configRoleView{
			Key:          k,
			Agent:        rc.Agent,
			SystemPrompt: rc.SystemPrompt,
			Project:      rc.Project,
			Tags:         nonNil(rc.Tags),
			EnvKeys:      sortedMapKeys(rc.Env),
		})
	}
	return out
}

func buildSupervisorView(sc *config.SupervisorConfig) *supervisorView {
	if sc == nil {
		return nil
	}
	return &supervisorView{
		Enabled:                sc.Enabled,
		IntervalSec:            sc.IntervalSec,
		AutoAnswer:             sc.AutoAnswer,
		EscalateTo:             sc.EscalateTo,
		MaxRoundsPerJob:        sc.MaxRoundsPerJob,
		AllowPromptRegex:       nonNil(sc.AllowPromptRegex),
		OwnerAnswerTimeoutSec:  sc.OwnerAnswerTimeoutSec,
		DesiredSupervisors:     sc.DesiredSupervisors,
		ReconcileRunner:        sc.ReconcileRunner,
		ReconcileIntervalSec:   sc.ReconcileIntervalSec,
		ReconcilePrompt:        sc.ReconcilePrompt,
		ReconcileJobTimeoutSec: sc.ReconcileJobTimeoutSec,
		Leader:                 buildSupervisorLeaderView(sc.Leader),
	}
}

func buildSupervisorLeaderView(lc *config.LeaderConfig) *supervisorLeaderView {
	if lc == nil {
		return nil
	}
	scopes := lc.Scopes
	if len(scopes) == 0 {
		scopes = []string{config.LeaderPlanScope} // the default the accessor applies
	}
	return &supervisorLeaderView{
		Enabled:           lc.Enabled,
		Agent:             lc.Agent,
		Scopes:            scopes,
		MaxRoundsPerScope: lc.MaxRounds(),
		WakeDelaySec:      int(lc.WakeDelay().Seconds()),
		OnMemberDone:      lc.MemberDoneWakes(),
	}
}

func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

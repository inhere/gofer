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
}

type webhookView struct {
	URL       string   `json:"url"`
	Events    []string `json:"events"`
	SecretSet bool     `json:"secret_set"`
	Projects  []string `json:"projects"`
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
	if cfg == nil {
		return configView{
			Projects: []projectView{},
			Agents:   []configAgentView{},
			Runners:  []configRunnerView{},
			Roles:    []configRoleView{},
		}
	}
	return configView{
		Server:     buildServerConfigView(cfg.Server),
		Storage:    buildStorageConfigView(cfg.Storage),
		Projects:   buildProjectViews(cfg.Projects),
		Agents:     buildAgentViews(cfg.Agents),
		Runners:    buildRunnerViews(cfg.Runners),
		Roles:      buildRoleViews(cfg.Roles),
		Supervisor: buildSupervisorView(cfg.Supervisor),
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
	}
	for _, wh := range n.Webhooks {
		out.Webhooks = append(out.Webhooks, webhookView{
			URL:       wh.URL,
			Events:    nonNil(wh.Events),
			SecretSet: wh.SecretEnv != "",
			Projects:  nonNil(wh.Projects),
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
		p := projects[k]
		out = append(out, projectView{
			Key:               k,
			HostPath:          p.HostPath,
			ContainerPath:     p.ContainerPath,
			DefaultAgent:      p.DefaultAgent,
			AllowedAgents:     p.AllowedAgents,
			AllowedRunners:    p.AllowedRunners,
			AllowExec:         p.AllowExec,
			MaxConcurrentJobs: p.MaxConcurrentJobs,
		})
	}
	return out
}

func buildAgentViews(agents map[string]config.AgentConfig) []configAgentView {
	keys := sortedMapKeys(agents)
	out := make([]configAgentView, 0, len(keys))
	for _, k := range keys {
		ac := agents[k]
		out = append(out, configAgentView{
			Key:            k,
			Type:           ac.Type,
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

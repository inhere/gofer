package httpapi

import (
	"github.com/inhere/gofer/internal/config"
)

// The rest of the configuration, as GET /v1/config publishes it (gofer-4uk5).
//
// The console used to read only server/storage/projects/agents/runners/roles/leader/
// schedule, so most of a real config.yaml was invisible from the web. The types below
// cover every remaining top-level section and the server.* blocks that were missing; the
// rule for secrets is the one the rest of this API already follows — a value is never
// echoed, only whether it is configured (…_set), and env/header names stay hidden where
// they themselves were treated as sensitive (webhook secret_env precedent).
//
// Everything here is READ-ONLY over the API: the write surface is still only
// PUT /v1/config/server, /v1/config/work, /v1/config/steward and the agent endpoints, and
// the console marks these sections read-only.

type logConfigView struct {
	File       string `json:"file,omitempty"`
	Dir        string `json:"dir,omitempty"`
	MaxSizeMB  int    `json:"max_size_mb"`
	MaxAgeDays int    `json:"max_age_days"`
	MaxBackups int    `json:"max_backups"`
}

type wakeupConfigView struct {
	// TTLSec is the RESOLVED lifetime (the documented default when unset).
	TTLSec int `json:"ttl_sec"`
}

type ptyConfigView struct {
	TranscriptMaxBytes int64 `json:"transcript_max_bytes"`
}

type sessionConfigView struct {
	AutoRelayIdleSec             *int     `json:"auto_relay_idle_sec"`
	AutoRelayTurnSec             *int     `json:"auto_relay_turn_sec"`
	InjectCommands               []string `json:"inject_commands"`
	TakeoverInputDelayMs         *int     `json:"takeover_input_delay_ms"`
	AutoRelaySkipWhenSupervising *bool    `json:"auto_relay_skip_when_supervising"`
	SupervisingWindowSec         *int     `json:"supervising_window_sec"`
	ProgressIntervalSec          *int     `json:"progress_interval_sec"`
	OfflineAfterSec              *int     `json:"offline_after_sec"`
	TakeoverAliveSec             *int     `json:"takeover_alive_sec"`
	RelayOnWaitSec               *int     `json:"relay_on_wait_sec"`
	RelayAutoWaitSec             *int     `json:"relay_auto_wait_sec"`
	StopJobWait                  string   `json:"stop_job_wait,omitempty"`
}

type workConfigView struct {
	DigestTime               string   `json:"digest_time,omitempty"`
	DigestEnabled            *bool    `json:"digest_enabled"`
	SummarizerAgent          string   `json:"summarizer_agent,omitempty"`
	SummarizerArgs           []string `json:"summarizer_args"`
	SummarizerProject        string   `json:"summarizer_project,omitempty"`
	SummarizerSessionMaxRuns int      `json:"summarizer_session_max_runs"`
	SummarizerSessionMaxAge  string   `json:"summarizer_session_max_age,omitempty"`
	SummarizeEnabled         *bool    `json:"summarize_enabled"`
	SummarizeIdleMin         int      `json:"summarize_idle_min"`
	SummarizeMinIntervalMin  int      `json:"summarize_min_interval_min"`
	SummarizeDailyLimit      int      `json:"summarize_daily_limit"`
	AutoHandoff              *bool    `json:"auto_handoff"`
	RequestTimeoutMin        int      `json:"request_timeout_min"`
	NeedsMeNotify            *bool    `json:"needs_me_notify"`
	NeedsMeThrottleMin       int      `json:"needs_me_throttle_min"`
	StallAfter               string   `json:"stall_after,omitempty"`
}

type stewardConfigView struct {
	Enabled          bool   `json:"enabled"`
	Agent            string `json:"agent,omitempty"`
	Project          string `json:"project,omitempty"`
	ReviewTime       string `json:"review_time,omitempty"`
	IdleEndMin       int    `json:"idle_end_min"`
	ReviewMaxItems   int    `json:"review_max_items"`
	EventWake        bool   `json:"event_wake"`
	EventThrottleMin int    `json:"event_throttle_min"`
}

type mailConfigView struct {
	TickSec                int             `json:"tick_sec"`
	WakeIdleSec            int             `json:"wake_idle_sec"`
	WakeIntervalSec        int             `json:"wake_interval_sec"`
	DeliverBySec           int             `json:"deliver_by_sec"`
	ClaimLeaseSec          int             `json:"claim_lease_sec"`
	BroadcastMaxRecipients int             `json:"broadcast_max_recipients"`
	BroadcastRate          string          `json:"broadcast_rate,omitempty"`
	AgentsMaxResident      int             `json:"agents_max_resident"`
	Agents                 []mailAgentView `json:"agents"`
}

type mailAgentView struct {
	Key         string   `json:"key"`
	Agent       string   `json:"agent,omitempty"`
	Runner      string   `json:"runner,omitempty"`
	Args        []string `json:"args"`
	IdleMinutes int      `json:"idle_minutes"`
	MaxLetters  int      `json:"max_letters"`
	MaxHours    int      `json:"max_hours"`
}

type toolView struct {
	Name string `json:"name"`
	Desc string `json:"desc,omitempty"`
	Bin  string `json:"bin,omitempty"`
}

type mcpServerConfigView struct {
	Key        string   `json:"key"`
	Desc       string   `json:"desc,omitempty"`
	Type       string   `json:"type,omitempty"`
	Command    string   `json:"command,omitempty"`
	Args       []string `json:"args"`
	URL        string   `json:"url,omitempty"`
	EnvKeys    []string `json:"env_keys"`
	HeaderKeys []string `json:"header_keys"`
	// SecretEnvSet / SecretHeaderKeys report the secret indirection without naming the
	// env vars it reads (the webhook secret_env rule).
	SecretEnvSet     bool     `json:"secret_env_set"`
	SecretHeaderKeys []string `json:"secret_header_keys"`
	Tools            []string `json:"tools"`
	Enabled          bool     `json:"enabled"`
}

type secretConfigView struct {
	Key         string `json:"key"`
	Description string `json:"description,omitempty"`
	As          string `json:"as,omitempty"`
	// Set reports whether the indirection is configured at all. The env NAME it reads is
	// deliberately not echoed (no read path in this API exposes an env name).
	Set bool `json:"set"`
}

type modelPriceView struct {
	Model      string  `json:"model"`
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

type httpProxyConfigView struct {
	// ProxyURLSet reports whether an outbound proxy is configured; the URL may carry
	// credentials, so it is never echoed.
	ProxyURLSet bool     `json:"proxy_url_set"`
	AllowSites  []string `json:"allow_sites"`
}

// The server.* blocks the console could not see.

type tlsConfigView struct {
	Addr     string `json:"addr,omitempty"`
	CertFile string `json:"cert_file,omitempty"`
	KeyFile  string `json:"key_file,omitempty"`
	Enabled  bool   `json:"enabled"`
}

type updateConfigView struct {
	Enabled     bool   `json:"enabled"`
	Interval    string `json:"interval,omitempty"`
	Repo        string `json:"repo,omitempty"`
	LatestURL   string `json:"latest_url,omitempty"`
	URLTemplate string `json:"url_template,omitempty"`
	// ProxySet reports whether the check goes through a proxy (the URL may carry
	// credentials).
	ProxySet bool `json:"proxy_set"`
}

type workerEnrollmentConfigView struct {
	workerEnrollmentView
	TrustedProxies []string `json:"trusted_proxies"`
}

type pushConfigView struct {
	VAPIDSubject string `json:"vapid_subject,omitempty"`
}

type sessionMessagingView struct {
	Enabled             bool   `json:"enabled"`
	MessengerCommand    string `json:"messenger_command,omitempty"`
	MessengerTimeoutSec int    `json:"messenger_timeout_sec"`
	MessengerIdleSec    int    `json:"messenger_idle_sec"`
	// The rotation limits are RESOLVED (the shipped default when unset).
	MessengerMaxMessages int `json:"messenger_max_messages"`
	MessengerMaxAgeSec   int `json:"messenger_max_age_sec"`
}

type policyRepushView struct {
	TimeoutSec  int `json:"timeout_sec"`
	MaxAttempts int `json:"max_attempts"`
}

type agentFallbackView struct {
	OnFailure   bool `json:"on_failure"`
	PreDispatch bool `json:"pre_dispatch"`
	// Configured is false when the block is absent (the defaults apply).
	Configured bool `json:"configured"`
}

type xferConfigView struct {
	MaxBytes        int64 `json:"max_bytes"`
	TTLSec          int   `json:"ttl_sec"`
	CollectMaxBytes int64 `json:"collect_max_bytes"`
}

type attachmentsConfigView struct {
	MaxBytes        int64 `json:"max_bytes"`
	MaxPerMessage   int   `json:"max_per_message"`
	HourlyPerCaller int   `json:"hourly_per_caller"`
}

type holdConfigView struct {
	DefaultTimeoutSec int `json:"default_timeout_sec"`
	MaxTimeoutSec     int `json:"max_timeout_sec"`
}

type desktopNotifyView struct {
	Enabled  bool     `json:"enabled"`
	Events   []string `json:"events"`
	Projects []string `json:"projects"`
	MergeSec int      `json:"merge_sec"`
}

type storageExtraView struct {
	MailRetentionDays       int `json:"mail_retention_days"`
	AttachmentRetentionDays int `json:"attachment_retention_days"`
}

func buildLogConfigView(lc config.LogConfig) logConfigView {
	return logConfigView{File: lc.File, Dir: lc.Dir, MaxSizeMB: lc.MaxSizeMB, MaxAgeDays: lc.MaxAgeDays, MaxBackups: lc.MaxBackups}
}

func buildWakeupConfigView(wc config.WakeupConfig) wakeupConfigView {
	return wakeupConfigView{TTLSec: wc.TTLSec}
}

func buildSessionConfigView(sc config.SessionConfig) sessionConfigView {
	return sessionConfigView{
		AutoRelayIdleSec:             sc.AutoRelayIdleSec,
		AutoRelayTurnSec:             sc.AutoRelayTurnSec,
		InjectCommands:               nonNil(sc.InjectCommands),
		TakeoverInputDelayMs:         sc.TakeoverInputDelayMs,
		AutoRelaySkipWhenSupervising: sc.AutoRelaySkipWhenSupervising,
		SupervisingWindowSec:         sc.SupervisingWindowSec,
		ProgressIntervalSec:          sc.ProgressIntervalSec,
		OfflineAfterSec:              sc.OfflineAfterSec,
		TakeoverAliveSec:             sc.TakeoverAliveSec,
		RelayOnWaitSec:               sc.RelayOnWaitSec,
		RelayAutoWaitSec:             sc.RelayAutoWaitSec,
		StopJobWait:                  sc.StopJobWait,
	}
}

func buildWorkConfigView(wc config.WorkConfig) workConfigView {
	return workConfigView{
		DigestTime:               wc.DigestTime,
		DigestEnabled:            wc.DigestEnabled,
		SummarizerAgent:          wc.SummarizerAgent,
		SummarizerArgs:           nonNil(wc.SummarizerArgs),
		SummarizerProject:        wc.SummarizerProject,
		SummarizerSessionMaxRuns: wc.SummarizerSessionMaxRuns,
		SummarizerSessionMaxAge:  wc.SummarizerSessionMaxAge,
		SummarizeEnabled:         wc.SummarizeEnabled,
		SummarizeIdleMin:         wc.SummarizeIdleMin,
		SummarizeMinIntervalMin:  wc.SummarizeMinIntervalMin,
		SummarizeDailyLimit:      wc.SummarizeDailyLimit,
		AutoHandoff:              wc.AutoHandoff,
		RequestTimeoutMin:        wc.RequestTimeoutMin,
		NeedsMeNotify:            wc.NeedsMeNotify,
		NeedsMeThrottleMin:       wc.NeedsMeThrottleMin,
		StallAfter:               wc.StallAfter,
	}
}

func buildStewardConfigView(sc config.StewardConfig) stewardConfigView {
	return stewardConfigView{
		Enabled:          sc.Enabled,
		Agent:            sc.Agent,
		Project:          sc.Project,
		ReviewTime:       sc.ReviewTime,
		IdleEndMin:       sc.IdleEndMin,
		ReviewMaxItems:   sc.ReviewMaxItems,
		EventWake:        sc.EventWake,
		EventThrottleMin: sc.EventThrottleMin,
	}
}

func buildMailConfigView(mc config.MailConfig) mailConfigView {
	keys := sortedMapKeys(mc.Agents)
	out := mailConfigView{
		TickSec:                mc.TickSec,
		WakeIdleSec:            mc.WakeIdleSec,
		WakeIntervalSec:        mc.WakeIntervalSec,
		DeliverBySec:           mc.DeliverBySec,
		ClaimLeaseSec:          mc.ClaimLeaseSec,
		BroadcastMaxRecipients: mc.BroadcastMaxRecipients,
		BroadcastRate:          mc.BroadcastRate,
		AgentsMaxResident:      mc.AgentsMaxResident,
		Agents:                 make([]mailAgentView, 0, len(keys)),
	}
	for _, k := range keys {
		a := mc.Agents[k]
		out.Agents = append(out.Agents, mailAgentView{
			Key: k, Agent: a.Agent, Runner: a.Runner, Args: nonNil(a.Args),
			IdleMinutes: a.IdleMinutes, MaxLetters: a.EffectiveMaxLetters(), MaxHours: a.EffectiveMaxHours(),
		})
	}
	return out
}

func buildToolViews(tools []config.Tool) []toolView {
	out := make([]toolView, 0, len(tools))
	for _, t := range tools {
		out = append(out, toolView{Name: t.Name, Desc: t.Desc, Bin: t.Bin})
	}
	return out
}

func buildMCPServerConfigViews(servers map[string]config.MCPServerConfig) []mcpServerConfigView {
	keys := sortedMapKeys(servers)
	out := make([]mcpServerConfigView, 0, len(keys))
	for _, k := range keys {
		sc := servers[k]
		out = append(out, mcpServerConfigView{
			Key:              k,
			Desc:             sc.Desc,
			Type:             sc.Type,
			Command:          sc.Command,
			Args:             nonNil(sc.Args),
			URL:              sc.URL,
			EnvKeys:          sortedMapKeys(sc.Env),
			HeaderKeys:       sortedMapKeys(sc.Headers),
			SecretEnvSet:     len(sc.SecretEnv) > 0,
			SecretHeaderKeys: sortedMapKeys(sc.SecretHeaders),
			Tools:            nonNil(sc.Tools),
			Enabled:          sc.IsEnabled(),
		})
	}
	return out
}

func buildSecretConfigViews(secrets map[string]config.SecretConfig) []secretConfigView {
	keys := sortedMapKeys(secrets)
	out := make([]secretConfigView, 0, len(keys))
	for _, k := range keys {
		sc := secrets[k]
		out = append(out, secretConfigView{Key: k, Description: sc.Description, As: sc.As, Set: sc.Env != ""})
	}
	return out
}

func buildModelPriceViews(prices map[string]config.ModelPrice) []modelPriceView {
	keys := sortedMapKeys(prices)
	out := make([]modelPriceView, 0, len(keys))
	for _, k := range keys {
		p := prices[k]
		out = append(out, modelPriceView{Model: k, Input: p.Input, Output: p.Output, CacheRead: p.CacheRead, CacheWrite: p.CacheWrite})
	}
	return out
}

func buildHTTPProxyConfigView(hc config.HTTPProxyConfig) httpProxyConfigView {
	return httpProxyConfigView{ProxyURLSet: hc.ProxyURL != "", AllowSites: nonNil(hc.AllowSites)}
}

func buildTLSConfigView(tc *config.TLSConfig) *tlsConfigView {
	if tc == nil {
		return nil
	}
	return &tlsConfigView{Addr: tc.Addr, CertFile: tc.CertFile, KeyFile: tc.KeyFile, Enabled: tc.Addr != ""}
}

func buildUpdateConfigView(uc config.UpdateConfig) updateConfigView {
	return updateConfigView{
		Enabled:     uc.IsEnabled(),
		Interval:    uc.Interval,
		Repo:        uc.Repo,
		LatestURL:   uc.LatestURL,
		URLTemplate: uc.URLTemplate,
		ProxySet:    uc.Proxy != "",
	}
}

// buildWorkerEnrollmentView renders server.worker_enrollment for the console. The view
// type itself (shared with the write endpoint) lives in config_write_handler.go, and the
// proxy list is added on top of it here.
func buildWorkerEnrollmentView(we config.WorkerEnrollmentConfig) workerEnrollmentConfigView {
	return workerEnrollmentConfigView{
		workerEnrollmentView: workerEnrollmentView{
			Enabled:         we.Enabled,
			DecideSec:       we.DecideSec,
			CollectSec:      we.CollectSec,
			PerSourcePerMin: we.PerSourcePerMin,
			MaxPending:      we.MaxPending,
		},
		TrustedProxies: nonNil(we.TrustedProxies),
	}
}

func buildSessionMessagingView(sm config.SessionMessagingConfig) sessionMessagingView {
	return sessionMessagingView{
		Enabled:              sm.Enabled == nil || *sm.Enabled,
		MessengerCommand:     sm.MessengerCommand,
		MessengerTimeoutSec:  sm.MessengerTimeoutSec,
		MessengerIdleSec:     sm.MessengerIdleSec,
		MessengerMaxMessages: sm.MaxMessages(),
		MessengerMaxAgeSec:   int(sm.MaxAge().Seconds()),
	}
}

func buildPolicyRepushView(pr *config.PolicyRepushConfig) *policyRepushView {
	if pr == nil {
		return nil
	}
	return &policyRepushView{TimeoutSec: pr.TimeoutSec, MaxAttempts: pr.MaxAttempts}
}

func buildAgentFallbackView(af *config.AgentFallbackConfig) agentFallbackView {
	if af == nil {
		return agentFallbackView{Configured: false}
	}
	return agentFallbackView{
		Configured:  true,
		OnFailure:   af.OnFailure == nil || *af.OnFailure,
		PreDispatch: af.PreDispatch,
	}
}

func buildXferConfigView(xc config.XferConfig) xferConfigView {
	return xferConfigView{MaxBytes: xc.MaxBytes, TTLSec: xc.TTLSec, CollectMaxBytes: xc.CollectMaxBytes}
}

func buildAttachmentsConfigView(ac config.AttachmentsConfig) attachmentsConfigView {
	return attachmentsConfigView{MaxBytes: ac.MaxBytes, MaxPerMessage: ac.MaxPerMessage, HourlyPerCaller: ac.HourlyPerCaller}
}

func buildHoldConfigView(hc config.HoldConfig) holdConfigView {
	return holdConfigView{DefaultTimeoutSec: hc.DefaultTimeoutSec, MaxTimeoutSec: hc.MaxTimeoutSec}
}

func buildDesktopNotifyView(dn *config.DesktopNotifyConfig) desktopNotifyView {
	return desktopNotifyView{
		Enabled:  dn.IsEnabled(),
		Events:   nonNil(dn.Events),
		Projects: nonNil(dn.Projects),
		MergeSec: dn.EffectiveMergeSec(),
	}
}

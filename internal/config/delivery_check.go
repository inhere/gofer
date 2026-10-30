package config

import (
	"fmt"
	"strings"
	"time"
)

// DeliveryCheckConfig controls the opt-in server-side delivery checker. It only
// describes the P0 contract; the checker runner is introduced in the next phase.
type DeliveryCheckConfig struct {
	Enabled         bool     `yaml:"enabled,omitempty"`
	Agent           string   `yaml:"agent,omitempty"`
	Model           string   `yaml:"model,omitempty"`
	Args            []string `yaml:"args,omitempty"`
	TimeoutSec      int      `yaml:"timeout_sec,omitempty"`
	MaxAutoResumes  *int     `yaml:"max_auto_resumes,omitempty"`
	DailyLimit      int      `yaml:"daily_limit,omitempty"`
	InputMaxBytes   int      `yaml:"input_max_bytes,omitempty"`
	ReportTailBytes int      `yaml:"report_tail_bytes,omitempty"`
	// IncludeAcceptance merges the job's acceptance criteria into the checked
	// requirements (nil = default true).
	IncludeAcceptance *bool `yaml:"include_acceptance,omitempty"`
}

const (
	DefaultDeliveryCheckTimeoutSec      = 120
	DefaultDeliveryCheckMaxAutoResumes  = 2
	DefaultDeliveryCheckDailyLimit      = 50
	DefaultDeliveryCheckInputMaxBytes   = 65536
	DefaultDeliveryCheckReportTailBytes = 16384
)

func (d DeliveryCheckConfig) EffectiveTimeoutSec() int {
	return posOr(d.TimeoutSec, DefaultDeliveryCheckTimeoutSec)
}
func (d DeliveryCheckConfig) EffectiveMaxAutoResumes() int {
	if d.MaxAutoResumes == nil {
		return DefaultDeliveryCheckMaxAutoResumes
	}
	if *d.MaxAutoResumes < 0 {
		return 0
	}
	return *d.MaxAutoResumes
}
func (d DeliveryCheckConfig) EffectiveIncludeAcceptance() bool {
	return d.IncludeAcceptance == nil || *d.IncludeAcceptance
}
func (d DeliveryCheckConfig) EffectiveDailyLimit() int {
	if d.DailyLimit < 0 {
		return 0
	}
	if d.DailyLimit == 0 {
		return DefaultDeliveryCheckDailyLimit
	}
	return d.DailyLimit
}
func (d DeliveryCheckConfig) EffectiveInputMaxBytes() int {
	return posOr(d.InputMaxBytes, DefaultDeliveryCheckInputMaxBytes)
}
func (d DeliveryCheckConfig) EffectiveReportTailBytes() int {
	return posOr(d.ReportTailBytes, DefaultDeliveryCheckReportTailBytes)
}

// deliveryCheckNoToolsArgs is the argv the server forces onto the checker agent so it
// runs without tools (design §4.2 / §4.6), keyed like builtinReadOnlyArgs. It rides
// AFTER the operator's args, so a later duplicate flag cannot be undone by them.
//
//   - claude: `--tools ""` empties the built-in tool set (same preset as the
//     summarizer); `--no-session-persistence` keeps the one-shot out of the history.
//   - codex: it has no switch that removes every tool; the shell tool is turned off
//     through its feature flag and the read-only sandbox (read_only_args, added by the
//     job) bars any write.
//   - suga: `run` has no tool-set switch either; without `--allow-write` the shell and
//     workspace-write tools are off, and `--read-only` disables every mutating tool and
//     wins over the template's `--allow-write`.
var deliveryCheckNoToolsArgs = map[string][]string{
	"claude": {"--tools", "", "--no-session-persistence"},
	"codex":  {"-c", "features.shell_tool=false"},
	"suga":   {"--read-only", "--no-project-rules"},
}

// DeliveryCheckNoToolsArgs returns the forced no-tools argv for an agent name (the
// config key first, then the command base name), or nil when the agent is unknown:
// the checker must then not run, since tool use could not be ruled out.
func DeliveryCheckNoToolsArgs(names ...string) []string {
	for _, n := range names {
		n = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(n)), ".exe")
		if args, ok := deliveryCheckNoToolsArgs[n]; ok {
			return append([]string(nil), args...)
		}
	}
	return nil
}

func (d DeliveryCheckConfig) validate() error {
	if strings.TrimSpace(d.Agent) != d.Agent {
		return fmt.Errorf("server.delivery_check.agent %q has surrounding whitespace", d.Agent)
	}
	if strings.TrimSpace(d.Model) != d.Model {
		return fmt.Errorf("server.delivery_check.model %q has surrounding whitespace", d.Model)
	}
	if d.MaxAutoResumes != nil && *d.MaxAutoResumes < 0 {
		return fmt.Errorf("server.delivery_check.max_auto_resumes must not be negative")
	}
	for name, v := range map[string]int{"server.delivery_check.timeout_sec": d.TimeoutSec, "server.delivery_check.daily_limit": d.DailyLimit, "server.delivery_check.input_max_bytes": d.InputMaxBytes, "server.delivery_check.report_tail_bytes": d.ReportTailBytes} {
		if v < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
	}
	return nil
}

// BackgroundAgentsConfig contains shared session rotation policy.
type BackgroundAgentsConfig struct {
	Session BackgroundAgentSessionConfig `yaml:"session,omitempty"`
}
type BackgroundAgentSessionConfig struct {
	MaxContextRatio float64 `yaml:"max_context_ratio,omitempty"`
	MaxRuns         int     `yaml:"max_runs,omitempty"`
	MaxAge          string  `yaml:"max_age,omitempty"`
}

func (s BackgroundAgentSessionConfig) EffectiveMaxContextRatio() float64 {
	if s.MaxContextRatio <= 0 {
		return 0.7
	}
	return s.MaxContextRatio
}
func (s BackgroundAgentSessionConfig) EffectiveMaxRuns() int { return posOr(s.MaxRuns, 20) }
func (s BackgroundAgentSessionConfig) validate() error {
	if s.MaxContextRatio < 0 || s.MaxContextRatio > 1 {
		return fmt.Errorf("server.background_agents.session.max_context_ratio must be between 0 and 1")
	}
	if s.MaxRuns < 0 {
		return fmt.Errorf("server.background_agents.session.max_runs must not be negative")
	}
	if age := strings.TrimSpace(s.MaxAge); age != "" {
		dur, err := time.ParseDuration(age)
		if err != nil || dur <= 0 {
			return fmt.Errorf("server.background_agents.session.max_age %q must be a positive duration (empty = disabled)", s.MaxAge)
		}
	}
	return nil
}

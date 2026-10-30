package config

import (
	"fmt"
	"strings"
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
	if strings.TrimSpace(s.MaxAge) != "" { /* parsed by the runner phase; preserve empty as disabled */
	}
	return nil
}

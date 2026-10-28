package config

import (
	"fmt"
	"strings"
	"time"
)

// MemoryConfig is the top-level memory configuration. Synthesis is intentionally
// opt-in because it may submit model work and handles cross-source project material.
type MemoryConfig struct {
	Synthesis SynthesisConfig `yaml:"synthesis,omitempty"`
}

// SynthesisConfig controls P1 material collection and the later synthesis runner.
type SynthesisConfig struct {
	Enabled              bool     `yaml:"enabled,omitempty"`
	Projects             []string `yaml:"projects,omitempty"`
	Time                 string   `yaml:"time,omitempty"`
	Lookback             string   `yaml:"lookback,omitempty"`
	Agent                string   `yaml:"agent,omitempty"`
	Runner               string   `yaml:"runner,omitempty"`
	ModelArgs            []string `yaml:"model_args,omitempty"`
	MaxInputBytes        int      `yaml:"max_input_bytes,omitempty"`
	MaxOutputBytes       int      `yaml:"max_output_bytes,omitempty"`
	Timeout              string   `yaml:"timeout,omitempty"`
	DailyLimit           int      `yaml:"daily_limit,omitempty"`
	RetryAfter           string   `yaml:"retry_after,omitempty"`
	RetainDays           int      `yaml:"retain_days,omitempty"`
	KeepMaterialSnapshot bool     `yaml:"keep_material_snapshot,omitempty"`
}

const (
	DefaultSynthesisLookback       = 24 * time.Hour
	DefaultSynthesisMaxInputBytes  = 24000
	DefaultSynthesisMaxOutputBytes = 12000
	DefaultSynthesisTimeout        = 5 * time.Minute
	DefaultSynthesisDailyLimit     = 1
	DefaultSynthesisRetryAfter     = 30 * time.Minute
	DefaultSynthesisRetainDays     = 90
)

func (s SynthesisConfig) LookbackDuration() time.Duration {
	return parsePositiveOr(s.Lookback, DefaultSynthesisLookback)
}
func (s SynthesisConfig) TimeoutDuration() time.Duration {
	return parsePositiveOr(s.Timeout, DefaultSynthesisTimeout)
}
func (s SynthesisConfig) RetryAfterDuration() time.Duration {
	return parsePositiveOr(s.RetryAfter, DefaultSynthesisRetryAfter)
}
func (s SynthesisConfig) InputLimit() int {
	if s.MaxInputBytes > 0 {
		return s.MaxInputBytes
	}
	return DefaultSynthesisMaxInputBytes
}
func (s SynthesisConfig) OutputLimit() int {
	if s.MaxOutputBytes > 0 {
		return s.MaxOutputBytes
	}
	return DefaultSynthesisMaxOutputBytes
}
func (s SynthesisConfig) EffectiveAgent(work WorkConfig) string {
	if strings.TrimSpace(s.Agent) != "" {
		return s.Agent
	}
	return work.SummarizerAgentName()
}
func (s SynthesisConfig) EffectiveRunner() string {
	if strings.TrimSpace(s.Runner) == "" {
		return BuiltinLocalRunner
	}
	return NormalizeRunnerName(s.Runner)
}

func parsePositiveOr(value string, fallback time.Duration) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(value)); err == nil && d > 0 {
		return d
	}
	return fallback
}

func (m MemoryConfig) validate(cfg *Config) error {
	s := m.Synthesis
	if strings.TrimSpace(s.Runner) != "" && !IsLocalRunnerName(s.Runner) {
		return fmt.Errorf("memory.synthesis.runner must be %q", BuiltinLocalRunner)
	}
	if strings.TrimSpace(s.Time) != "" {
		var h, min int
		if _, err := fmt.Sscanf(s.Time, "%d:%d", &h, &min); err != nil || len(s.Time) != 5 || s.Time[2] != ':' || h < 0 || h > 23 || min < 0 || min > 59 {
			return fmt.Errorf("memory.synthesis.time %q must be HH:MM", s.Time)
		}
	}
	for i, p := range s.Projects {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("memory.synthesis.projects[%d] must not be empty", i)
		}
		if cfg != nil {
			if _, ok := cfg.Projects[p]; !ok {
				return fmt.Errorf("memory.synthesis.projects[%d] %q does not exist", i, p)
			}
		}
	}
	for name, value := range map[string]int{"max_input_bytes": s.MaxInputBytes, "max_output_bytes": s.MaxOutputBytes, "retain_days": s.RetainDays} {
		if value < 0 {
			return fmt.Errorf("memory.synthesis.%s must be >= 0", name)
		}
	}
	if s.MaxInputBytes > 1<<20 {
		return fmt.Errorf("memory.synthesis.max_input_bytes must be <= 1048576")
	}
	if s.MaxOutputBytes > 1<<20 {
		return fmt.Errorf("memory.synthesis.max_output_bytes must be <= 1048576")
	}
	if s.RetainDays > 3650 {
		return fmt.Errorf("memory.synthesis.retain_days must be <= 3650")
	}
	if s.DailyLimit < -1 {
		return fmt.Errorf("memory.synthesis.daily_limit must be -1 or >= 0")
	}
	if s.DailyLimit > 100 {
		return fmt.Errorf("memory.synthesis.daily_limit must be <= 100")
	}
	for _, item := range []struct {
		name  string
		value string
		max   time.Duration
	}{
		{"lookback", s.Lookback, 30 * 24 * time.Hour}, {"timeout", s.Timeout, time.Hour}, {"retry_after", s.RetryAfter, 24 * time.Hour},
	} {
		if strings.TrimSpace(item.value) == "" {
			continue
		}
		d, err := time.ParseDuration(item.value)
		if err != nil || d <= 0 || d > item.max {
			return fmt.Errorf("memory.synthesis.%s must be a positive duration <= %s", item.name, item.max)
		}
	}
	if s.Enabled {
		agent := s.EffectiveAgent(cfg.Work)
		if strings.TrimSpace(agent) == "" {
			return fmt.Errorf("memory.synthesis.enabled requires an agent")
		}
		// Configured agents are authoritative; the default summarizer may also be supplied
		// by the detect-gated built-in templates after config decoding.
		if _, ok := cfg.Agents[agent]; !ok && agent != DefaultWorkSummarizerAgent {
			return fmt.Errorf("memory.synthesis.agent %q is not configured", agent)
		}
	}
	for _, arg := range s.ModelArgs {
		lower := strings.ToLower(strings.TrimSpace(arg))
		if lower == "--tools" || lower == "--shell" || lower == "--mcp" || strings.HasPrefix(lower, "--tools=") || strings.HasPrefix(lower, "--shell=") || strings.HasPrefix(lower, "--mcp=") || strings.Contains(lower, "mcp=") || strings.Contains(lower, "shell=") {
			return fmt.Errorf("memory.synthesis.model_args must not enable tools, shell, or MCP")
		}
	}
	return nil
}

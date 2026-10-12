package config

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Decision hooks (AUTO-04b, docs/design/2026-10-12-decision-hooks-design.md): a
// command the server runs SYNCHRONOUSLY at a decision point, whose JSON answer can
// refuse the action or make it more conservative. Unlike event hooks they sit on the
// request path, so they are short-lived and fail closed by default.

// Decision points a hook can attach to.
const (
	// DecisionPointSubmit runs before a job is admitted.
	DecisionPointSubmit = "job.submit"
	// DecisionPointDeliver runs before a worktree job's branch is handed over.
	DecisionPointDeliver = "job.deliver"
)

// Decision hook outcomes when the hook itself fails (on_error).
const (
	DecisionOnErrorDeny  = "deny"
	DecisionOnErrorAllow = "allow"
)

const (
	// DefaultDecisionHookTimeoutSec bounds one run when timeout_sec is unset: the
	// caller is waiting on the other side of the request.
	DefaultDecisionHookTimeoutSec = 5
	// MaxDecisionHookTimeoutSec is the ceiling a hook may ask for.
	MaxDecisionHookTimeoutSec = 30
)

// decisionPoints are the points implemented so far.
var decisionPoints = []string{DecisionPointSubmit, DecisionPointDeliver}

// DecisionHookConfig is one server.decision_hooks entry. Like event hooks it runs a
// command on the server host, so it is FILE-ONLY: the config write API refuses
// `server.decision_hooks` and read views never echo Env values.
type DecisionHookConfig struct {
	// Name identifies the hook in decisions, events and logs. Required, unique.
	Name string `yaml:"name"`
	// Points lists the decision points the hook answers. Required.
	Points []string `yaml:"points"`
	// Command is the argv to run (no shell). Required.
	Command []string `yaml:"command"`
	// Projects filters the project keys; empty = all projects.
	Projects []string `yaml:"projects,omitempty"`
	// Dir is the working directory (same rules as an event hook's dir).
	Dir string `yaml:"dir,omitempty"`
	// Env is layered over the filtered serve environment; values never appear in an
	// API view.
	Env map[string]string `yaml:"env,omitempty"`
	// TimeoutSec bounds one run; 0 = DefaultDecisionHookTimeoutSec, max MaxDecisionHookTimeoutSec.
	TimeoutSec int `yaml:"timeout_sec,omitempty"`
	// OnError decides what a failed run (cannot start, timeout, non-zero exit,
	// unreadable answer, a change it may not make) means: deny (default) or allow.
	OnError string `yaml:"on_error,omitempty"`
	// IncludeInternal also asks the hook about gofer's own internal jobs (delivery,
	// tracker sync, summarizer, steward…), which are skipped by default so that one
	// broken rule cannot stop gofer's own machinery.
	IncludeInternal bool `yaml:"include_internal,omitempty"`
	// Enabled pauses the hook without deleting it (nil = on).
	Enabled *bool `yaml:"enabled,omitempty"`
}

// IsEnabled reports whether the hook is active (nil/absent = on).
func (h DecisionHookConfig) IsEnabled() bool { return h.Enabled == nil || *h.Enabled }

// EffectiveTimeout is the per-run timeout after defaulting.
func (h DecisionHookConfig) EffectiveTimeout() time.Duration {
	sec := h.TimeoutSec
	if sec <= 0 {
		sec = DefaultDecisionHookTimeoutSec
	}
	if sec > MaxDecisionHookTimeoutSec {
		sec = MaxDecisionHookTimeoutSec
	}
	return time.Duration(sec) * time.Second
}

// EffectiveOnError is on_error after defaulting (deny).
func (h DecisionHookConfig) EffectiveOnError() string {
	if strings.TrimSpace(h.OnError) == "" {
		return DecisionOnErrorDeny
	}
	return h.OnError
}

// EffectiveDir resolves the working directory like an event hook's.
func (h DecisionHookConfig) EffectiveDir() string { return hookWorkDir(h.Dir) }

// EnvKeys returns the env variable names, sorted.
func (h DecisionHookConfig) EnvKeys() []string { return sortedKeys(h.Env) }

// DecisionHooksFor returns, in config order, the enabled hooks that answer point for
// projectKey. internal reports whether the action belongs to one of gofer's own
// internal jobs, which only include_internal hooks are asked about.
func (c *Config) DecisionHooksFor(point, projectKey string, internal bool) []DecisionHookConfig {
	if c == nil {
		return nil
	}
	var out []DecisionHookConfig
	for _, h := range c.Server.DecisionHooks {
		if !h.IsEnabled() || !slices.Contains(h.Points, point) || (internal && !h.IncludeInternal) {
			continue
		}
		if len(h.Projects) > 0 && !slices.Contains(h.Projects, projectKey) {
			continue
		}
		out = append(out, h)
	}
	return out
}

// validateDecisionHooks is the load-time check for server.decision_hooks.
func validateDecisionHooks(hooks []DecisionHookConfig) error {
	seen := make(map[string]bool, len(hooks))
	for i, h := range hooks {
		at := fmt.Sprintf("server.decision_hooks[%d]", i)
		if !eventHookNamePattern.MatchString(h.Name) {
			return fmt.Errorf("%s.name %q is invalid (letters, digits, '.', '_', '-'; must start with a letter or digit; max 64)", at, h.Name)
		}
		if seen[h.Name] {
			return fmt.Errorf("%s.name %q is duplicated", at, h.Name)
		}
		seen[h.Name] = true
		if len(h.Command) == 0 || strings.TrimSpace(h.Command[0]) == "" {
			return fmt.Errorf("%s (%s).command is required (argv form, e.g. [\"python3\", \"policy.py\"])", at, h.Name)
		}
		if len(h.Points) == 0 {
			return fmt.Errorf("%s (%s).points is required (%s)", at, h.Name, strings.Join(decisionPoints, ", "))
		}
		for _, p := range h.Points {
			if !slices.Contains(decisionPoints, p) {
				return fmt.Errorf("%s (%s).points has an unknown point %q (supported: %s)", at, h.Name, p, strings.Join(decisionPoints, ", "))
			}
		}
		if h.TimeoutSec < 0 || h.TimeoutSec > MaxDecisionHookTimeoutSec {
			return fmt.Errorf("%s (%s).timeout_sec must be between 0 and %d", at, h.Name, MaxDecisionHookTimeoutSec)
		}
		switch h.OnError {
		case "", DecisionOnErrorDeny, DecisionOnErrorAllow:
		default:
			return fmt.Errorf("%s (%s).on_error %q must be deny or allow", at, h.Name, h.OnError)
		}
		for k := range h.Env {
			if k == "" || strings.ContainsAny(k, "=\x00") {
				return fmt.Errorf("%s (%s).env has an invalid key %q", at, h.Name, k)
			}
		}
	}
	return nil
}

// cloneDecisionHooks deep-copies the hook list for Config.Clone.
func cloneDecisionHooks(in []DecisionHookConfig) []DecisionHookConfig {
	if in == nil {
		return nil
	}
	out := make([]DecisionHookConfig, len(in))
	for i, h := range in {
		h.Points = slices.Clone(h.Points)
		h.Command = slices.Clone(h.Command)
		h.Projects = slices.Clone(h.Projects)
		if h.Env != nil {
			env := make(map[string]string, len(h.Env))
			for k, v := range h.Env {
				env[k] = v
			}
			h.Env = env
		}
		h.Enabled = clonePtr(h.Enabled)
		out[i] = h
	}
	return out
}

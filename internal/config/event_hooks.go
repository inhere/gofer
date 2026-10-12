package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Event hook defaults and limits (AUTO-04, design docs/design/2026-10-11-event-hooks-design.md).
const (
	// DefaultEventHookTimeoutSec bounds one hook run when timeout_sec is unset.
	DefaultEventHookTimeoutSec = 30
	// MaxEventHookTimeoutSec is the ceiling a hook may ask for: the delivery sweep
	// waits for its hooks, so an unbounded one would stall the whole queue.
	MaxEventHookTimeoutSec = 300
	// DefaultEventHookMaxAttempts is the attempt cap (first run included) when
	// max_attempts is unset.
	DefaultEventHookMaxAttempts = 3
	// EventHookTargetPrefix marks an event_deliveries row aimed at a hook rather than
	// a webhook URL: the row's target is "hook:<name>".
	EventHookTargetPrefix = "hook:"
	// EventHookWildcard in a hook's events list subscribes it to every event type.
	EventHookWildcard = "*"
)

// eventHookNamePattern keeps a hook name usable as a delivery target, a log field and
// an env value without quoting.
var eventHookNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// EventHookConfig is one server-side event hook (AUTO-04): when a matching event
// happens, gofer runs Command on the server host with the generic webhook JSON body
// on stdin. It is a read-only side channel — the command's output never flows back
// into gofer — and, because it executes a command on the server, it can only be
// changed in the config file: the config write API refuses `server.event_hooks`
// (see fieldPolicies) and read views never echo Env values.
type EventHookConfig struct {
	// Name identifies the hook; deliveries target "hook:<name>". Required, unique.
	Name string `yaml:"name"`
	// Command is the argv to run (no shell). Required.
	Command []string `yaml:"command"`
	// Events filters the event types; empty = the webhook default trigger set,
	// ["*"] = every event.
	Events []string `yaml:"events,omitempty"`
	// Projects filters the project keys; empty = all projects (an event without a
	// project only matches an empty filter).
	Projects []string `yaml:"projects,omitempty"`
	// Dir is the working directory; empty = the gofer config directory. A relative
	// dir is resolved against the config directory.
	Dir string `yaml:"dir,omitempty"`
	// Env is layered over the filtered serve environment. Values may be secrets, so
	// no API view ever shows them (keys only).
	Env map[string]string `yaml:"env,omitempty"`
	// TimeoutSec bounds one run; 0 = DefaultEventHookTimeoutSec, max MaxEventHookTimeoutSec.
	TimeoutSec int `yaml:"timeout_sec,omitempty"`
	// MaxAttempts caps the runs per event (first included); 0 = DefaultEventHookMaxAttempts.
	MaxAttempts int `yaml:"max_attempts,omitempty"`
	// Enabled pauses the hook without deleting it (nil = on).
	Enabled *bool `yaml:"enabled,omitempty"`
}

// IsEnabled reports whether the hook is active (nil/absent = on).
func (h EventHookConfig) IsEnabled() bool { return h.Enabled == nil || *h.Enabled }

// Target is the delivery target a row for this hook carries.
func (h EventHookConfig) Target() string { return EventHookTargetPrefix + h.Name }

// EffectiveTimeout is the per-run timeout after defaulting.
func (h EventHookConfig) EffectiveTimeout() time.Duration {
	sec := h.TimeoutSec
	if sec <= 0 {
		sec = DefaultEventHookTimeoutSec
	}
	if sec > MaxEventHookTimeoutSec {
		sec = MaxEventHookTimeoutSec
	}
	return time.Duration(sec) * time.Second
}

// EffectiveMaxAttempts is the attempt cap after defaulting.
func (h EventHookConfig) EffectiveMaxAttempts() int {
	if h.MaxAttempts <= 0 {
		return DefaultEventHookMaxAttempts
	}
	return h.MaxAttempts
}

// EffectiveDir resolves the hook's working directory: its own dir (relative = under
// the gofer config directory), else the config directory itself, else "" (the
// serve process's cwd) when that directory does not exist.
func (h EventHookConfig) EffectiveDir() string { return hookWorkDir(h.Dir) }

// hookWorkDir resolves a hook's dir setting (event and decision hooks alike).
func hookWorkDir(dir string) string {
	base, _ := ConfigDir()
	dir = strings.TrimSpace(dir)
	if dir == "" {
		if fi, err := os.Stat(base); err == nil && fi.IsDir() {
			return base
		}
		return ""
	}
	if !filepath.IsAbs(dir) && base != "" {
		dir = filepath.Join(base, dir)
	}
	return dir
}

// EnvKeys returns the hook's env variable names, sorted (the only part of Env a
// read view may show).
func (h EventHookConfig) EnvKeys() []string { return sortedKeys(h.Env) }

// sortedKeys returns a hook env's variable names, sorted.
func sortedKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// EventHookName extracts the hook name from a delivery target; ok=false for a
// webhook target.
func EventHookName(target string) (string, bool) {
	name, ok := strings.CutPrefix(target, EventHookTargetPrefix)
	return name, ok && name != ""
}

// EventHook returns the configured hook with the given name.
func (c *Config) EventHook(name string) (EventHookConfig, bool) {
	if c == nil {
		return EventHookConfig{}, false
	}
	for _, h := range c.Server.EventHooks {
		if h.Name == name {
			return h, true
		}
	}
	return EventHookConfig{}, false
}

// validateEventHooks is the load-time check for server.event_hooks: a hook that
// cannot run (no command), cannot be addressed (bad / duplicate name) or would
// stall the delivery sweep (timeout out of range) fails the load, so it fails
// `gofer config validate` and a reload too.
func validateEventHooks(hooks []EventHookConfig) error {
	seen := make(map[string]bool, len(hooks))
	for i, h := range hooks {
		at := fmt.Sprintf("server.event_hooks[%d]", i)
		if !eventHookNamePattern.MatchString(h.Name) {
			return fmt.Errorf("%s.name %q is invalid (letters, digits, '.', '_', '-'; must start with a letter or digit; max 64)", at, h.Name)
		}
		if seen[h.Name] {
			return fmt.Errorf("%s.name %q is duplicated", at, h.Name)
		}
		seen[h.Name] = true
		if len(h.Command) == 0 || strings.TrimSpace(h.Command[0]) == "" {
			return fmt.Errorf("%s (%s).command is required (argv form, e.g. [\"python3\", \"hook.py\"])", at, h.Name)
		}
		if h.TimeoutSec < 0 || h.TimeoutSec > MaxEventHookTimeoutSec {
			return fmt.Errorf("%s (%s).timeout_sec must be between 0 and %d", at, h.Name, MaxEventHookTimeoutSec)
		}
		if h.MaxAttempts < 0 {
			return fmt.Errorf("%s (%s).max_attempts must be >= 0", at, h.Name)
		}
		for _, e := range h.Events {
			if strings.TrimSpace(e) == "" {
				return fmt.Errorf("%s (%s).events has an empty entry", at, h.Name)
			}
		}
		for k := range h.Env {
			if k == "" || strings.ContainsAny(k, "=\x00") {
				return fmt.Errorf("%s (%s).env has an invalid key %q", at, h.Name, k)
			}
		}
	}
	return nil
}

// cloneEventHooks deep-copies the hook list for Config.Clone.
func cloneEventHooks(in []EventHookConfig) []EventHookConfig {
	if in == nil {
		return nil
	}
	out := make([]EventHookConfig, len(in))
	for i, h := range in {
		h.Command = slices.Clone(h.Command)
		h.Events = slices.Clone(h.Events)
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

package config

import (
	"path/filepath"
	"testing"
	"time"

	yaml "github.com/goccy/go-yaml"
	"github.com/gookit/goutil/x/assert"
)

// TestEventHooksDecodeAndDefaults guards the yaml names of server.event_hooks and the
// defaults a hook gets when it leaves timeout / max_attempts / enabled unset.
func TestEventHooksDecodeAndDefaults(t *testing.T) {
	const src = `
server:
  event_hooks:
    - name: audit-log
      command: ["python3", "hook.py"]
      events: [job.terminal, plan.completed]
      projects: [proj-a]
      dir: /ops
      env: {AUDIT_ENDPOINT: "http://127.0.0.1:9000", B_KEY: x}
      timeout_sec: 45
      max_attempts: 5
      enabled: false
    - name: all
      command: [sync]
      events: ["*"]
`
	cfg := &Config{}
	assert.Require(t, assert.NoErr(t, yaml.Unmarshal([]byte(src), cfg)))
	ApplyDefaults(cfg)
	assert.Require(t, assert.NoErr(t, validate(cfg)))
	assert.Require(t, assert.Len(t, cfg.Server.EventHooks, 2))

	h := cfg.Server.EventHooks[0]
	assert.Eq(t, "audit-log", h.Name)
	assert.Eq(t, []string{"python3", "hook.py"}, h.Command)
	assert.Eq(t, []string{"job.terminal", "plan.completed"}, h.Events)
	assert.Eq(t, []string{"proj-a"}, h.Projects)
	assert.Eq(t, "/ops", h.Dir)
	assert.Eq(t, 45*time.Second, h.EffectiveTimeout())
	assert.Eq(t, 5, h.EffectiveMaxAttempts())
	assert.False(t, h.IsEnabled())
	assert.Eq(t, []string{"AUDIT_ENDPOINT", "B_KEY"}, h.EnvKeys())
	assert.Eq(t, "hook:audit-log", h.Target())

	d := cfg.Server.EventHooks[1]
	assert.Eq(t, []string{"*"}, d.Events)
	assert.Eq(t, time.Duration(DefaultEventHookTimeoutSec)*time.Second, d.EffectiveTimeout())
	assert.Eq(t, DefaultEventHookMaxAttempts, d.EffectiveMaxAttempts())
	assert.True(t, d.IsEnabled())

	got, ok := cfg.EventHook("all")
	assert.True(t, ok)
	assert.Eq(t, "all", got.Name)
	_, ok = cfg.EventHook("missing")
	assert.False(t, ok)

	name, ok := EventHookName("hook:audit-log")
	assert.True(t, ok)
	assert.Eq(t, "audit-log", name)
	_, ok = EventHookName("https://hooks.example.com/x")
	assert.False(t, ok)
}

// TestEventHooksValidate covers every load-time refusal: a hook that cannot be
// addressed, cannot run, or would stall the delivery sweep fails the load.
func TestEventHooksValidate(t *testing.T) {
	ok := EventHookConfig{Name: "a", Command: []string{"x"}}
	cases := []struct {
		name  string
		hooks []EventHookConfig
		want  string
	}{
		{"empty name", []EventHookConfig{{Command: []string{"x"}}}, "name"},
		{"bad name", []EventHookConfig{{Name: "has space", Command: []string{"x"}}}, "is invalid"},
		{"leading dash", []EventHookConfig{{Name: "-a", Command: []string{"x"}}}, "is invalid"},
		{"duplicate", []EventHookConfig{ok, ok}, "duplicated"},
		{"no command", []EventHookConfig{{Name: "a"}}, "command is required"},
		{"blank command", []EventHookConfig{{Name: "a", Command: []string{" "}}}, "command is required"},
		{"negative timeout", []EventHookConfig{{Name: "a", Command: []string{"x"}, TimeoutSec: -1}}, "timeout_sec"},
		{"timeout too long", []EventHookConfig{{Name: "a", Command: []string{"x"}, TimeoutSec: MaxEventHookTimeoutSec + 1}}, "timeout_sec"},
		{"negative attempts", []EventHookConfig{{Name: "a", Command: []string{"x"}, MaxAttempts: -1}}, "max_attempts"},
		{"empty event", []EventHookConfig{{Name: "a", Command: []string{"x"}, Events: []string{""}}}, "events"},
		{"bad env key", []EventHookConfig{{Name: "a", Command: []string{"x"}, Env: map[string]string{"A=B": "v"}}}, "env"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Server: ServerConfig{EventHooks: tc.hooks}}
			ApplyDefaults(cfg)
			err := validate(cfg)
			assert.Require(t, assert.Err(t, err))
			assert.StrContains(t, err.Error(), tc.want)
			assert.StrContains(t, err.Error(), "server.event_hooks[")
		})
	}

	t.Run("max timeout accepted", func(t *testing.T) {
		cfg := &Config{Server: ServerConfig{EventHooks: []EventHookConfig{{Name: "a.b_c-1", Command: []string{"x"}, TimeoutSec: MaxEventHookTimeoutSec}}}}
		ApplyDefaults(cfg)
		assert.NoErr(t, validate(cfg))
	})
}

// TestEventHooksPolicyFileOnly pins the user decision: server.event_hooks is NOT
// editable through the config write API (it would be remote command execution).
func TestEventHooksPolicyFileOnly(t *testing.T) {
	fp, ok := FieldPolicyFor("server.event_hooks")
	assert.True(t, ok)
	assert.False(t, fp.Editable)
}

// TestEventHooksCloneIsDeep proves a clone's hooks share nothing with the source.
func TestEventHooksCloneIsDeep(t *testing.T) {
	on := true
	src := &Config{Server: ServerConfig{EventHooks: []EventHookConfig{{
		Name: "a", Command: []string{"x", "y"}, Events: []string{"e"}, Projects: []string{"p"},
		Env: map[string]string{"K": "v"}, Enabled: &on,
	}}}}
	cl := src.Clone()
	h := &cl.Server.EventHooks[0]
	h.Command[0] = "changed"
	h.Events[0] = "changed"
	h.Projects[0] = "changed"
	h.Env["K"] = "changed"
	*h.Enabled = false
	s := src.Server.EventHooks[0]
	assert.Eq(t, "x", s.Command[0])
	assert.Eq(t, "e", s.Events[0])
	assert.Eq(t, "p", s.Projects[0])
	assert.Eq(t, "v", s.Env["K"])
	assert.True(t, s.IsEnabled())
}

// TestEventHookEffectiveDir resolves the working directory: explicit absolute dir
// as-is, a relative one under the config directory, and the config directory itself
// when unset.
func TestEventHookEffectiveDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv(EnvConfigDir, base)
	abs := filepath.Join(base, "abs")
	assert.Eq(t, abs, EventHookConfig{Dir: abs}.EffectiveDir())
	assert.Eq(t, filepath.Join(base, "ops"), EventHookConfig{Dir: "ops"}.EffectiveDir())
	assert.Eq(t, base, EventHookConfig{}.EffectiveDir())

	t.Setenv(EnvConfigDir, filepath.Join(base, "missing"))
	assert.Eq(t, "", EventHookConfig{}.EffectiveDir())
}

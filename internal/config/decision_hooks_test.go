package config

import (
	"strings"
	"testing"
	"time"

	yaml "github.com/goccy/go-yaml"
	"github.com/gookit/goutil/x/assert"
)

func TestDecisionHooksDecodeDefaultsAndMatch(t *testing.T) {
	const src = `
server:
  decision_hooks:
    - name: no-push
      points: [job.submit]
      command: ["python3", "policy.py"]
      projects: [proj-a]
      env: {POLICY: x}
    - name: pr-check
      points: [job.deliver, job.submit]
      command: [check]
      timeout_sec: 20
      on_error: allow
      include_internal: true
    - name: paused
      points: [job.submit]
      command: [x]
      enabled: false
`
	cfg := &Config{}
	assert.Require(t, assert.NoErr(t, yaml.Unmarshal([]byte(src), cfg)))
	ApplyDefaults(cfg)
	assert.Require(t, assert.NoErr(t, validate(cfg)))
	assert.Require(t, assert.Len(t, cfg.Server.DecisionHooks, 3))

	h := cfg.Server.DecisionHooks[0]
	assert.Eq(t, 5*time.Second, h.EffectiveTimeout())
	assert.Eq(t, DecisionOnErrorDeny, h.EffectiveOnError())
	assert.Eq(t, []string{"POLICY"}, h.EnvKeys())
	assert.Eq(t, 20*time.Second, cfg.Server.DecisionHooks[1].EffectiveTimeout())
	assert.Eq(t, DecisionOnErrorAllow, cfg.Server.DecisionHooks[1].EffectiveOnError())

	names := func(hs []DecisionHookConfig) []string {
		var out []string
		for _, h := range hs {
			out = append(out, h.Name)
		}
		return out
	}
	// Config order, project filter, disabled hooks skipped.
	assert.Eq(t, []string{"no-push", "pr-check"}, names(cfg.DecisionHooksFor(DecisionPointSubmit, "proj-a", false)))
	assert.Eq(t, []string{"pr-check"}, names(cfg.DecisionHooksFor(DecisionPointSubmit, "proj-b", false)))
	assert.Eq(t, []string{"pr-check"}, names(cfg.DecisionHooksFor(DecisionPointDeliver, "proj-a", false)))
	// Internal jobs only reach include_internal hooks.
	assert.Eq(t, []string{"pr-check"}, names(cfg.DecisionHooksFor(DecisionPointSubmit, "proj-a", true)))

	// Clone is deep.
	c := cfg.Clone()
	c.Server.DecisionHooks[0].Command[0] = "changed"
	c.Server.DecisionHooks[0].Env["POLICY"] = "changed"
	assert.Eq(t, "python3", cfg.Server.DecisionHooks[0].Command[0])
	assert.Eq(t, "x", cfg.Server.DecisionHooks[0].Env["POLICY"])
}

func TestDecisionHooksValidation(t *testing.T) {
	for _, tc := range []struct {
		hook DecisionHookConfig
		want string
	}{
		{DecisionHookConfig{Name: "bad name", Points: []string{DecisionPointSubmit}, Command: []string{"x"}}, "name"},
		{DecisionHookConfig{Name: "a", Points: []string{DecisionPointSubmit}}, "command is required"},
		{DecisionHookConfig{Name: "a", Command: []string{"x"}}, "points is required"},
		{DecisionHookConfig{Name: "a", Points: []string{"job.interaction"}, Command: []string{"x"}}, "unknown point"},
		{DecisionHookConfig{Name: "a", Points: []string{DecisionPointSubmit}, Command: []string{"x"}, TimeoutSec: 31}, "timeout_sec"},
		{DecisionHookConfig{Name: "a", Points: []string{DecisionPointSubmit}, Command: []string{"x"}, OnError: "ask"}, "on_error"},
		{DecisionHookConfig{Name: "a", Points: []string{DecisionPointSubmit}, Command: []string{"x"}, Env: map[string]string{"A=B": "1"}}, "env"},
	} {
		err := validateDecisionHooks([]DecisionHookConfig{tc.hook})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %q", tc.hook, err, tc.want)
		}
	}
	dup := DecisionHookConfig{Name: "a", Points: []string{DecisionPointSubmit}, Command: []string{"x"}}
	if err := validateDecisionHooks([]DecisionHookConfig{dup, dup}); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Errorf("duplicate: %v", err)
	}
}

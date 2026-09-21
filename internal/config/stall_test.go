package config

import "testing"

// TestStallTimeoutResolutionOrder pins the AUTO-05 stall-window resolution: a request
// override beats an agent's own value, which beats the server default; an exec job is
// never watched unless someone asked for it; and an interactive job is never watched
// at all. The tri-state (nil = inherit, 0 = off) is the reason every layer is a
// pointer: "turn it off for this agent" must not read as "unset".
func TestStallTimeoutResolutionOrder(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{StallTimeoutSec: intPtr(900)},
		Agents: map[string]AgentConfig{
			"codex":     {Type: "cli-agent"},
			"codexFast": {Type: "cli-agent", StallTimeoutSec: intPtr(30)},
			"codexOff":  {Type: "cli-agent", StallTimeoutSec: intPtr(0)},
			"deploy":    {Type: "exec"},
		},
	}
	job5 := intPtr(5)

	cases := []struct {
		name        string
		agentKey    string
		agentType   string
		requested   *int
		interactive bool
		want        int
	}{
		{"job beats agent and server", "codexFast", "cli-agent", job5, false, 5},
		{"agent beats server", "codexFast", "cli-agent", nil, false, 30},
		{"server default", "codex", "cli-agent", nil, false, 900},
		{"agent explicitly off", "codexOff", "cli-agent", nil, false, 0},
		{"exec default is off", "deploy", "exec", nil, false, 0},
		{"an explicit window applies to exec too", "deploy", "exec", job5, false, 5},
		{"interactive is never watched", "codex", "cli-agent", nil, true, 0},
		{"interactive ignores an explicit window", "codex", "cli-agent", job5, true, 0},
	}
	for _, tc := range cases {
		if got := cfg.EffectiveStallTimeoutSec(tc.agentKey, tc.agentType, tc.requested, tc.interactive); got != tc.want {
			t.Errorf("%s: stall = %d, want %d", tc.name, got, tc.want)
		}
	}

	// An unset server value resolves to the documented default (900s) rather than
	// "no watchdog": the capability is on unless configured off.
	plain := &Config{Agents: map[string]AgentConfig{"codex": {Type: "cli-agent"}}}
	if got := plain.EffectiveStallTimeoutSec("codex", "cli-agent", nil, false); got != DefaultStallTimeoutSec {
		t.Errorf("unset server.stall_timeout_sec = %d, want %d", got, DefaultStallTimeoutSec)
	}
	if DefaultStallTimeoutSec != 900 {
		t.Errorf("DefaultStallTimeoutSec = %d, want 900", DefaultStallTimeoutSec)
	}
	// A nil config (no config at all) must not panic a resolver the job path calls.
	var nilCfg *Config
	if got := nilCfg.EffectiveStallTimeoutSec("codex", "cli-agent", nil, false); got != DefaultStallTimeoutSec {
		t.Errorf("nil config stall = %d, want %d", got, DefaultStallTimeoutSec)
	}
}

func intPtr(n int) *int { return &n }

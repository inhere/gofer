package bgagent

import (
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func rec(runs int, created int64, used, limit int64) *jobstore.BackgroundSession {
	return &jobstore.BackgroundSession{Kind: KindSummarizer, SessionID: "s1", Runs: runs, CreatedAt: created,
		ContextUsed: used, ContextLimit: limit, LastJobID: "job-1"}
}

func TestDecideRules(t *testing.T) {
	pol := Policy{MaxRuns: 5, MaxContextRatio: 0.7}
	resumable := Caps{CanResume: true}
	clearable := Caps{CanResume: true, CanClearContext: true}
	const now = 10_000
	cases := []struct {
		name       string
		in         DecideInput
		mode       Mode
		reason     string
		rotated    bool
		sessionKey string
	}{
		{"no session", DecideInput{Policy: pol, Caps: resumable, Now: now}, ModeFresh, ReasonNone, false, ""},
		{"manual reset wins", DecideInput{Policy: pol, Caps: resumable, Now: now, Reset: true}, ModeFresh, ReasonManualReset, false, ""},
		{"agent or model changed", DecideInput{Policy: pol, Caps: resumable, Now: now, OtherBinding: true}, ModeFresh, ReasonKeyChanged, false, ""},
		{"agent cannot resume", DecideInput{Policy: pol, Caps: Caps{}, Now: now, Active: rec(1, now, 0, 0)}, ModeFresh, ReasonNotResumable, false, ""},
		{"healthy session resumes", DecideInput{Policy: pol, Caps: resumable, Now: now, Active: rec(1, now, 100, 1000)}, ModeResume, ReasonNone, false, "s1"},
		// context ratio is the main rule and is inclusive
		{"ratio just below", DecideInput{Policy: pol, Caps: resumable, Now: now, Active: rec(1, now, 699, 1000)}, ModeResume, ReasonNone, false, "s1"},
		{"ratio at limit", DecideInput{Policy: pol, Caps: resumable, Now: now, Active: rec(1, now, 700, 1000)}, ModeFresh, ReasonContextRatio, true, ""},
		{"ratio over, clear supported", DecideInput{Policy: pol, Caps: clearable, Now: now, Active: rec(1, now, 900, 1000)}, ModeClearContext, ReasonContextRatio, true, "s1"},
		// unknown ratio is not 0 and not a trigger: only the run count applies
		{"window unknown", DecideInput{Policy: pol, Caps: resumable, Now: now, Active: rec(1, now, 900, 0)}, ModeResume, ReasonNone, false, "s1"},
		{"fill unknown", DecideInput{Policy: pol, Caps: resumable, Now: now, Active: rec(1, now, 0, 1000)}, ModeResume, ReasonNone, false, "s1"},
		// run count is the secondary rule and is inclusive
		{"runs below", DecideInput{Policy: pol, Caps: resumable, Now: now, Active: rec(4, now, 0, 0)}, ModeResume, ReasonNone, false, "s1"},
		{"runs at limit", DecideInput{Policy: pol, Caps: resumable, Now: now, Active: rec(5, now, 0, 0)}, ModeFresh, ReasonMaxRuns, true, ""},
		{"runs at limit, clear supported", DecideInput{Policy: pol, Caps: clearable, Now: now, Active: rec(5, now, 0, 0)}, ModeClearContext, ReasonMaxRuns, true, "s1"},
		{"ratio is checked before runs", DecideInput{Policy: pol, Caps: resumable, Now: now, Active: rec(9, now, 800, 1000)}, ModeFresh, ReasonContextRatio, true, ""},
		// no age bound unless configured
		{"old session, no max age", DecideInput{Policy: pol, Caps: resumable, Now: now, Active: rec(1, 0, 0, 0)}, ModeResume, ReasonNone, false, "s1"},
		{"max age just below", DecideInput{Policy: Policy{MaxAge: time.Hour}, Caps: resumable, Now: now, Active: rec(1, now-3599, 0, 0)}, ModeResume, ReasonNone, false, "s1"},
		{"max age at limit", DecideInput{Policy: Policy{MaxAge: time.Hour}, Caps: resumable, Now: now, Active: rec(1, now-3600, 0, 0)}, ModeFresh, ReasonMaxAge, true, ""},
		{"ratio before age", DecideInput{Policy: Policy{MaxAge: time.Hour, MaxContextRatio: 0.5}, Caps: resumable, Now: now, Active: rec(1, 0, 600, 1000)}, ModeFresh, ReasonContextRatio, true, ""},
		{"all rules off", DecideInput{Policy: Policy{}, Caps: resumable, Now: now, Active: rec(500, 0, 999, 1000)}, ModeResume, ReasonNone, false, "s1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide(tc.in)
			if d.Mode != tc.mode || d.Reason != tc.reason || d.Rotated != tc.rotated || d.Session.ID != tc.sessionKey {
				t.Fatalf("Decide = %+v, want mode=%s reason=%q rotated=%v session=%q", d, tc.mode, tc.reason, tc.rotated, tc.sessionKey)
			}
		})
	}
}

func TestPolicyFromConfigDefaults(t *testing.T) {
	p := PolicyFromConfig(config.BackgroundAgentSessionConfig{})
	if p.MaxRuns != 20 || p.MaxContextRatio != 0.7 || p.MaxAge != 0 {
		t.Fatalf("defaults = %+v, want 20 runs / 0.7 / no age", p)
	}
	p = PolicyFromConfig(config.BackgroundAgentSessionConfig{MaxRuns: 3, MaxContextRatio: 0.5, MaxAge: "90m"})
	if p.MaxRuns != 3 || p.MaxContextRatio != 0.5 || p.MaxAge != 90*time.Minute {
		t.Fatalf("explicit = %+v", p)
	}
	if lim := LimitsFromConfig(config.BackgroundAgentsConfig{DailyCostUSD: 2, DailyTokens: 9}); lim.DailyCostUSD != 2 || lim.DailyTokens != 9 {
		t.Fatalf("limits = %+v", lim)
	}
}

func TestModelFromArgs(t *testing.T) {
	cases := map[string][]string{
		"haiku":  {"--model", "haiku"},
		"sonnet": {"-p", "-m", "sonnet", "--x"},
		"opus":   {"--model=opus"},
		"last":   {"--model", "first", "--model=last"},
		"":       {"--tools", ""},
	}
	for want, args := range cases {
		if got := ModelFromArgs(args); got != want {
			t.Errorf("ModelFromArgs(%v) = %q, want %q", args, got, want)
		}
	}
	if ModelFromArgs([]string{"--model"}) != "" {
		t.Fatal("a dangling --model must read as none")
	}
}

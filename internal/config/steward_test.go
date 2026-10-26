package config

import (
	"slices"
	"testing"
	"time"
)

func TestStewardConfigDefaults(t *testing.T) {
	var s StewardConfig
	if s.Enabled || s.AgentName() != "" {
		t.Fatalf("steward must be off and agent-less by default: %+v", s)
	}
	if s.ProjectKey() != DefaultProjectKey {
		t.Fatalf("project default = %q, want the built-in default project", s.ProjectKey())
	}
	if s.IdleEnd() != 30*time.Minute || s.MaxReviewItems() != 20 || s.EventThrottle() != 30*time.Minute {
		t.Fatalf("defaults wrong: %v %d %v", s.IdleEnd(), s.MaxReviewItems(), s.EventThrottle())
	}
}

func TestStewardReviewClockDefaultsToTenMinutesBeforeDigest(t *testing.T) {
	var s StewardConfig
	if h, m := s.ReviewClock(WorkConfig{}); h != 8 || m != 50 {
		t.Fatalf("default review clock = %02d:%02d, want 08:50 (10 min before the 09:00 digest)", h, m)
	}
	if h, m := s.ReviewClock(WorkConfig{DigestTime: "00:05"}); h != 23 || m != 55 {
		t.Fatalf("review clock must wrap past midnight, got %02d:%02d", h, m)
	}
	s.ReviewTime = "07:30"
	if h, m := s.ReviewClock(WorkConfig{}); h != 7 || m != 30 {
		t.Fatalf("explicit review time ignored: %02d:%02d", h, m)
	}
}

func TestStewardValidate(t *testing.T) {
	cases := map[string]StewardConfig{
		"negative idle":     {IdleEndMin: -1},
		"padded agent":      {Agent: " claude-acp"},
		"bad review time":   {ReviewTime: "25:00"},
		"garbage time":      {ReviewTime: "soon"},
		"negative throttle": {EventThrottleMin: -5},
		// gofer-j0aq: steward.approval is held to the same bar as a project's block.
		"unknown approval mode":    {Approval: &ApprovalConfig{Mode: "on"}},
		"unknown approval kind":    {Approval: &ApprovalConfig{AutoAllowKinds: []string{"write"}}},
		"empty approval tool name": {Approval: &ApprovalConfig{AutoAllowTools: []string{" "}}},
	}
	for name, s := range cases {
		if err := s.validate(); err == nil {
			t.Errorf("%s: want a validation error", name)
		}
	}
	if err := (StewardConfig{Enabled: true, Agent: "claude-acp", ReviewTime: "08:15"}).validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if err := (StewardConfig{Approval: &ApprovalConfig{Mode: ApprovalAsk}}).validate(); err != nil {
		t.Fatalf("a valid steward approval block rejected: %v", err)
	}
}

// TestStewardApprovalFromYAML is the config-file half of gofer-j0aq: `steward.approval`
// loads as an ordinary approval block (so a file carrying it is accepted), and a bad block
// is REFUSED at load instead of silently widening the steward's gate.
func TestStewardApprovalFromYAML(t *testing.T) {
	base := "steward:\n  enabled: true\n  agent: claude-acp\n"
	cfg, err := loadMailCfg(t, base+"  approval:\n    mode: ask\n    ask_tools: [gofer_work_remind]\n")
	if err != nil {
		t.Fatalf("steward.approval rejected at load: %v", err)
	}
	if cfg.Steward.Approval == nil || cfg.Steward.Approval.Mode != ApprovalAsk {
		t.Fatalf("steward.approval = %+v, want mode ask", cfg.Steward.Approval)
	}
	if pol := cfg.Steward.ApprovalPolicy(); !slices.Contains(pol.AskTools, "gofer_work_remind") {
		t.Fatalf("resolved steward policy lost the configured ask_tools: %+v", pol)
	}
	if _, err := loadMailCfg(t, base+"  approval:\n    mode: sometimes\n"); err == nil {
		t.Fatal("an unknown steward.approval.mode must fail the load")
	}
}

// TestStewardApprovalPolicy pins gofer-j0aq's default: the steward's session runs mode=ask
// with gofer's own read-only / advice-only tools PLUS the steward's write tools, so its
// tidy-up (remind / note / ask a session to report) runs without a human on a card nobody
// answers — while every other ACP session of the project stays on the project's policy.
func TestStewardApprovalPolicy(t *testing.T) {
	var s StewardConfig
	pol := s.ApprovalPolicy()
	if pol.Mode != ApprovalAsk {
		t.Fatalf("default steward approval mode = %q, want ask", pol.Mode)
	}
	tool := func(list []string, name string) bool { return slices.Contains(list, name) }
	for _, name := range []string{
		"gofer_work_list",     // read-only
		"gofer_today_advise",  // advice-only write
		"gofer_work_remind",   // steward write: the gofer-j0aq case
		"gofer_steward_notes", //
		"gofer_work_request_report",
		"gofer_session_ask",
	} {
		if !tool(pol.AutoAllowTools, name) {
			t.Errorf("default steward auto_allow_tools is missing %q: %v", name, pol.AutoAllowTools)
		}
	}
	// Writes that are NOT the steward's job stay gated.
	for _, name := range []string{"gofer_run_job", "gofer_cancel_job", "gofer_memory_rm", "gofer_session_mail_reply"} {
		if tool(pol.AutoAllowTools, name) {
			t.Errorf("steward auto_allow_tools must not contain %q", name)
		}
	}
	// The default is not the ordinary ACP default: those writes are gated everywhere else.
	if slices.Contains(DefaultApprovalAutoAllowTools, "gofer_work_remind") {
		t.Fatal("gofer_work_remind must stay gated for an ordinary ACP session")
	}

	// A block that only sets the mode keeps the steward's own tool list (partial config
	// must not silently re-gate the tools the steward is made of)...
	s.Approval = &ApprovalConfig{Mode: ApprovalStrict}
	if pol = s.ApprovalPolicy(); pol.Mode != ApprovalStrict || !tool(pol.AutoAllowTools, "gofer_work_remind") {
		t.Fatalf("mode-only block = %+v, want strict + the steward tool list", pol)
	}
	// ...and an explicit list REPLACES it, exactly like the project block.
	s.Approval = &ApprovalConfig{Mode: ApprovalAsk, AutoAllowTools: []string{"gofer_work_remind"}}
	if pol = s.ApprovalPolicy(); len(pol.AutoAllowTools) != 1 || pol.AutoAllowTools[0] != "gofer_work_remind" {
		t.Fatalf("explicit auto_allow_tools = %v, want exactly the configured entry", pol.AutoAllowTools)
	}
}

// Without steward.approval the steward keeps its project's mode — approval off stays off —
// and only gains its own writes; it is never gated harder than the project's other
// sessions (the first cut defaulted to mode=ask and parked stewards on approval-off
// projects).
func TestStewardApprovalOverKeepsTheProjectMode(t *testing.T) {
	off := StewardApprovalOver(ProjectConfig{}.ApprovalPolicy())
	if off.Mode != ApprovalOff {
		t.Fatalf("mode = %q over an approval-off project, want off", off.Mode)
	}
	ask := StewardApprovalOver(ApprovalConfig{Mode: ApprovalAsk})
	if ask.Mode != ApprovalAsk || !slices.Contains(ask.AutoAllowTools, "gofer_work_remind") || !slices.Contains(ask.AutoAllowTools, "gofer_today_list") {
		t.Fatalf("over an ask project = %+v, want ask + read-only tools + the steward's writes", ask)
	}
	strict := StewardApprovalOver(ApprovalConfig{Mode: ApprovalStrict})
	if strict.Mode != ApprovalStrict {
		t.Fatalf("mode = %q over a strict project, want strict", strict.Mode)
	}
	twice := StewardApprovalOver(ask)
	if len(twice.AutoAllowTools) != len(ask.AutoAllowTools) {
		t.Fatalf("tools duplicated: %d then %d", len(ask.AutoAllowTools), len(twice.AutoAllowTools))
	}
}

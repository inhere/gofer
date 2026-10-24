package config

import (
	"strings"
	"testing"
)

// TestApprovalPolicyDefaultsAndValidation pins the project approval gate's defaults
// (design §GATE-01.1) and its LOAD-time validation: an unset block must read as the
// documented policy (off, the two default kind lists, 30min, reject, remember), and a
// typo in any enum/kind must fail the load instead of silently weakening the gate.
func TestApprovalPolicyDefaultsAndValidation(t *testing.T) {
	def := ProjectConfig{}.ApprovalPolicy()
	if def.Mode != ApprovalOff {
		t.Errorf("default mode = %q, want %q", def.Mode, ApprovalOff)
	}
	if def.TimeoutSec != DefaultApprovalTimeoutSec {
		t.Errorf("default timeout_sec = %d, want %d", def.TimeoutSec, DefaultApprovalTimeoutSec)
	}
	if def.OnTimeout != ApprovalOnTimeoutReject {
		t.Errorf("default on_timeout = %q, want %q", def.OnTimeout, ApprovalOnTimeoutReject)
	}
	if !def.AllowsAlways() {
		t.Error("default remember_allow_always = false, want true")
	}
	if got, want := strings.Join(def.AutoAllowKinds, ","), strings.Join(DefaultApprovalAutoAllowKinds, ","); got != want {
		t.Errorf("default auto_allow_kinds = %q, want %q", got, want)
	}
	if got, want := strings.Join(def.AskKinds, ","), strings.Join(DefaultApprovalAskKinds, ","); got != want {
		t.Errorf("default ask_kinds = %q, want %q", got, want)
	}
	if got, want := strings.Join(def.AutoAllowTools, ","), strings.Join(DefaultApprovalAutoAllowTools, ","); got != want {
		t.Errorf("default auto_allow_tools = %q, want %q", got, want)
	}
	if len(def.AskTools) != 0 {
		t.Errorf("default ask_tools = %v, want empty", def.AskTools)
	}

	// A partial block keeps what it sets and defaults the rest.
	no := false
	part := ProjectConfig{Approval: &ApprovalConfig{
		Mode:                ApprovalAsk,
		AutoAllowKinds:      []string{"read", "search"},
		TimeoutSec:          60,
		RememberAllowAlways: &no,
	}}.ApprovalPolicy()
	if part.Mode != ApprovalAsk || part.TimeoutSec != 60 || part.OnTimeout != ApprovalOnTimeoutReject {
		t.Errorf("partial block resolved to %+v, want mode=ask timeout=60 on_timeout=reject", part)
	}
	if part.AllowsAlways() {
		t.Error("remember_allow_always: false was dropped")
	}
	// ask_kinds unset keeps the default list; auto_allow_kinds is the explicit one.
	if got, want := strings.Join(part.AutoAllowKinds, ","), "read,search"; got != want {
		t.Errorf("auto_allow_kinds = %q, want %q", got, want)
	}
	if len(part.AskKinds) != len(DefaultApprovalAskKinds) {
		t.Errorf("ask_kinds = %v, want the default list", part.AskKinds)
	}

	// Kind classification: ask_kinds wins over auto_allow_kinds, and a kind named by
	// neither list is a request for approval (fail-closed).
	auto := ProjectConfig{Approval: &ApprovalConfig{Mode: ApprovalAsk}}.ApprovalPolicy()
	if !auto.AutoAllow("read") {
		t.Error("read should be auto-allowed by the default policy")
	}
	if auto.AutoAllow("edit") {
		t.Error("edit should require approval under mode=ask")
	}
	if auto.AutoAllow("brand_new_kind") {
		t.Error("an unknown tool kind must require approval, not slip through")
	}
	both := ProjectConfig{Approval: &ApprovalConfig{
		AutoAllowKinds: []string{"read"},
		AskKinds:       []string{"read"},
	}}.ApprovalPolicy()
	if both.AutoAllow("read") {
		t.Error("ask_kinds must take precedence over auto_allow_kinds")
	}

	// Tool names (gofer-78on): the default list is gofer's read-only tools plus its
	// advice-only ones, ask_tools wins, an explicit list replaces the default, and an
	// entry matches either spelling of the same tool.
	if !auto.AutoAllowTool("gofer_today_advise") {
		t.Error("gofer_today_advise must be auto-allowed by the default policy")
	}
	if !auto.AutoAllowTool("mcp__gofer__gofer_today_advise") {
		t.Error("an MCP-qualified gofer tool name must match the same default entry")
	}
	if auto.AutoAllowTool("gofer_run_job") {
		t.Error("a gofer write tool must require approval under mode=ask")
	}
	if auto.AutoAllowTool("Bash") {
		t.Error("a tool named by neither list must require approval")
	}
	tools := ProjectConfig{Approval: &ApprovalConfig{
		AutoAllowTools: []string{"Bash", "mcp__docs__search"},
		AskTools:       []string{"Bash", "gofer_work_list"},
	}}.ApprovalPolicy()
	if tools.AutoAllowTool("Bash") || !tools.AsksTool("Bash") {
		t.Error("ask_tools must take precedence over auto_allow_tools")
	}
	if !tools.AutoAllowTool("search") {
		t.Error("a bare list entry must match the qualified request name")
	}
	if tools.AutoAllowTool("gofer_today_advise") {
		t.Error("an explicit auto_allow_tools list must replace the default one")
	}
	if tools.AutoAllowTool("gofer_work_list") {
		t.Error("ask_tools must veto a name that the default list would allow")
	}

	bad := []struct {
		name string
		ap   ApprovalConfig
		want string
	}{
		{"unknown mode", ApprovalConfig{Mode: "on"}, "approval.mode"},
		{"unknown on_timeout", ApprovalConfig{OnTimeout: "deny"}, "on_timeout"},
		{"unknown auto_allow kind", ApprovalConfig{AutoAllowKinds: []string{"write"}}, "auto_allow_kinds"},
		{"unknown ask kind", ApprovalConfig{AskKinds: []string{"shell"}}, "ask_kinds"},
		{"empty auto_allow_tools entry", ApprovalConfig{AutoAllowTools: []string{" "}}, "auto_allow_tools"},
		{"ask_tools entry with whitespace", ApprovalConfig{AskTools: []string{" Bash"}}, "ask_tools"},
		{"negative timeout", ApprovalConfig{TimeoutSec: -1}, "timeout_sec"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			ap := tc.ap
			err := validate(&Config{Projects: map[string]ProjectConfig{
				"demo": {HostPath: "/tmp/demo", Approval: &ap},
			}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validate() = %v, want an error naming %q", err, tc.want)
			}
		})
	}

	ok := []ApprovalConfig{
		{Mode: ApprovalStrict},
		{Mode: ApprovalOff, OnTimeout: ApprovalOnTimeoutAllow},
		{Mode: ApprovalAsk, AutoAllowKinds: []string{"read"}, AskKinds: []string{"edit", "switch_mode"}, TimeoutSec: 30},
		{Mode: ApprovalAsk, AutoAllowTools: []string{"Bash", "mcp__docs__search"}, AskTools: []string{"Read"}},
	}
	for i, ap := range ok {
		ap := ap
		if err := validate(&Config{Projects: map[string]ProjectConfig{
			"demo": {HostPath: "/tmp/demo", Approval: &ap},
		}}); err != nil {
			t.Fatalf("case %d: validate() = %v, want nil", i, err)
		}
	}

	// The agent-level tightening knob is validated with the same rigour: an
	// unknown permission_policy would otherwise be silently ignored at run time.
	if err := validate(&Config{Agents: map[string]AgentConfig{
		"bot": {Type: "acp-agent", ACP: &ACPConfig{PermissionPolicy: "always"}},
	}}); err == nil || !strings.Contains(err.Error(), "permission_policy") {
		t.Fatalf("validate(agent permission_policy) = %v, want an error naming permission_policy", err)
	}
	for _, pol := range []string{"", ApprovalAutoAllow, ApprovalAsk, ApprovalStrict} {
		if err := validate(&Config{Agents: map[string]AgentConfig{
			"bot": {Type: "acp-agent", ACP: &ACPConfig{PermissionPolicy: pol}},
		}}); err != nil {
			t.Fatalf("validate(agent permission_policy=%q) = %v, want nil", pol, err)
		}
	}
}

// TestApprovalWithOverrideReplacesProject: a request-level policy (a resident 收件会话's
// mail.agents.<名字>.approval, gofer-81b4) REPLACES the project's policy rather than
// tightening it — that is what lets one session ask while the project auto-allows — and
// only the agent's own acp.permission_policy still tightens it.
func TestApprovalWithOverrideReplacesProject(t *testing.T) {
	cfg := &Config{
		Projects: map[string]ProjectConfig{
			"strictproj": {Approval: &ApprovalConfig{Mode: ApprovalStrict, TimeoutSec: 5}},
			"openproj":   {},
		},
		Agents: map[string]AgentConfig{
			"plain":  {Type: "acp-agent"},
			"strict": {Type: "acp-agent", ACP: &ACPConfig{PermissionPolicy: ApprovalStrict}},
		},
	}
	ask := &ApprovalConfig{Mode: ApprovalAsk, TimeoutSec: 60}

	// The override wins over a STRICTER project: it is not a merge.
	got := cfg.EffectiveApprovalWith("strictproj", "plain", ask)
	if got.Mode != ApprovalAsk || got.TimeoutSec != 60 {
		t.Fatalf("override over strict project = %+v, want mode=ask timeout=60", got)
	}
	if len(got.AutoAllowKinds) == 0 || len(got.AskKinds) == 0 {
		t.Fatalf("the override lost its resolved kind lists: %+v", got)
	}
	// An override of off over an asking project stays off (the project is replaced).
	if got := cfg.EffectiveApprovalWith("openproj", "plain", &ApprovalConfig{Mode: ApprovalOff}); got.Mode != ApprovalOff {
		t.Fatalf("override off = %q, want off", got.Mode)
	}
	// The agent knob still only tightens: strict beats the session's ask.
	if got := cfg.EffectiveApprovalWith("openproj", "strict", ask); got.Mode != ApprovalStrict {
		t.Fatalf("override ask + agent strict = %q, want %q", got.Mode, ApprovalStrict)
	}
	// No override = the project policy, exactly as before.
	if got := cfg.EffectiveApprovalWith("strictproj", "plain", nil); got.Mode != ApprovalStrict || got.TimeoutSec != 5 {
		t.Fatalf("nil override over strict project = %+v, want the project policy", got)
	}
}

// TestApprovalAgentCanOnlyTighten: an agent's acp.permission_policy may only TIGHTEN
// the project's approval mode (design §GATE-01.1) — ask/strict raise a permissive
// project, and a stricter project always wins over a laxer agent.
func TestApprovalAgentCanOnlyTighten(t *testing.T) {
	cases := []struct {
		name    string
		project string
		agent   string
		want    string
	}{
		{"no approval block, no agent policy", "", "", ApprovalOff},
		{"no approval block, agent auto_allow", "", "auto_allow", ApprovalOff},
		{"no approval block, agent ask", "", ApprovalAsk, ApprovalAsk},
		{"no approval block, agent strict", "", ApprovalStrict, ApprovalStrict},
		{"project off, agent ask", ApprovalOff, ApprovalAsk, ApprovalAsk},
		{"project off, agent strict", ApprovalOff, ApprovalStrict, ApprovalStrict},
		{"project ask, agent auto_allow stays ask", ApprovalAsk, "auto_allow", ApprovalAsk},
		{"project ask, agent ask", ApprovalAsk, ApprovalAsk, ApprovalAsk},
		{"project ask, agent strict", ApprovalAsk, ApprovalStrict, ApprovalStrict},
		{"project strict, agent auto_allow stays strict", ApprovalStrict, "auto_allow", ApprovalStrict},
		{"project strict, agent ask stays strict", ApprovalStrict, ApprovalAsk, ApprovalStrict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proj := ProjectConfig{HostPath: "/tmp/demo"}
			if tc.project != "" {
				proj.Approval = &ApprovalConfig{Mode: tc.project, AutoAllowKinds: []string{"read", "search"}}
			}
			cfg := &Config{
				Projects: map[string]ProjectConfig{"demo": proj},
				Agents: map[string]AgentConfig{
					"bot": {Type: "acp-agent", ACP: &ACPConfig{PermissionPolicy: tc.agent}},
				},
			}
			got := cfg.EffectiveApproval("demo", "bot")
			if got.Mode != tc.want {
				t.Fatalf("EffectiveApproval(%q, %q).Mode = %q, want %q", tc.project, tc.agent, got.Mode, tc.want)
			}
			// The tightened policy keeps the project's kind lists/defaults — the agent
			// knob only raises the MODE.
			if len(got.AutoAllowKinds) == 0 || len(got.AskKinds) == 0 || got.TimeoutSec == 0 {
				t.Fatalf("effective policy lost its resolved fields: %+v", got)
			}
		})
	}

	// An unknown project/agent resolves to the defaults rather than panicking.
	got := (&Config{}).EffectiveApproval("nope", "nobody")
	if got.Mode != ApprovalOff || got.TimeoutSec != DefaultApprovalTimeoutSec {
		t.Fatalf("EffectiveApproval(unknown) = %+v, want the defaults", got)
	}
}

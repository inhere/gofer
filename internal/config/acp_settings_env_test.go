package config

import (
	"slices"
	"testing"
)

// TestACPConfigInheritsClaudeSettingsEnv pins the F14 switch's semantics: it is opt-in
// per agent (an explicit true only). A nil ACPConfig is "this agent says nothing", and
// an unset field is NOT the same decision as an explicit false — the built-in
// claude-acp template sets true, everything else (including a hand-written acp-agent)
// stays off until an operator turns it on.
func TestACPConfigInheritsClaudeSettingsEnv(t *testing.T) {
	on, off := true, false
	cases := []struct {
		name string
		acp  *ACPConfig
		want bool
	}{
		{"nil_block", nil, false},
		{"unset", &ACPConfig{}, false},
		{"explicit_false", &ACPConfig{ClaudeSettingsEnv: &off}, false},
		{"explicit_true", &ACPConfig{ClaudeSettingsEnv: &on}, true},
	}
	for _, tc := range cases {
		if got := tc.acp.InheritsClaudeSettingsEnv(); got != tc.want {
			t.Errorf("%s: InheritsClaudeSettingsEnv() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestFieldPolicyAcpClaudeSettingsEnvIsEditable pins the R3 side of F14: the member is
// writable and HOT (no restart, the next job reads the file), which it gets from the
// `agents.*.acp` row through the longest-prefix rule — a nested acp member is carried
// inside the block's patch body, so its path resolves there like `modes` and
// `log_thoughts` do.
//
// It is deliberately NOT a row of its own, and therefore not in EditableAgentFields():
// that list is the FLAT field vocabulary applyAgentWrite clears when a body omits a
// field, and a dotted name in it would be "cleared" as if it were a top-level field.
func TestFieldPolicyAcpClaudeSettingsEnvIsEditable(t *testing.T) {
	for _, path := range []string{
		"agents.*.acp.claude_settings_env",
		"agents.claude-acp.acp.claude_settings_env",
	} {
		fp, ok := FieldPolicyFor(path)
		if !ok {
			t.Fatalf("FieldPolicyFor(%q) missed; the nested acp member is unclassified", path)
		}
		if !fp.Editable || fp.RestartRequired {
			t.Fatalf("FieldPolicyFor(%q) = %+v, want editable and hot (no restart)", path, fp)
		}
	}
	if slices.Contains(EditableAgentFields(), "acp.claude_settings_env") {
		t.Error("EditableAgentFields() carries the nested member: the flat clear-set must stay top-level")
	}
	if !slices.Contains(EditableAgentFields(), "acp") {
		t.Error("EditableAgentFields() lost the `acp` block itself")
	}
}

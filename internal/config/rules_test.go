package config

import "testing"

// TestRulesUnionAcrossLevels pins JOB-06①'s four-level MANDATORY rule union: the
// levels accumulate in the order server → agent → project → this request, duplicates
// are dropped keeping FIRST-appearance order, and the two off switches (--no-rules,
// an exec agent that has no prompt to inject into) return none. Rules are a UNION
// like skills and unlike the nearest-layer-wins resolvers (retry/fallback): an agent
// adding its own rule must never unhook the project's or the deployment's.
func TestRulesUnionAcrossLevels(t *testing.T) {
	cfg := &Config{
		Server:   ServerConfig{Rules: []string{"house-rules"}},
		Agents:   map[string]AgentConfig{"omp": {Rules: []string{"windows-host"}}},
		Projects: map[string]ProjectConfig{"hyy": {Rules: []string{"gofer-repo"}}},
	}

	got := cfg.EffectiveRules("hyy", "omp", []string{"job-only"}, false, "cli-agent")
	want := []string{"house-rules", "windows-host", "gofer-repo", "job-only"}
	if len(got) != len(want) {
		t.Fatalf("EffectiveRules = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("EffectiveRules = %v, want %v (level order server → agent → project → job)", got, want)
		}
	}

	// A duplicate is kept once, at its FIRST position: a project re-binding the
	// deployment's rule must not make it appear twice in the injected section.
	got = cfg.EffectiveRules("hyy", "omp", []string{"gofer-repo", "house-rules"}, false, "cli-agent")
	want = []string{"house-rules", "windows-host", "gofer-repo"}
	if len(got) != len(want) {
		t.Fatalf("deduplicated union = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("deduplicated union = %v, want %v", got, want)
		}
	}

	// --no-rules wins over every level, including this request's own --rule names.
	if got := cfg.EffectiveRules("hyy", "omp", []string{"job-only"}, true, "cli-agent"); len(got) != 0 {
		t.Fatalf("with disable = %v, want none", got)
	}
	// An exec agent runs a caller-supplied command: it has no prompt to inject into.
	if got := cfg.EffectiveRules("hyy", "omp", []string{"job-only"}, false, "exec"); len(got) != 0 {
		t.Fatalf("exec agent = %v, want none", got)
	}
	// An unknown project/agent key never panics: the levels that exist still apply.
	if got := cfg.EffectiveRules("nope", "nope", nil, false, "cli-agent"); len(got) != 1 || got[0] != "house-rules" {
		t.Fatalf("unknown keys = %v, want the server level [house-rules]", got)
	}

	// "Nothing bound" is nil (one shape for callers and encoders), and blank names are
	// dropped rather than becoming a rule called "".
	empty := &Config{Agents: map[string]AgentConfig{"omp": {}}, Projects: map[string]ProjectConfig{"hyy": {}}}
	if got := empty.EffectiveRules("hyy", "omp", nil, false, "cli-agent"); got != nil {
		t.Fatalf("nothing bound = %v, want nil", got)
	}
	if got := empty.EffectiveRules("hyy", "omp", []string{"", "  "}, false, "cli-agent"); got != nil {
		t.Fatalf("blank names = %v, want nil", got)
	}
	// A nil config is the worker side of a POLICY-pushed deployment: not a panic.
	var nilCfg *Config
	if got := nilCfg.EffectiveRules("hyy", "omp", []string{"a"}, false, "cli-agent"); len(got) != 1 || got[0] != "a" {
		t.Fatalf("nil config = %v, want the request's own [a]", got)
	}
}

// TestEffectiveRulesMaxBytes: the total-rule cap is server.rules_max_bytes with the
// shipped 16KiB default; 0 and negative read as "unset" (the way every other cap in
// gofer's config behaves), never as "reject everything".
func TestEffectiveRulesMaxBytes(t *testing.T) {
	if got := DefaultRulesMaxBytes; got != 16384 {
		t.Fatalf("DefaultRulesMaxBytes = %d, want 16384", got)
	}
	if got := (&Config{}).EffectiveRulesMaxBytes(); got != DefaultRulesMaxBytes {
		t.Fatalf("unset cap = %d, want the default %d", got, DefaultRulesMaxBytes)
	}
	if got := (&Config{Server: ServerConfig{RulesMaxBytes: 4096}}).EffectiveRulesMaxBytes(); got != 4096 {
		t.Fatalf("configured cap = %d, want 4096", got)
	}
	if got := (&Config{Server: ServerConfig{RulesMaxBytes: -1}}).EffectiveRulesMaxBytes(); got != DefaultRulesMaxBytes {
		t.Fatalf("negative cap = %d, want the default %d", got, DefaultRulesMaxBytes)
	}
	var nilCfg *Config
	if got := nilCfg.EffectiveRulesMaxBytes(); got != DefaultRulesMaxBytes {
		t.Fatalf("nil config cap = %d, want the default %d", got, DefaultRulesMaxBytes)
	}
}

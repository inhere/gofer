package config

import "testing"

func TestBudgetOverIsPerDimension(t *testing.T) {
	def := &Budget{MaxTokens: 100, MaxCostUSD: 2, MaxTurns: 10}
	got := (&Budget{MaxTurns: 3}).Over(def)
	if got == nil || *got != (Budget{MaxTokens: 100, MaxCostUSD: 2, MaxTurns: 3}) {
		t.Fatalf("over = %+v", got)
	}
	if (*Budget)(nil).Over(nil) != nil || (&Budget{}).Over(&Budget{}) != nil {
		t.Fatal("nothing limited must normalise to nil")
	}
}

func TestEffectiveBudgetLayers(t *testing.T) {
	cfg := &Config{
		Agents:   map[string]AgentConfig{"a": {Budget: &Budget{MaxTokens: 1, MaxCostUSD: 1, MaxTurns: 1}}},
		Projects: map[string]ProjectConfig{"p": {Budget: &Budget{MaxTokens: 2, MaxCostUSD: 2}}},
	}
	if got := cfg.EffectiveBudget("p", "a", &Budget{MaxCostUSD: 3}); got == nil || *got != (Budget{MaxTokens: 2, MaxCostUSD: 3, MaxTurns: 1}) {
		t.Fatalf("agent < project < request: %+v", got)
	}
	if got := cfg.EffectiveBudget("other", "none", nil); got != nil {
		t.Fatalf("no layer set = %+v, want nil", got)
	}
}

func TestValidateBudgetRejectsNegative(t *testing.T) {
	bad := &Config{Agents: map[string]AgentConfig{"a": {Type: "cli-agent", Command: "a", Args: []string{"{{prompt}}"}, Budget: &Budget{MaxTokens: -1}}}}
	if err := Validate(bad); err == nil {
		t.Fatal("negative agent budget accepted")
	}
	badProj := &Config{Projects: map[string]ProjectConfig{"p": {HostPath: "/x", Budget: &Budget{MaxCostUSD: -0.5}}}}
	if err := Validate(badProj); err == nil {
		t.Fatal("negative project budget accepted")
	}
}

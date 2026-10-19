package config

import (
	"strings"
	"testing"
)

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

func TestBudgetAgentDimensions(t *testing.T) {
	soft := &Budget{AgentTokens: 100, AgentCostUSD: 1}
	if soft.IsZero() || soft.HasHard() || !soft.HasAgent() {
		t.Fatalf("agent-only budget: zero=%v hard=%v agent=%v", soft.IsZero(), soft.HasHard(), soft.HasAgent())
	}
	hard := &Budget{MaxTokens: 5}
	if hard.HasAgent() || !hard.HasHard() {
		t.Fatal("hard-only budget misclassified")
	}
	got := (&Budget{AgentTokens: 7}).Over(&Budget{MaxTokens: 5, AgentTokens: 1, AgentCostUSD: 2})
	if got == nil || *got != (Budget{MaxTokens: 5, AgentTokens: 7, AgentCostUSD: 2}) {
		t.Fatalf("over = %+v, want per-dimension merge including agent_*", got)
	}
	both := Budget{MaxTokens: 5, AgentTokens: 7}
	if w := both.WithoutAgent(); w == nil || *w != (Budget{MaxTokens: 5}) {
		t.Fatalf("WithoutAgent = %+v", w)
	}
	if w := both.WithoutHard(); w == nil || *w != (Budget{AgentTokens: 7}) {
		t.Fatalf("WithoutHard = %+v", w)
	}
	if (&Budget{AgentTokens: 7}).WithoutAgent() != nil {
		t.Fatal("removing the only dimension must normalise to nil")
	}
	if err := (&Budget{AgentCostUSD: -1}).Validate(); err == nil {
		t.Fatal("negative agent_cost_usd validated")
	}
	if err := (&Budget{AgentTokens: -1}).Validate(); err == nil {
		t.Fatal("negative agent_tokens validated")
	}
}

func TestValidateAgentBudgetMapping(t *testing.T) {
	mk := func(typ string, m *AgentBudgetMap) *Config {
		return &Config{Agents: map[string]AgentConfig{"a": {Type: typ, Command: "a", Args: []string{"{{prompt}}"}, AgentBudget: m}}}
	}
	ok := &AgentBudgetMap{TokensArgs: []string{"--t", "{{budget_tokens}}"}, CostEnv: "COST_BUDGET"}
	if err := Validate(mk("cli-agent", ok)); err != nil {
		t.Fatalf("valid mapping rejected: %v", err)
	}
	if err := Validate(mk("cli-agent", nil)); err != nil {
		t.Fatalf("unset mapping rejected: %v", err)
	}
	for name, cfg := range map[string]*Config{
		"missing placeholder": mk("cli-agent", &AgentBudgetMap{TokensArgs: []string{"--t", "5"}}),
		"wrong placeholder":   mk("cli-agent", &AgentBudgetMap{CostArgs: []string{"--c", "{{budget_tokens}}"}}),
		"prompt placeholder":  mk("cli-agent", &AgentBudgetMap{TokensArgs: []string{"{{prompt}}", "{{budget_tokens}}"}}),
		"bad env name":        mk("cli-agent", &AgentBudgetMap{TokensEnv: "A=B"}),
		"exec agent":          {Agents: map[string]AgentConfig{"a": {Type: "exec", AgentBudget: ok}}},
	} {
		if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "agent_budget") {
			t.Fatalf("%s: err = %v, want an agent_budget error", name, err)
		}
	}
	if !ok.SupportsTokens() || !ok.SupportsCost() || (&AgentBudgetMap{TokensEnv: "X"}).SupportsCost() || (*AgentBudgetMap)(nil).SupportsTokens() {
		t.Fatal("Supports* misreports the mapping")
	}
}

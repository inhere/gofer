package agent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestBuildFromAgentBudget(t *testing.T) {
	mapping := &config.AgentBudgetMap{
		TokensArgs: []string{"--budget-tokens", "{{budget_tokens}}"},
		CostArgs:   []string{"--budget-cost-usd", "{{budget_cost_usd}}"},
		TokensEnv:  "SUGA_BUDGET_TOKENS",
		CostEnv:    "SUGA_BUDGET_COST_USD",
	}
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"cli":      {Type: TypeCLIAgent, Command: "x", Args: []string{"run", "--prompt", "{{prompt}}"}, AgentBudget: mapping},
		"cli-env":  {Type: TypeCLIAgent, Command: "x", Args: []string{"run", "{{prompt}}"}, AgentBudget: &config.AgentBudgetMap{TokensEnv: "T"}},
		"acp":      {Type: TypeACPAgent, Command: "x", Args: []string{"acp"}, AgentBudget: mapping},
		"plain":    {Type: TypeCLIAgent, Command: "x", Args: []string{"{{prompt}}"}},
		"tokens-o": {Type: TypeCLIAgent, Command: "x", Args: []string{"{{prompt}}"}, AgentBudget: &config.AgentBudgetMap{TokensEnv: "T"}},
		"exec":     {Type: TypeExec},
	}}
	b := &config.Budget{AgentTokens: 50000, AgentCostUSD: 1.5}

	t.Run("cli-agent: argv appended after the template, before agent args", func(t *testing.T) {
		res, err := BuildFrom(cfg, "cli", "go", nil, Vars{}, BuildOptions{Budget: b, AgentArgs: []string{"--x"}})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"run", "--prompt", "go", "--budget-tokens", "50000", "--budget-cost-usd", "1.5", "--x"}
		if !reflect.DeepEqual(res.Args, want) {
			t.Fatalf("argv = %#v, want %#v", res.Args, want)
		}
		if len(res.Env) != 0 {
			t.Fatalf("env = %v, want none when argv maps both dimensions", res.Env)
		}
	})

	t.Run("cli-agent with only an env mapping exports the variable", func(t *testing.T) {
		res, err := BuildFrom(cfg, "cli-env", "go", nil, Vars{}, BuildOptions{Budget: &config.Budget{AgentTokens: 9}})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(res.Args, []string{"run", "go"}) || res.Env["T"] != "9" {
			t.Fatalf("args=%v env=%v", res.Args, res.Env)
		}
	})

	t.Run("acp-agent prefers the environment", func(t *testing.T) {
		res, err := BuildFrom(cfg, "acp", "", nil, Vars{}, BuildOptions{Budget: b})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(res.Args, []string{"acp"}) || res.Env["SUGA_BUDGET_TOKENS"] != "50000" || res.Env["SUGA_BUDGET_COST_USD"] != "1.5" {
			t.Fatalf("args=%v env=%v", res.Args, res.Env)
		}
	})

	t.Run("no agent budget: argv untouched, hard limits are not rendered", func(t *testing.T) {
		res, err := BuildFrom(cfg, "cli", "go", nil, Vars{}, BuildOptions{Budget: &config.Budget{MaxTokens: 5}})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"run", "--prompt", "go"}; !reflect.DeepEqual(res.Args, want) {
			t.Fatalf("argv = %#v, want %#v", res.Args, want)
		}
	})

	t.Run("a dimension without a mapping is an error, never dropped", func(t *testing.T) {
		for _, tc := range []struct {
			key string
			b   *config.Budget
		}{
			{"plain", &config.Budget{AgentTokens: 1}},
			{"tokens-o", &config.Budget{AgentCostUSD: 1}},
			{"exec", &config.Budget{AgentTokens: 1}},
		} {
			_, err := BuildFrom(cfg, tc.key, "go", []string{"echo"}, Vars{}, BuildOptions{Budget: tc.b})
			if err == nil || !strings.Contains(err.Error(), "support budgets") {
				t.Fatalf("%s: err = %v, want a does-not-support-budgets error", tc.key, err)
			}
		}
	})

	t.Run("the template is not mutated", func(t *testing.T) {
		if _, err := BuildFrom(cfg, "cli", "go", nil, Vars{}, BuildOptions{Budget: b}); err != nil {
			t.Fatal(err)
		}
		if got := cfg.Agents["cli"].Args; !reflect.DeepEqual(got, []string{"run", "--prompt", "{{prompt}}"}) {
			t.Fatalf("template mutated: %v", got)
		}
	})
}

// The built-in suga template carries the mapping (cli-agent by key, acp-agent by command
// name), and an explicit mapping on the agent wins.
func TestAgentBudgetBuiltinSuga(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"suga":     {Type: TypeCLIAgent, Command: "suga", Args: []string{"run", "--prompt", "{{prompt}}"}},
		"suga-acp": {Type: TypeACPAgent, Command: "suga", Args: []string{"acp"}},
		"mine":     {Type: TypeCLIAgent, Command: "suga", Args: []string{"run", "{{prompt}}"}, AgentBudget: &config.AgentBudgetMap{TokensEnv: "MINE"}},
		"claude":   {Type: TypeCLIAgent, Command: "claude", Args: []string{"-p", "{{prompt}}"}},
	}}
	b := &config.Budget{AgentTokens: 200000, AgentCostUSD: 2}

	res, err := BuildFrom(cfg, "suga", "go", nil, Vars{}, BuildOptions{Budget: b})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"run", "--prompt", "go", "--budget-tokens", "200000", "--budget-cost-usd", "2"}; !reflect.DeepEqual(res.Args, want) {
		t.Fatalf("suga argv = %#v, want %#v", res.Args, want)
	}
	res, err = BuildFrom(cfg, "suga-acp", "", nil, Vars{}, BuildOptions{Budget: b})
	if err != nil {
		t.Fatal(err)
	}
	if res.Env["SUGA_BUDGET_TOKENS"] != "200000" || res.Env["SUGA_BUDGET_COST_USD"] != "2" {
		t.Fatalf("suga-acp env = %v", res.Env)
	}
	res, err = BuildFrom(cfg, "mine", "go", nil, Vars{}, BuildOptions{Budget: &config.Budget{AgentTokens: 3}})
	if err != nil || res.Env["MINE"] != "3" {
		t.Fatalf("explicit mapping: err=%v env=%v", err, res.Env)
	}
	if _, err := BuildFrom(cfg, "mine", "go", nil, Vars{}, BuildOptions{Budget: &config.Budget{AgentCostUSD: 3}}); err == nil {
		t.Fatal("an explicit mapping must not be topped up from the built-in table")
	}
	if _, err := BuildFrom(cfg, "claude", "go", nil, Vars{}, BuildOptions{Budget: b}); err == nil {
		t.Fatal("claude has no agent_budget mapping and must refuse")
	}
}

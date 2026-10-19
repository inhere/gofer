package agent

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/inhere/gofer/internal/config"
)

// AgentBudgetFragment renders a job's soft budget (config.Budget.AgentTokens /
// AgentCostUSD) into what the agent is started with: argv to append and env to set. It
// is the single place that applies agent_budget, shared by a fresh run and a resume.
//
// A cli-agent takes the argv fragment when a dimension declares one and the environment
// variable otherwise; an acp-agent prefers the variable (its launch argv is fixed, only
// the process environment is per job) and falls back to the fragment. A dimension that
// is set on the job but has no mapping on the agent is an error, never silently dropped.
// A budget without agent_* values renders nothing.
func AgentBudgetFragment(agentKey string, ac config.AgentConfig, b *config.Budget) (args []string, env map[string]string, err error) {
	if !b.HasAgent() {
		return nil, nil, nil
	}
	m := ac.AgentBudget
	preferEnv := ac.Type == TypeACPAgent
	type dim struct {
		set         bool
		supported   bool
		what        string
		value       string
		placeholder string
		argv        []string
		envName     string
	}
	dims := []dim{
		{b.AgentTokens > 0, m.SupportsTokens(), "tokens", strconv.FormatInt(b.AgentTokens, 10), "{{budget_tokens}}", nil, ""},
		{b.AgentCostUSD > 0, m.SupportsCost(), "cost_usd", strconv.FormatFloat(b.AgentCostUSD, 'f', -1, 64), "{{budget_cost_usd}}", nil, ""},
	}
	if m != nil {
		dims[0].argv, dims[0].envName = m.TokensArgs, m.TokensEnv
		dims[1].argv, dims[1].envName = m.CostArgs, m.CostEnv
	}
	for _, d := range dims {
		if !d.set {
			continue
		}
		if !d.supported {
			return nil, nil, fmt.Errorf("agent %q does not support budgets: no agent_budget mapping for %s (set agents.%s.agent_budget)", agentKey, d.what, agentKey)
		}
		useEnv := d.envName != "" && (preferEnv || len(d.argv) == 0)
		if useEnv {
			if env == nil {
				env = map[string]string{}
			}
			env[d.envName] = d.value
			continue
		}
		for _, a := range d.argv {
			args = append(args, strings.ReplaceAll(a, d.placeholder, d.value))
		}
	}
	return args, env, nil
}

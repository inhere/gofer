package config

import (
	"fmt"
	"math"
)

// Budget is a job's spend ceiling (N2 §B, GATE-02). Every field is optional and 0 means
// "no limit on this dimension"; an all-zero Budget is the same as none. It is the
// shared shape of the request (JobRequest.Budget), the config defaults
// (agents.<k>.budget / projects.<k>.budget), the task-book frontmatter and the worker
// dispatch frame, which is why it lives in the config package (the lowest layer all of
// them already import).
//
// The limits are enforced on the EXECUTING machine from the agent's own streamed
// accounting: MaxTokens against input+output+cache (the job usage total), MaxCostUSD
// only where the source reports a cost, MaxTurns against the number of model requests.
type Budget struct {
	MaxTokens  int64   `json:"max_tokens,omitempty" yaml:"max_tokens,omitempty"`
	MaxCostUSD float64 `json:"max_cost_usd,omitempty" yaml:"max_cost_usd,omitempty"`
	MaxTurns   int     `json:"max_turns,omitempty" yaml:"max_turns,omitempty"`
	// AgentTokens / AgentCostUSD are the SOFT budget handed to the agent itself (not
	// metered or enforced by gofer): the agent winds the run up on its own once it has
	// spent that much, so the report is still delivered. They are rendered into the
	// agent's argv through agents.<k>.agent_budget_args; an agent without that mapping
	// refuses a job that carries them. Keep them below the hard max_* limits — a hard
	// kill lands before the agent's own wrap-up.
	AgentTokens  int64   `json:"agent_tokens,omitempty" yaml:"agent_tokens,omitempty"`
	AgentCostUSD float64 `json:"agent_cost_usd,omitempty" yaml:"agent_cost_usd,omitempty"`
}

// IsZero reports whether no dimension is limited. Safe on a nil receiver.
func (b *Budget) IsZero() bool {
	return b == nil || (b.MaxTokens == 0 && b.MaxCostUSD == 0 && b.MaxTurns == 0 && b.AgentTokens == 0 && b.AgentCostUSD == 0)
}

// HasHard reports whether a gofer-enforced limit (max_*) is set. Safe on a nil receiver.
func (b *Budget) HasHard() bool {
	return b != nil && (b.MaxTokens != 0 || b.MaxCostUSD != 0 || b.MaxTurns != 0)
}

// HasAgent reports whether a soft budget for the agent itself (agent_*) is set. Safe on
// a nil receiver.
func (b *Budget) HasAgent() bool {
	return b != nil && (b.AgentTokens != 0 || b.AgentCostUSD != 0)
}

// WithoutAgent returns a copy of b with the agent_* soft budget removed (nil when
// nothing else is left). Safe on a nil receiver.
func (b *Budget) WithoutAgent() *Budget {
	if b == nil {
		return nil
	}
	c := *b
	c.AgentTokens, c.AgentCostUSD = 0, 0
	return c.Normalize()
}

// WithoutHard returns a copy of b with the max_* limits removed (nil when nothing else
// is left). Safe on a nil receiver.
func (b *Budget) WithoutHard() *Budget {
	if b == nil {
		return nil
	}
	c := *b
	c.MaxTokens, c.MaxCostUSD, c.MaxTurns = 0, 0, 0
	return c.Normalize()
}

// Normalize returns nil for an empty budget, else a copy, so "no budget" has exactly one
// representation (and request_json / wire frames stay byte-identical to before).
func (b *Budget) Normalize() *Budget {
	if b.IsZero() {
		return nil
	}
	c := *b
	return &c
}

// Validate rejects a negative, NaN or infinite limit (0 = unlimited is fine).
func (b *Budget) Validate() error {
	if b == nil {
		return nil
	}
	if b.MaxTokens < 0 {
		return fmt.Errorf("budget max_tokens must be >= 0")
	}
	if b.MaxTurns < 0 {
		return fmt.Errorf("budget max_turns must be >= 0")
	}
	if b.MaxCostUSD < 0 || math.IsNaN(b.MaxCostUSD) || math.IsInf(b.MaxCostUSD, 0) {
		return fmt.Errorf("budget max_cost_usd must be a finite number >= 0")
	}
	if b.AgentTokens < 0 {
		return fmt.Errorf("budget agent_tokens must be >= 0")
	}
	if b.AgentCostUSD < 0 || math.IsNaN(b.AgentCostUSD) || math.IsInf(b.AgentCostUSD, 0) {
		return fmt.Errorf("budget agent_cost_usd must be a finite number >= 0")
	}
	return nil
}

// Over returns b with every unset (0) dimension filled from def — the layered default
// rule (agent < project < request: the more specific layer wins per dimension). Either
// side may be nil; the result is nil when nothing is limited.
func (b *Budget) Over(def *Budget) *Budget {
	var out Budget
	if def != nil {
		out = *def
	}
	if b != nil {
		if b.MaxTokens != 0 {
			out.MaxTokens = b.MaxTokens
		}
		if b.MaxCostUSD != 0 {
			out.MaxCostUSD = b.MaxCostUSD
		}
		if b.MaxTurns != 0 {
			out.MaxTurns = b.MaxTurns
		}
		if b.AgentTokens != 0 {
			out.AgentTokens = b.AgentTokens
		}
		if b.AgentCostUSD != 0 {
			out.AgentCostUSD = b.AgentCostUSD
		}
	}
	return out.Normalize()
}

// EffectiveBudget resolves the job's budget: agent default < project default < the
// request's own, merged per dimension (see Budget.Over). nil = unlimited.
func (c *Config) EffectiveBudget(projectKey, agentKey string, requested *Budget) *Budget {
	var def *Budget
	if c != nil {
		if a, ok := c.Agents[agentKey]; ok {
			def = a.Budget
		}
		if p, ok := c.Projects[projectKey]; ok {
			def = p.Budget.Over(def)
		}
	}
	return requested.Over(def)
}

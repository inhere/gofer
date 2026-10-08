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
}

// IsZero reports whether no dimension is limited. Safe on a nil receiver.
func (b *Budget) IsZero() bool {
	return b == nil || (b.MaxTokens == 0 && b.MaxCostUSD == 0 && b.MaxTurns == 0)
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

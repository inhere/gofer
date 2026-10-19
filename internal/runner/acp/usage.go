package acp

import (
	"encoding/json"

	"github.com/inhere/gofer/internal/runner"
)

// usageFromUpdate reads one usage_update payload (SUP-01 E). ACP leaves the object's
// shape to the agent, so the shared reader takes the recognisable subset and reports
// nil for a payload it cannot read — a job row must never carry a guessed tally.
func usageFromUpdate(raw json.RawMessage) *runner.Usage {
	if len(raw) == 0 {
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	usage := runner.UsageFromObject(obj, runner.UsageSourceACP)
	meta, ok := obj["_meta"].(map[string]any)
	if !ok {
		return usage
	}
	usage = mergeMetaUsage(usage, meta)
	// The agent's own soft-budget snapshot rides _meta (budget_* keys); it is additive
	// to the token / cost tally and may arrive on an update that has no other usage.
	if budget := runner.AgentBudgetFromMeta(meta); budget != nil {
		if usage == nil {
			usage = &runner.Usage{Source: runner.UsageSourceACP}
		}
		usage.AgentBudget = budget
	}
	return usage
}

// mergeMetaUsage folds the billing counters of an update's _meta over its standard
// usage object (either side may be nil).
func mergeMetaUsage(usage *runner.Usage, meta map[string]any) *runner.Usage {
	metaUsage := runner.UsageFromObject(meta, runner.UsageSourceACP)
	if metaUsage == nil {
		return usage
	}
	if usage == nil {
		return metaUsage
	}
	// ACP's standard `used` value can describe context consumption. Only an
	// explicit billing total in _meta overrides it; do not let a derived sum of
	// metadata input/output counters replace that standard context count.
	if _, totalTokens := meta["totalTokens"]; !totalTokens {
		if _, totalTokens = meta["total_tokens"]; !totalTokens {
			if _, totalTokens = meta["total"]; !totalTokens {
				metaUsage.TotalTokens = 0
			}
		}
	}
	return overlayUsage(usage, metaUsage)
}

// overlayUsage merges a later update into the tally a job accumulates: every counter
// the update actually reported replaces the previous value and the ones it left out
// keep theirs. An agent reports a PARTIAL block per update (a cost-only update must
// not erase the token counts reported before it), which is why this is a merge and
// not an assignment. cur/next must be non-nil.
func overlayUsage(cur, next *runner.Usage) *runner.Usage {
	if cur == nil {
		return next
	}
	if next.InputTokens > 0 {
		cur.InputTokens = next.InputTokens
	}
	if next.OutputTokens > 0 {
		cur.OutputTokens = next.OutputTokens
	}
	if next.CacheReadTokens > 0 {
		cur.CacheReadTokens = next.CacheReadTokens
	}
	if next.CacheWriteTokens > 0 {
		cur.CacheWriteTokens = next.CacheWriteTokens
	}
	if next.TotalTokens > 0 {
		cur.TotalTokens = next.TotalTokens
	}
	if next.CostUSD > 0 {
		cur.CostUSD = next.CostUSD
	}
	if next.ToolCalls > 0 {
		cur.ToolCalls = next.ToolCalls
	}
	if next.AgentBudget != nil {
		b := *next.AgentBudget
		cur.AgentBudget = &b
	}
	return cur
}

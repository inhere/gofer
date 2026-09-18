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
	return runner.UsageFromObject(obj, runner.UsageSourceACP)
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
	return cur
}

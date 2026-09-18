package runner

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Usage is one job's token/cost accounting (SUP-01 E): what the agent that ran the
// job reported about the work, in ONE shape for every source. It lives here — not in
// the job package — because the acp runner and the ndjson capture produce it and the
// job package reads it (job imports runner, never the reverse: G022).
//
// Every field is best-effort: a zero counter means "the agent did not report this
// one" (these payloads are partial by nature), and CostUSD stays 0 for an agent with
// no cost notion (codex). Source names WHERE the numbers came from, so a reader can
// tell a provider's own accounting from a projected one.
type Usage struct {
	InputTokens      int64   `json:"input_tokens,omitempty"`
	OutputTokens     int64   `json:"output_tokens,omitempty"`
	CacheReadTokens  int64   `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64   `json:"cache_write_tokens,omitempty"`
	TotalTokens      int64   `json:"total_tokens,omitempty"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
	Source           string  `json:"source,omitempty"`
}

// The usage sources (Usage.Source): which capture produced the numbers. They are the
// vocabulary the job row, `job show` and the dashboard print.
const (
	// UsageSourceNDJSONOMP: the last completed assistant message of an `omp --mode json`
	// stream.
	UsageSourceNDJSONOMP = "ndjson:omp"
	// UsageSourceNDJSONClaude: the terminal `result` row of a `claude --output-format
	// stream-json` stream.
	UsageSourceNDJSONClaude = "ndjson:claude"
	// UsageSourceCodexStderr: the `tokens used` tail codex prints on stderr.
	UsageSourceCodexStderr = "codex:stderr"
	// UsageSourceACP: the `usage_update` session/updates an acp agent sends.
	UsageSourceACP = "acp:usage_update"
)

// UsageFromObject reads an agent's usage payload — its own JSON object — into the one
// shape gofer records. It is deliberately tolerant: ACP leaves the field names to the
// agent and the ndjson agents differ from each other, so every spelling seen in the
// wild is accepted (camelCase / snake_case / a numeric string), while a payload with
// nothing recognisable yields nil — a guess would put made-up numbers on a job row.
//
// Total is the agent's own total when it reports one, otherwise the sum of the
// counters it did report (claude's usage block carries no total field).
func UsageFromObject(obj map[string]any, source string) *Usage {
	if len(obj) == 0 {
		return nil
	}
	u := &Usage{Source: source}
	u.InputTokens = int64(usageNum(obj, "inputTokens", "input_tokens", "input"))
	u.OutputTokens = int64(usageNum(obj, "outputTokens", "output_tokens", "output"))
	u.CacheReadTokens = int64(usageNum(obj,
		"cacheReadTokens", "cache_read_tokens", "cacheReadInputTokens", "cache_read_input_tokens", "cacheRead", "cache_read"))
	u.CacheWriteTokens = int64(usageNum(obj,
		"cacheWriteTokens", "cache_write_tokens", "cacheCreationInputTokens", "cache_creation_input_tokens", "cacheWrite", "cache_write"))
	u.TotalTokens = int64(usageNum(obj, "totalTokens", "total_tokens", "total", "used"))
	if u.TotalTokens == 0 {
		u.TotalTokens = u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheWriteTokens
	}
	u.CostUSD = usageCost(obj)
	if *u == (Usage{Source: source}) {
		return nil // nothing recognisable: report none rather than a zero tally.
	}
	return u
}

// usageNum reads the first numeric field among keys (0 when none is present). JSON
// numbers arrive as float64; a string-encoded count is accepted too, which some
// agents emit for a counter they formatted themselves.
func usageNum(obj map[string]any, keys ...string) float64 {
	for _, k := range keys {
		v, ok := obj[k]
		if !ok {
			continue
		}
		switch n := v.(type) {
		case float64:
			return n
		case json.Number:
			if f, err := n.Float64(); err == nil {
				return f
			}
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
				return f
			}
		}
	}
	return 0
}

// usageCost reads a payload's cost: a number under one of the cost keys, or a nested
// cost block (omp reports cost.total; ACP agents report either shape).
func usageCost(obj map[string]any) float64 {
	if v := usageNum(obj, "cost_usd", "costUsd", "total_cost_usd", "totalCostUsd", "cost"); v != 0 {
		return v
	}
	if block, ok := obj["cost"].(map[string]any); ok {
		return usageNum(block, "total", "amount", "usd")
	}
	return 0
}

package runner

// ContextUsage is how full the agent's context window was at the end of a run, in the one
// shape gofer records. It is separate from the billing counters: those add up over a run,
// this is a level. Every figure is optional — an agent that does not report a window
// leaves it out and the background-agent rotation then falls back to counting runs; a
// window is never guessed from a model name.
type ContextUsage struct {
	// UsedTokens is the context size the agent reported or estimated; 0 = not reported.
	UsedTokens int64 `json:"used_tokens,omitempty"`
	// LimitTokens is the model's context window as the agent reported it; 0 = unknown.
	LimitTokens int64 `json:"limit_tokens,omitempty"`
	// Ratio is the agent's own used/limit fraction when it reports one (suga's
	// turn_end.context.ratio); 0 = not reported, see Fraction.
	Ratio float64 `json:"ratio,omitempty"`
	// Estimated marks a figure the agent (or gofer, from a billing counter) estimated.
	Estimated bool   `json:"estimated,omitempty"`
	Source    string `json:"source,omitempty"`
}

// Context sources (ContextUsage.Source).
const (
	ContextSourceTurnEnd = "ndjson:turn_end"  // suga's turn_end.context object
	ContextSourceUsage   = "ndjson:usage"     // context_* keys of a usage object / row
	ContextSourceClaude  = "ndjson:claude"    // result.modelUsage[model].contextWindow + its input counters
	ContextSourceACP     = "acp:usage_update" // usage_update used / size
)

// Fraction is the context fill in [0, +inf), and whether it is known: the agent's own
// ratio first, else used / limit when both are known. Unknown is not zero.
func (c *ContextUsage) Fraction() (float64, bool) {
	if c == nil {
		return 0, false
	}
	if c.Ratio > 0 {
		return c.Ratio, true
	}
	if c.LimitTokens > 0 && c.UsedTokens > 0 {
		return float64(c.UsedTokens) / float64(c.LimitTokens), true
	}
	return 0, false
}

// known reports whether the value carries anything worth recording.
func (c *ContextUsage) known() bool {
	return c != nil && (c.UsedTokens > 0 || c.LimitTokens > 0 || c.Ratio > 0)
}

// ContextFromLine reads the context level one NDJSON row carries, whatever the dialect:
//
//   - suga: `{"type":"turn_end","context":{"est_tokens":..,"window":..,"ratio":..}}`
//   - the common usage shape: `context_used_tokens` / `context_limit_tokens` on the row
//     itself or inside its `usage` object
//   - claude: a `result` row's `modelUsage.<model>.contextWindow` with that model's input
//     counters as the (estimated) fill
//
// nil when the row carries none.
func ContextFromLine(obj map[string]any) *ContextUsage {
	if block, ok := obj["context"].(map[string]any); ok {
		c := &ContextUsage{
			UsedTokens:  int64(usageNum(block, "est_tokens", "estTokens", "used_tokens", "used", "tokens")),
			LimitTokens: int64(usageNum(block, "window", "limit_tokens", "limit", "size")),
			Ratio:       usageNum(block, "ratio"),
			Source:      ContextSourceTurnEnd,
		}
		if c.known() {
			c.Estimated = c.UsedTokens > 0 && usageNum(block, "est_tokens", "estTokens") > 0
			return c
		}
	}
	if c := contextFromUsageKeys(obj); c != nil {
		return c
	}
	if u, ok := obj["usage"].(map[string]any); ok {
		if c := contextFromUsageKeys(u); c != nil {
			return c
		}
	}
	return contextFromClaudeResult(obj)
}

func contextFromUsageKeys(obj map[string]any) *ContextUsage {
	c := &ContextUsage{
		UsedTokens:  int64(usageNum(obj, "context_used_tokens", "contextUsedTokens")),
		LimitTokens: int64(usageNum(obj, "context_limit_tokens", "contextLimitTokens", "context_window", "contextWindow")),
		Source:      ContextSourceUsage,
	}
	if !c.known() {
		return nil
	}
	return c
}

// contextFromClaudeResult reads claude's terminal result row. modelUsage is cumulative
// over the run, so the fill is an estimate; with several models the one with the largest
// window is taken. A row without a reported window yields nil — claude's window is not
// guessed from the model name.
func contextFromClaudeResult(obj map[string]any) *ContextUsage {
	models, ok := obj["modelUsage"].(map[string]any)
	if !ok {
		return nil
	}
	var best *ContextUsage
	for _, raw := range models {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		window := int64(usageNum(m, "contextWindow", "context_window"))
		if window <= 0 {
			continue
		}
		used := int64(usageNum(m, "inputTokens", "input_tokens") +
			usageNum(m, "cacheReadInputTokens", "cache_read_input_tokens") +
			usageNum(m, "cacheCreationInputTokens", "cache_creation_input_tokens"))
		if best == nil || window > best.LimitTokens {
			best = &ContextUsage{UsedTokens: used, LimitTokens: window, Estimated: true, Source: ContextSourceClaude}
		}
	}
	return best
}

// ContextFromACPUpdate reads the standard fields of an ACP usage_update: `used` (tokens
// currently in context) and `size` (the context window).
func ContextFromACPUpdate(obj map[string]any) *ContextUsage {
	c := &ContextUsage{
		UsedTokens:  int64(usageNum(obj, "used")),
		LimitTokens: int64(usageNum(obj, "size")),
		Source:      ContextSourceACP,
	}
	if c.LimitTokens <= 0 {
		return nil // `used` alone is billing-shaped for some agents: only a window makes it a level.
	}
	return c
}

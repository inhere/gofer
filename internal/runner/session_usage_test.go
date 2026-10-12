package runner

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// gofer-2ldy: only tallies without a reported cost are priced; unknown models listed.
func TestSessionUsageEstimateCost(t *testing.T) {
	su := SessionUsage{ByModel: map[string]Usage{
		"claude-opus-5-5":  {InputTokens: 1_000_000, OutputTokens: 100_000},
		"claude-haiku-5-5": {InputTokens: 500, CostUSD: 0.5}, // reported: left alone
		"mystery-model":    {InputTokens: 10},
	}}
	price := func(m string) (config.ModelPrice, bool) {
		if m == "claude-opus-5-5" {
			return config.ModelPrice{Input: 10, Output: 50}, true
		}
		return config.ModelPrice{}, false
	}
	usd, unpriced := su.EstimateCost(price)
	if usd != 15 || len(unpriced) != 1 || unpriced[0] != "mystery-model" {
		t.Fatalf("estimate = %v %v", usd, unpriced)
	}
}

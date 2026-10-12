package config

import (
	"math"
	"testing"
)

func TestPriceForAndEstimate(t *testing.T) {
	c := &Config{Pricing: map[string]ModelPrice{
		"claude-opus-5-5": {Input: 10, Output: 50},
		"claude-*":        {Input: 1, Output: 5},
		"claude-sonnet-*": {Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75},
	}}
	if p, ok := c.PriceFor("Claude-Opus-5-5"); !ok || p.Input != 10 {
		t.Fatalf("exact: %+v %v", p, ok)
	}
	if p, ok := c.PriceFor("claude-sonnet-5-5"); !ok || p.Input != 3 {
		t.Fatalf("longest glob: %+v %v", p, ok)
	}
	if p, ok := c.PriceFor("claude-haiku-5-5"); !ok || p.Input != 1 {
		t.Fatalf("short glob: %+v %v", p, ok)
	}
	if _, ok := c.PriceFor("gpt-5"); ok {
		t.Fatal("unpriced model matched")
	}
	got := ModelPrice{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}.Estimate(1_000_000, 100_000, 2_000_000, 0)
	if math.Abs(got-(3+1.5+0.6)) > 1e-9 {
		t.Fatalf("estimate = %v", got)
	}
	if err := (&Config{Pricing: map[string]ModelPrice{"x": {Input: -1}}}).ValidatePricing(); err == nil {
		t.Fatal("negative price accepted")
	}
}

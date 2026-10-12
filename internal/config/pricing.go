package config

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// ModelPrice is a model's price in USD per million tokens (gofer-2ldy). It turns the
// token counts of a usage that carries no reported cost — terminal session usage, a
// job whose agent does not report one — into an estimate. Nothing is built in: prices
// change, so the operator writes the table for what they use.
type ModelPrice struct {
	Input      float64 `yaml:"input" json:"input"`
	Output     float64 `yaml:"output" json:"output"`
	CacheRead  float64 `yaml:"cache_read,omitempty" json:"cache_read,omitempty"`
	CacheWrite float64 `yaml:"cache_write,omitempty" json:"cache_write,omitempty"`
}

// Estimate prices token counts (USD).
func (p ModelPrice) Estimate(input, output, cacheRead, cacheWrite int64) float64 {
	return (float64(input)*p.Input + float64(output)*p.Output +
		float64(cacheRead)*p.CacheRead + float64(cacheWrite)*p.CacheWrite) / 1e6
}

// PriceFor finds model's price: an exact key first, else the longest matching glob
// pattern (`claude-sonnet-*`). Matching is case-insensitive.
func (c *Config) PriceFor(model string) (ModelPrice, bool) {
	if c == nil || len(c.Pricing) == 0 || strings.TrimSpace(model) == "" {
		return ModelPrice{}, false
	}
	m := strings.ToLower(strings.TrimSpace(model))
	for k, p := range c.Pricing {
		if strings.ToLower(k) == m {
			return p, true
		}
	}
	keys := make([]string, 0, len(c.Pricing))
	for k := range c.Pricing {
		if strings.ContainsAny(k, "*?[") {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		return len(keys[i]) > len(keys[j]) || len(keys[i]) == len(keys[j]) && keys[i] < keys[j]
	})
	for _, k := range keys {
		if ok, _ := path.Match(strings.ToLower(k), m); ok {
			return c.Pricing[k], true
		}
	}
	return ModelPrice{}, false
}

// ValidatePricing checks the price table.
func (c *Config) ValidatePricing() error {
	for k, p := range c.Pricing {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("pricing: empty model name")
		}
		if _, err := path.Match(strings.ToLower(k), ""); err != nil {
			return fmt.Errorf("pricing.%s: bad pattern: %v", k, err)
		}
		if p.Input < 0 || p.Output < 0 || p.CacheRead < 0 || p.CacheWrite < 0 {
			return fmt.Errorf("pricing.%s: prices must not be negative", k)
		}
	}
	return nil
}

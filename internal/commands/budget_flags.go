package commands

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/job"
)

// budgetFlags are the N2 §B spend-ceiling flags shared by `job run`, `job resume` and
// the plan todo commands. They stay strings / zero-valued when absent so "not given"
// (inherit the default / the source job's budget) is distinguishable from a number.
type budgetFlags struct {
	maxTokens string
	maxCost   string
	maxTurns  int
}

func (f *budgetFlags) bind(c *gcli.Command, category string) {
	c.StrOpt2(&f.maxTokens, "max-tokens", "kill the job once its token usage (input+output+cache) exceeds this; accepts 50000 / 50k / 1.5m (default: the agent's / project's budget, else unlimited)", jobRunOptCategory(category, ""))
	c.StrOpt2(&f.maxCost, "max-cost", "kill the job once its reported cost exceeds this many USD, e.g. 2.5 (only for agents that report a cost; default unlimited)", jobRunOptCategory(category, ""))
	c.IntOpt2(&f.maxTurns, "max-turns", "kill the job once it has made more than this many model requests (default unlimited)", jobRunOptCategory(category, 0))
}

// build returns the budget the flags describe, nil when none was given.
func (f *budgetFlags) build() (*job.Budget, error) {
	b := &job.Budget{MaxTurns: f.maxTurns}
	if s := strings.TrimSpace(f.maxTokens); s != "" {
		n, err := parseTokenCount(s)
		if err != nil {
			return nil, fmt.Errorf("--max-tokens: %w", err)
		}
		b.MaxTokens = n
	}
	if s := strings.TrimSpace(f.maxCost); s != "" {
		v, err := strconv.ParseFloat(strings.TrimPrefix(s, "$"), 64)
		if err != nil || v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("--max-cost must be a positive number of USD, got %q", f.maxCost)
		}
		b.MaxCostUSD = v
	}
	if b.MaxTurns < 0 {
		return nil, fmt.Errorf("--max-turns must be positive")
	}
	if err := job.CheckBudget(b); err != nil {
		return nil, err
	}
	return b.Normalize(), nil
}

// parseTokenCount reads 50000, 50k or 1.5m (case-insensitive) as a positive token count.
func parseTokenCount(s string) (int64, error) {
	mult := 1.0
	switch last := s[len(s)-1]; last {
	case 'k', 'K':
		mult, s = 1e3, s[:len(s)-1]
	case 'm', 'M':
		mult, s = 1e6, s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(s, "_", ""), 64)
	if err != nil || v <= 0 || math.IsNaN(v) || v*mult > float64(math.MaxInt64)/2 {
		return 0, fmt.Errorf("must be a positive token count (50000, 50k, 1.5m), got %q", s)
	}
	return int64(math.Round(v * mult)), nil
}

// formatBudget renders a job's ceiling for `job show`: "50k tokens / $2.00 / 20 turns".
func formatBudget(b *job.Budget) string {
	if b.IsZero() {
		return ""
	}
	var parts []string
	if b.MaxTokens > 0 {
		parts = append(parts, job.FormatTokens(b.MaxTokens)+" tokens")
	}
	if b.MaxCostUSD > 0 {
		parts = append(parts, "$"+strconv.FormatFloat(b.MaxCostUSD, 'f', -1, 64))
	}
	if b.MaxTurns > 0 {
		parts = append(parts, strconv.Itoa(b.MaxTurns)+" turns")
	}
	return strings.Join(parts, " / ")
}

// formatBudgetSpent renders what the job has used against its ceiling, e.g.
// "12.3k tokens / $0.1200 / 5 turns" — only the dimensions that are limited.
func formatBudgetSpent(b *job.Budget, u *job.Usage) string {
	if b.IsZero() || u == nil {
		return ""
	}
	var parts []string
	if b.MaxTokens > 0 && u.TotalTokens > 0 {
		parts = append(parts, job.FormatTokens(u.TotalTokens)+" tokens")
	}
	if b.MaxCostUSD > 0 && u.CostUSD > 0 {
		parts = append(parts, "$"+strconv.FormatFloat(u.CostUSD, 'f', 4, 64))
	}
	if b.MaxTurns > 0 && u.Turns > 0 {
		parts = append(parts, strconv.FormatInt(u.Turns, 10)+" turns")
	}
	return strings.Join(parts, " / ")
}

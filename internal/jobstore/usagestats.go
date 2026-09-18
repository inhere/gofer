package jobstore

import (
	"fmt"
	"time"
)

// UsageStats is the per-agent token/cost aggregate behind /v1/stats (SUP-01 E): one
// entry per computed window, keyed by the window's label ("24h", "7d"), plus the flag
// saying whether the budget ran out before every window was computed — the same
// degraded mode DBStats uses, because a dashboard poll must never stall on a big or
// cold db.
type UsageStats struct {
	Windows map[string]UsageWindow
	Partial bool
}

// UsageWindow is one reporting window of the aggregate: the per-agent tallies and
// their sum.
type UsageWindow struct {
	ByAgent map[string]UsageAgent
	Total   UsageAgent
}

// UsageAgent is one agent's tally inside a window. Jobs counts EVERY job the agent
// ran in the window — one that captured no usage still ran — while the token/cost
// sums only see the jobs that actually reported numbers.
type UsageAgent struct {
	Jobs         int
	TotalTokens  int64
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
}

// usageStatsQuery aggregates jobs.usage_json per agent inside one window. json_valid
// guards the extraction: a row with no usage (empty column) or a corrupt blob is
// simply not counted, instead of failing the whole query — SQLite's json functions
// raise a malformed-JSON error on a non-JSON operand, and one bad row must not blank
// the dashboard's usage card.
const usageStatsQuery = `SELECT agent, COUNT(*),
  COALESCE(SUM(CASE WHEN json_valid(usage_json) THEN json_extract(usage_json,'$.total_tokens') END),0),
  COALESCE(SUM(CASE WHEN json_valid(usage_json) THEN json_extract(usage_json,'$.input_tokens') END),0),
  COALESCE(SUM(CASE WHEN json_valid(usage_json) THEN json_extract(usage_json,'$.output_tokens') END),0),
  COALESCE(SUM(CASE WHEN json_valid(usage_json) THEN json_extract(usage_json,'$.cost_usd') END),0)
FROM jobs WHERE started_at >= ? GROUP BY agent`

// UsageStats aggregates what each agent reported in each window ending at now. budget
// caps the TOTAL pass (one query per window, each cheap, but the dashboard polls this
// every few seconds); when it is exhausted the remaining windows are left out of the
// map and Partial is set — a caller must read a MISSING window as "not computed",
// never as zero usage.
func (s *Store) UsageStats(now int64, windows []time.Duration, budget time.Duration) (UsageStats, error) {
	out := UsageStats{Windows: make(map[string]UsageWindow, len(windows))}
	start := time.Now()
	for _, w := range windows {
		if time.Since(start) >= budget {
			out.Partial = true
			break
		}
		win, err := s.usageWindow(now - int64(w/time.Second))
		if err != nil {
			return UsageStats{}, err
		}
		out.Windows[windowLabel(w)] = win
	}
	return out, nil
}

// windowLabel renders a reporting window the way the wire spells it: whole days from
// two days up ("7d"), hours below that ("24h").
func windowLabel(w time.Duration) string {
	hours := int(w.Hours())
	if hours >= 48 && hours%24 == 0 {
		return fmt.Sprintf("%dd", hours/24)
	}
	return fmt.Sprintf("%dh", hours)
}

// usageWindow runs the aggregation for one lower bound (unix seconds).
func (s *Store) usageWindow(since int64) (UsageWindow, error) {
	rows, err := s.db.Query(usageStatsQuery, since)
	if err != nil {
		return UsageWindow{}, fmt.Errorf("jobstore: usage by agent: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := UsageWindow{ByAgent: map[string]UsageAgent{}}
	for rows.Next() {
		var agent string
		var a UsageAgent
		if err := rows.Scan(&agent, &a.Jobs, &a.TotalTokens, &a.InputTokens, &a.OutputTokens, &a.CostUSD); err != nil {
			return UsageWindow{}, fmt.Errorf("jobstore: scan usage row: %w", err)
		}
		out.ByAgent[agent] = a
		out.Total.Jobs += a.Jobs
		out.Total.TotalTokens += a.TotalTokens
		out.Total.InputTokens += a.InputTokens
		out.Total.OutputTokens += a.OutputTokens
		out.Total.CostUSD += a.CostUSD
	}
	if err := rows.Err(); err != nil {
		return UsageWindow{}, fmt.Errorf("jobstore: usage by agent: %w", err)
	}
	return out, nil
}

package jobstore

import (
	"math"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

// TestUsageStatsAggregatesByAgentAndWindow: the /v1/stats usage block (SUP-01 E) sums
// the persisted usage per agent inside each window. A job that captured no usage still
// counts as a job (it ran, and the caller asked "how many runs"), a job outside a
// window is invisible to it, and a malformed usage blob neither fails the aggregation
// nor contributes numbers.
func TestUsageStatsAggregatesByAgentAndWindow(t *testing.T) {
	s := openTest(t)
	const now = int64(1_700_000_000)
	const day = int64(86400)

	put := func(id, agent string, started int64, usageJSON string) {
		t.Helper()
		rec := sampleJob(id, "proj", started)
		rec.Agent, rec.Status = agent, "done"
		rec.EndedAt, rec.UpdatedAt = started+1, started+1
		rec.UsageJSON = usageJSON
		assert.NoErr(t, s.UpsertJob(rec))
	}
	put("u-omp-1", "omp", now-day/2,
		`{"input_tokens":100,"output_tokens":200,"total_tokens":1000,"cost_usd":0.01,"source":"ndjson:omp"}`)
	put("u-omp-2", "omp", now-3600, `{"total_tokens":500,"cost_usd":0.005,"source":"ndjson:omp"}`)
	put("u-omp-old", "omp", now-3*day, `{"total_tokens":100,"source":"ndjson:omp"}`) // inside 7d, outside 24h
	put("u-omp-none", "omp", now-600, "")                                            // ran, reported no usage
	put("u-codex-1", "codex", now-7200, `{"total_tokens":2000,"cost_usd":0.02,"source":"codex:stderr"}`)
	put("u-ancient", "codex", now-30*day, `{"total_tokens":999999,"source":"codex:stderr"}`) // outside both
	put("u-broken", "omp", now-100, `{not json`)                                             // a corrupt row

	stats, err := s.UsageStats(now, []time.Duration{24 * time.Hour, 7 * 24 * time.Hour}, time.Minute)
	assert.NoErr(t, err)
	if stats.Partial {
		t.Fatalf("partial = true with a one-minute budget: %+v", stats)
	}
	if len(stats.Windows) != 2 {
		t.Fatalf("windows = %v, want 24h and 7d", stats.Windows)
	}

	d := stats.Windows["24h"]
	omp := d.ByAgent["omp"]
	if omp.Jobs != 4 || omp.TotalTokens != 1500 || omp.InputTokens != 100 || omp.OutputTokens != 200 {
		t.Fatalf("24h omp = %+v, want 4 jobs / 1500 total / 100 in / 200 out", omp)
	}
	if math.Abs(omp.CostUSD-0.015) > 1e-9 {
		t.Fatalf("24h omp cost = %v, want 0.015", omp.CostUSD)
	}
	if codex := d.ByAgent["codex"]; codex.Jobs != 1 || codex.TotalTokens != 2000 {
		t.Fatalf("24h codex = %+v, want 1 job / 2000 tokens", codex)
	}
	if d.Total.Jobs != 5 || d.Total.TotalTokens != 3500 {
		t.Fatalf("24h total = %+v, want 5 jobs / 3500 tokens", d.Total)
	}
	if math.Abs(d.Total.CostUSD-0.035) > 1e-9 {
		t.Fatalf("24h total cost = %v, want 0.035", d.Total.CostUSD)
	}

	w := stats.Windows["7d"]
	if got := w.ByAgent["omp"]; got.Jobs != 5 || got.TotalTokens != 1600 {
		t.Fatalf("7d omp = %+v, want the 3-day-old job included (5 jobs / 1600 tokens)", got)
	}
	if got := w.ByAgent["codex"]; got.Jobs != 1 || got.TotalTokens != 2000 {
		t.Fatalf("7d codex = %+v, want only the in-window job", got)
	}
	if w.Total.Jobs != 6 || w.Total.TotalTokens != 3600 {
		t.Fatalf("7d total = %+v, want 6 jobs / 3600 tokens", w.Total)
	}
}

// TestUsageStatsPartialOnBudget: the aggregate honours the /v1/stats budget — an
// exhausted budget reports partial with the windows it did NOT compute left out
// (rather than reported as zeros), and a healthy budget computes them all.
func TestUsageStatsPartialOnBudget(t *testing.T) {
	s := openTest(t)
	const now = int64(1_700_000_000)
	windows := []time.Duration{24 * time.Hour, 7 * 24 * time.Hour}

	rec := sampleJob("u1", "proj", now-600)
	rec.Agent = "omp"
	rec.UsageJSON = `{"total_tokens":42,"source":"ndjson:omp"}`
	assert.NoErr(t, s.UpsertJob(rec))

	stats, err := s.UsageStats(now, windows, 0)
	assert.NoErr(t, err)
	if !stats.Partial {
		t.Fatalf("partial = false with a zero budget: %+v", stats)
	}
	if len(stats.Windows) != 0 {
		t.Fatalf("windows = %v, want none computed under an exhausted budget", stats.Windows)
	}

	stats, err = s.UsageStats(now, windows, time.Minute)
	assert.NoErr(t, err)
	if stats.Partial {
		t.Fatalf("partial = true with a one-minute budget: %+v", stats)
	}
	if len(stats.Windows) != 2 {
		t.Fatalf("windows = %v, want both computed", stats.Windows)
	}
	if got := stats.Windows["24h"]; got.Total.Jobs != 1 || got.Total.TotalTokens != 42 {
		t.Fatalf("24h total = %+v, want the one job's tokens", got.Total)
	}
}

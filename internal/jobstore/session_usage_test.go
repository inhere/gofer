package jobstore

import (
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/runner"
)

func usageOf(in, out int64) runner.Usage {
	return runner.Usage{InputTokens: in, OutputTokens: out, TotalTokens: in + out}
}

func TestAddSessionUsageAccumulatesAndTalliesDaily(t *testing.T) {
	s := openTest(t)
	cur := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return cur })
	_, err := s.UpsertAgentSession(AgentSession{SessionID: "su1", Agent: "claude", ProjectKey: "p1"})
	assert.NoErr(t, err)
	_, err = s.UpsertAgentSession(AgentSession{SessionID: "su2", Agent: "codex", ProjectKey: "p1"})
	assert.NoErr(t, err)

	ok, err := s.AddSessionUsage("nope", runner.SessionUsage{Main: usageOf(1, 1)})
	assert.NoErr(t, err)
	assert.False(t, ok)

	d1 := runner.SessionUsage{Main: usageOf(10, 5), Sub: usageOf(3, 2),
		ByModel: map[string]runner.Usage{"opus": usageOf(10, 5), "sonnet": usageOf(3, 2)}}
	ok, err = s.AddSessionUsage("su1", d1)
	assert.NoErr(t, err)
	assert.True(t, ok)
	_, err = s.AddSessionUsage("su1", runner.SessionUsage{Main: usageOf(1, 1), ByModel: map[string]runner.Usage{"opus": usageOf(1, 1)}})
	assert.NoErr(t, err)

	a, _, err := s.GetAgentSession("su1")
	assert.NoErr(t, err)
	got := ParseSessionUsage(a.UsageJSON)
	assert.Eq(t, int64(11), got.Main.InputTokens)
	assert.Eq(t, int64(6), got.Main.OutputTokens)
	assert.Eq(t, int64(17), got.Main.TotalTokens)
	assert.Eq(t, int64(5), got.Sub.TotalTokens)
	assert.Eq(t, int64(17), got.ByModel["opus"].TotalTokens)
	assert.Eq(t, int64(22), got.Total().TotalTokens)

	// the next day, another agent's session
	cur = cur.Add(24 * time.Hour)
	_, err = s.AddSessionUsage("su2", runner.SessionUsage{Main: usageOf(100, 50)}) // no model split
	assert.NoErr(t, err)

	st, err := s.SessionUsageStats(cur.Unix(), []time.Duration{24 * time.Hour, 7 * 24 * time.Hour})
	assert.NoErr(t, err)
	// "24h" from 2026-10-10 10:00 reaches back to the 9th's bucket (whole-day tally)
	w24 := st.Windows["24h"]
	assert.Eq(t, 2, w24.Sessions)
	assert.Eq(t, int64(172), w24.Total.TotalTokens)
	assert.Eq(t, int64(150), w24.ByAgent["codex"].TotalTokens)
	assert.Eq(t, int64(22), w24.ByAgent["claude"].TotalTokens)
	assert.Eq(t, 2, st.Windows["7d"].Sessions)

	// a month later nothing is inside the windows any more
	st, err = s.SessionUsageStats(cur.Add(30*24*time.Hour).Unix(), []time.Duration{24 * time.Hour, 7 * 24 * time.Hour})
	assert.NoErr(t, err)
	assert.Eq(t, 0, st.Windows["7d"].Sessions)
	assert.Eq(t, int64(0), st.Windows["7d"].Total.TotalTokens)
}

func TestParseSessionUsageTolerant(t *testing.T) {
	assert.True(t, ParseSessionUsage("").Empty())
	assert.True(t, ParseSessionUsage("{broken").Empty())
}

package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestPlanUsageCacheAndCarrierAttribution: the roll-up carries cache read / write
// totals (overall and per agent), and a cli resume carrier (agent column `exec` with a
// resume_agent) is booked on its real agent, not on `exec`.
func TestPlanUsageCacheAndCarrierAttribution(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-cache", Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))

	for _, rec := range []JobRecord{
		{
			ID: "c-claude-1", ProjectKey: "proj", Agent: "claude", Runner: "local", Status: "done",
			ResultDir: "/tmp/results/c-claude-1", PlanID: "plan-cache", StartedAt: 1,
			UsageJSON: `{"total_tokens":1000,"cache_read_tokens":300,"cache_write_tokens":50,"cost_usd":0.5}`,
		},
		{
			// A resume carrier: ran under `exec`, but the session belongs to claude.
			ID: "c-carrier", ProjectKey: "proj", Agent: "exec", ResumeAgent: "claude", Runner: "local", Status: "done",
			ResultDir: "/tmp/results/c-carrier", PlanID: "plan-cache", StartedAt: 2,
			UsageJSON: `{"total_tokens":400,"cache_read_tokens":200,"cache_write_tokens":10,"cost_usd":0.1}`,
		},
		{
			// A plain exec job has no resume_agent and stays on `exec`.
			ID: "c-exec", ProjectKey: "proj", Agent: "exec", Runner: "local", Status: "done",
			ResultDir: "/tmp/results/c-exec", PlanID: "plan-cache", StartedAt: 3,
		},
		{
			ID: "c-codex", ProjectKey: "proj", Agent: "codex", Runner: "local", Status: "done",
			ResultDir: "/tmp/results/c-codex", PlanID: "plan-cache", StartedAt: 4,
			UsageJSON: `{"total_tokens":100,"cache_read_tokens":7}`,
		},
	} {
		assert.NoErr(t, s.UpsertJob(rec))
	}

	u, err := s.PlanUsage("plan-cache")
	assert.NoErr(t, err)
	assert.Eq(t, 4, u.Jobs)
	assert.Eq(t, int64(1500), u.TotalTokens)
	assert.Eq(t, int64(507), u.CacheReadTokens)
	assert.Eq(t, int64(60), u.CacheWriteTokens)

	claude := u.ByAgent["claude"]
	assert.Eq(t, 2, claude.Jobs)
	assert.Eq(t, int64(1400), claude.TotalTokens)
	assert.Eq(t, int64(500), claude.CacheReadTokens)
	assert.Eq(t, int64(60), claude.CacheWriteTokens)
	assert.Eq(t, 1, u.ByAgent["exec"].Jobs)
	assert.Eq(t, int64(7), u.ByAgent["codex"].CacheReadTokens)
}

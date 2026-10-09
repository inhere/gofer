package jobstore

import (
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestJobMetricsMigrationAdditive: a database opened before gofer-yelm P2 (no
// job_metrics table, no idx_jobs_ended) gains both on the next Open, the jobs rows are
// untouched, and re-opening again is a no-op.
func TestJobMetricsMigrationAdditive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := Open(path)
	assert.NoErr(t, err)
	rec := sampleJob("m-old", "p", 100)
	rec.Status, rec.EndedAt = "done", 200
	assert.NoErr(t, s.UpsertJob(rec))
	_, err = s.db.Exec(`DROP TABLE job_metrics`)
	assert.NoErr(t, err)
	_, err = s.db.Exec(`DROP INDEX idx_jobs_ended`)
	assert.NoErr(t, err)
	assert.False(t, indexExists(t, s, "idx_jobs_ended"))
	assert.NoErr(t, s.Close())

	for i := 0; i < 2; i++ {
		s, err = Open(path)
		assert.NoErr(t, err)
		assert.True(t, indexExists(t, s, "idx_jobs_ended"))
		assert.True(t, tableHasColumn(t, s, "job_metrics", "tool_calls"))
		assert.True(t, tableHasColumn(t, s, "job_metrics", "version"))
		got, ok, err := s.GetJob("m-old")
		assert.NoErr(t, err)
		assert.True(t, ok)
		assert.Eq(t, int64(200), got.EndedAt)
		assert.NoErr(t, s.Close())
	}
}

// TestJobMetricsRoundTripKeepsNulls: a nil metric is stored as NULL and read back as
// nil (「—」 on the page), never as 0; an upsert replaces the row.
func TestJobMetricsRoundTripKeepsNulls(t *testing.T) {
	s := openTest(t)
	turns, cost := int64(4), 0.25
	assert.NoErr(t, s.UpsertJobMetrics(JobMetrics{JobID: "j1", Model: "m-1", Turns: &turns, CostUSD: &cost}))
	got, ok, err := s.GetJobMetrics("j1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "m-1", got.Model)
	assert.Eq(t, int64(4), *got.Turns)
	assert.Nil(t, got.ToolCalls)
	assert.Nil(t, got.FilesChanged)
	assert.Eq(t, 0.25, *got.CostUSD)
	assert.Eq(t, JobMetricsVersion, got.Version)
	assert.True(t, got.ComputedAt > 0)

	tools := int64(9)
	assert.NoErr(t, s.UpsertJobMetrics(JobMetrics{JobID: "j1", ToolCalls: &tools}))
	got, _, err = s.GetJobMetrics("j1")
	assert.NoErr(t, err)
	assert.Nil(t, got.Turns)
	assert.Eq(t, int64(9), *got.ToolCalls)
	assert.Eq(t, "", got.Model)

	_, ok, err = s.GetJobMetrics("missing")
	assert.NoErr(t, err)
	assert.False(t, ok)
}

// TestStatsGenMovesOnEndsOnly: the overview cache's invalidation counter moves when a
// job ends (terminal or needs_review), when metrics or session usage land, and on bulk
// job writes — not on every running-state upsert.
func TestStatsGenMovesOnEndsOnly(t *testing.T) {
	s := openTest(t)
	g0 := s.StatsGen()
	rec := sampleJob("g1", "p", 100)
	rec.Status = "running"
	assert.NoErr(t, s.UpsertJob(rec))
	assert.Eq(t, g0, s.StatsGen())

	rec.Status, rec.EndedAt = "needs_review", 150
	assert.NoErr(t, s.UpsertJob(rec))
	g1 := s.StatsGen()
	assert.True(t, g1 > g0)

	rec.Status = "done"
	assert.NoErr(t, s.UpsertJob(rec))
	g2 := s.StatsGen()
	assert.True(t, g2 > g1)

	assert.NoErr(t, s.UpsertJobMetrics(JobMetrics{JobID: "g1"}))
	assert.True(t, s.StatsGen() > g2)
}

// TestDeriveEvidence pins the folding rules: acp summaries are turns + tool calls; a
// session's follow-up prompts are human input and the gaps before them are waits;
// replayed recovery events do not count twice; an L0 auto answer is neither a human
// nor a wait; an agent (driver) answer is a wait but not a human.
func TestDeriveEvidence(t *testing.T) {
	events := []metricsEvent{
		{Type: "job.acp_summary", Detail: `{"tool_calls":3}`, At: 10},
		{Type: evTurnStarted, Detail: `{"turn_no":1}`, At: 5},
		{Type: evTurnEnded, Detail: `{"turn_no":1}`, At: 20},
		{Type: evAwaitingInput, Detail: `{"turn_no":1}`, At: 20},
		{Type: evTurnStarted, Detail: `{"turn_no":2}`, At: 80},
		{Type: "job.acp_summary", Detail: `{"tool_calls":2}`, At: 90},
		{Type: evTurnEnded, Detail: `{"turn_no":2}`, At: 95},
		{Type: evTurnEnded, Detail: `{"turn_no":2,"replayed":true}`, At: 96},
		{Type: evAwaitingInput, Detail: `{"turn_no":2}`, At: 96},
	}
	inters := []metricsInteraction{
		{CreatedAt: 30, AnsweredAt: 50, AnsweredBy: "human"},
		{CreatedAt: 60, AnsweredAt: 61, AnsweredBy: "auto:choice"},
		{CreatedAt: 70, AnsweredAt: 75, AnsweredBy: "agent:sup-1"},
		{CreatedAt: 76, AnsweredAt: 0, AnsweredBy: ""}, // never answered
		{CreatedAt: 77, AnsweredAt: 78, AnsweredBy: "push"},
	}
	ev := deriveEvidence(events, inters)
	assert.Eq(t, int64(2), ev.ACPTurns)
	assert.Eq(t, int64(5), ev.ACPToolCalls)
	assert.Eq(t, int64(2), ev.SessionTurns)
	assert.Eq(t, int64(1), ev.SessionSays)
	assert.Eq(t, int64(60), ev.SessionIdleSec) // 20 -> 80; the trailing wait is not a gap
	assert.Eq(t, int64(2), ev.HumanAnswers)
	assert.Eq(t, int64(20+5+1), ev.InteractionWaitSec)
}

// TestJobMetricsEvidenceReadsStore: the store read selects the right events and
// interactions of the job and nothing of another job.
func TestJobMetricsEvidenceReadsStore(t *testing.T) {
	s := openTest(t)
	_, err := s.InsertJobEvent(JobEvent{JobID: "e1", Type: "job.acp_summary", Detail: `{"tool_calls":4}`, At: 10})
	assert.NoErr(t, err)
	_, err = s.InsertJobEvent(JobEvent{JobID: "e1", Type: "job.terminal", Detail: `{"status":"done"}`, At: 11})
	assert.NoErr(t, err)
	_, err = s.InsertJobEvent(JobEvent{JobID: "e2", Type: "job.acp_summary", Detail: `{"tool_calls":99}`, At: 10})
	assert.NoErr(t, err)
	assert.NoErr(t, s.UpsertInteraction(InteractionRecord{ID: "i1", JobID: "e1", Type: "choice", Prompt: "?",
		Status: "answered", CreatedAt: 100, AnsweredAt: 130, AnsweredBy: "human"}))
	ev, err := s.JobMetricsEvidence("e1")
	assert.NoErr(t, err)
	assert.Eq(t, int64(1), ev.ACPTurns)
	assert.Eq(t, int64(4), ev.ACPToolCalls)
	assert.Eq(t, int64(1), ev.HumanAnswers)
	assert.Eq(t, int64(30), ev.InteractionWaitSec)
}

// TestListJobsForMetricsSkipsCurrentRows: the backfill walk only visits ended jobs,
// pages by (ended_at, id), and — unless forced — skips jobs whose metrics row is
// already at the current version (what makes a re-run a no-op).
func TestListJobsForMetricsSkipsCurrentRows(t *testing.T) {
	s := openTest(t)
	put := func(id, status string, ended int64) {
		rec := sampleJob(id, "p", 10)
		rec.Status, rec.EndedAt = status, ended
		assert.NoErr(t, s.UpsertJob(rec))
	}
	put("a", "done", 100)
	put("b", "failed", 100)
	put("c", "done", 200)
	put("r", "running", 0)
	put("old", "done", 50)

	ids := func(recs []JobRecord) []string {
		out := []string{}
		for _, r := range recs {
			out = append(out, r.ID)
		}
		return out
	}
	recs, err := s.ListJobsForMetrics(60, 0, "", 10, true)
	assert.NoErr(t, err)
	assert.Eq(t, []string{"a", "b", "c"}, ids(recs))

	recs, err = s.ListJobsForMetrics(60, 100, "a", 10, true)
	assert.NoErr(t, err)
	assert.Eq(t, []string{"b", "c"}, ids(recs))

	assert.NoErr(t, s.UpsertJobMetrics(JobMetrics{JobID: "b"}))
	recs, err = s.ListJobsForMetrics(60, 0, "", 10, true)
	assert.NoErr(t, err)
	assert.Eq(t, []string{"a", "c"}, ids(recs))
	recs, err = s.ListJobsForMetrics(60, 0, "", 10, false)
	assert.NoErr(t, err)
	assert.Eq(t, []string{"a", "b", "c"}, ids(recs))
}

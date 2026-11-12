package jobstore

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestPlanCompleteOnFieldMigration: a plans table that predates complete_on gains the
// column on Open, an old row reads back `job` (its todos kept completing with the job),
// and the field round-trips without moving anything else; invalid values are refused.
func TestPlanCompleteOnFieldMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-plans-complete-on.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	assert.NoErr(t, err)
	_, err = raw.Exec(`CREATE TABLE plans (
	  plan_id      TEXT PRIMARY KEY,
	  title        TEXT,
	  description  TEXT,
	  status       TEXT NOT NULL,
	  owner        TEXT,
	  progress     INTEGER NOT NULL DEFAULT 0,
	  project_key  TEXT,
	  paused       INTEGER NOT NULL DEFAULT 0,
	  blocked_todo TEXT,
	  leader       TEXT NOT NULL DEFAULT 'off',
	  notify       TEXT NOT NULL DEFAULT 'off',
	  supervisor_session_id TEXT,
	  created_at   INTEGER NOT NULL,
	  updated_at   INTEGER NOT NULL
	)`)
	assert.NoErr(t, err)
	_, err = raw.Exec(`INSERT INTO plans (plan_id, status, created_at, updated_at) VALUES ('plan-ancient-co', 'open', 7, 7)`)
	assert.NoErr(t, err)
	assert.NoErr(t, raw.Close())

	s, err := Open(path)
	assert.NoErr(t, err)
	defer s.Close()

	assert.True(t, tableHasColumn(t, s, "plans", "complete_on"))
	old, ok, err := s.GetPlan("plan-ancient-co")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, PlanCompleteOnJob, old.CompleteOn)
	assert.False(t, old.MergeCompletion())

	assert.NoErr(t, s.SetPlanCompleteOn("plan-ancient-co", PlanCompleteOnMerge))
	got, _, err := s.GetPlan("plan-ancient-co")
	assert.NoErr(t, err)
	assert.Eq(t, PlanCompleteOnMerge, got.CompleteOn)
	assert.True(t, got.MergeCompletion())
	assert.Eq(t, PlanOpen, got.Status)

	assert.Err(t, s.SetPlanCompleteOn("plan-ancient-co", "later"))
	assert.Err(t, s.SetPlanCompleteOn("plan-not-here", PlanCompleteOnMerge))
	assert.Err(t, s.InsertPlan(Plan{PlanID: "plan-bad-co-1", CompleteOn: "later", CreatedAt: 1, UpdatedAt: 1}))
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-def-co-1", CreatedAt: 1, UpdatedAt: 1}))
	def, _, _ := s.GetPlan("plan-def-co-1")
	assert.Eq(t, PlanCompleteOnJob, def.CompleteOn)
}

package jobstore

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestFreshOpenHasPlansTableAndPlanIDColumn(t *testing.T) {
	s := openTest(t)
	assert.True(t, tableExists(t, s, "plans"))
	assert.True(t, indexExists(t, s, "idx_plans_status"))
	assert.True(t, tableHasColumn(t, s, "jobs", "plan_id"))
	assert.True(t, indexExists(t, s, "idx_jobs_plan_id"))
}

func TestPlanInsertGetListStatusAndAttach(t *testing.T) {
	s := openTest(t)
	p := Plan{
		PlanID: "plan-1", Title: "phase", Description: "desc",
		Status: PlanOpen, Owner: "alice", CreatedAt: 100, UpdatedAt: 100,
	}
	assert.NoErr(t, s.InsertPlan(p))

	got, ok, err := s.GetPlan("plan-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "phase", got.Title)
	assert.Eq(t, "desc", got.Description)
	assert.Eq(t, "alice", got.Owner)
	assert.Eq(t, PlanOpen, got.Status)

	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-2", Status: PlanActive, CreatedAt: 200, UpdatedAt: 200}))
	open, err := s.ListPlans(PlanOpen, 0)
	assert.NoErr(t, err)
	assert.Len(t, open, 1)
	assert.Eq(t, "plan-1", open[0].PlanID)
	all, err := s.ListPlans("", 0)
	assert.NoErr(t, err)
	assert.Len(t, all, 2)
	assert.Eq(t, "plan-2", all[0].PlanID)

	assert.NoErr(t, s.SetPlanStatus("plan-1", PlanDone, 80))
	got, _, err = s.GetPlan("plan-1")
	assert.NoErr(t, err)
	assert.Eq(t, PlanDone, got.Status)
	assert.Eq(t, 80, got.Progress)

	j := sampleJob("job-1", "alpha", 300)
	assert.NoErr(t, s.UpsertJob(j))
	attached, err := s.AttachJobToPlan("job-1", "plan-1")
	assert.NoErr(t, err)
	assert.True(t, attached)
	attached, err = s.AttachJobToPlan("missing", "plan-1")
	assert.NoErr(t, err)
	assert.False(t, attached)

	jobs, err := s.ListJobs(ListQuery{Plan: "plan-1"})
	assert.NoErr(t, err)
	assert.Len(t, jobs, 1)
	assert.Eq(t, "job-1", jobs[0].ID)
	assert.Eq(t, "plan-1", jobs[0].PlanID)
}

func TestInsertPlanRequiresID(t *testing.T) {
	s := openTest(t)
	assert.Err(t, s.InsertPlan(Plan{Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))
}

func TestMigrateAddsPlanSupportToOldDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	raw, err := sql.Open("sqlite", "file:"+path)
	assert.NoErr(t, err)
	_, err = raw.Exec(`CREATE TABLE jobs (
	  id           TEXT PRIMARY KEY,
	  project_key  TEXT NOT NULL,
	  agent        TEXT NOT NULL,
	  runner       TEXT NOT NULL,
	  worker_id    TEXT,
	  status       TEXT NOT NULL,
	  exit_code    INTEGER NOT NULL DEFAULT 0,
	  cwd          TEXT,
	  result_dir   TEXT NOT NULL,
	  request_json TEXT,
	  error        TEXT,
	  started_at   INTEGER NOT NULL,
	  ended_at     INTEGER,
	  updated_at   INTEGER NOT NULL
	)`)
	assert.NoErr(t, err)
	_, err = raw.Exec(`INSERT INTO jobs (id, project_key, agent, runner, status, result_dir, started_at, updated_at)
	  VALUES ('old-1','p','exec','local','done','/tmp/r/old-1', 10, 10)`)
	assert.NoErr(t, err)
	assert.NoErr(t, raw.Close())

	s, err := Open(path)
	assert.NoErr(t, err)
	defer s.Close()

	assert.True(t, tableHasColumn(t, s, "jobs", "plan_id"))
	assert.True(t, tableExists(t, s, "plans"))
	assert.True(t, indexExists(t, s, "idx_jobs_plan_id"))

	got, ok, err := s.GetJob("old-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", got.PlanID)

	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-old", Status: PlanOpen, CreatedAt: 20, UpdatedAt: 20}))
	attached, err := s.AttachJobToPlan("old-1", "plan-old")
	assert.NoErr(t, err)
	assert.True(t, attached)
	jobs, err := s.ListJobs(ListQuery{Plan: "plan-old"})
	assert.NoErr(t, err)
	assert.Len(t, jobs, 1)
}

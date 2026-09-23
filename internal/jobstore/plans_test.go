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
	assert.True(t, tableExists(t, s, "plan_todos"))
	assert.True(t, indexExists(t, s, "idx_plan_todos_plan"))
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

func TestPlanJobStatusCountsAndRollup(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-counts", Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-empty", Status: PlanOpen, CreatedAt: 2, UpdatedAt: 2}))

	statuses := []string{"queued", "running", "pending_interaction", "done", "failed", "timeout", "cancelled"}
	for i, st := range statuses {
		j := withStatus(sampleJob("job-"+st, "alpha", int64(100+i)), st)
		j.PlanID = "plan-counts"
		assert.NoErr(t, s.UpsertJob(j))
	}
	other := withStatus(sampleJob("job-other-plan", "alpha", 999), "done")
	other.PlanID = "other-plan"
	assert.NoErr(t, s.UpsertJob(other))

	raw, err := s.PlanJobStatusCounts("plan-counts")
	assert.NoErr(t, err)
	assert.Eq(t, map[string]int{
		"queued": 1, "running": 1, "pending_interaction": 1, "done": 1,
		"failed": 1, "timeout": 1, "cancelled": 1,
	}, raw)

	assert.Eq(t, PlanCounts{Total: 7, Queued: 1, Running: 2, Done: 1, Failed: 3}, RollupPlanCounts(raw))

	empty, err := s.PlanJobStatusCounts("plan-empty")
	assert.NoErr(t, err)
	assert.NotNil(t, empty)
	assert.Len(t, empty, 0)
	assert.Eq(t, PlanCounts{}, RollupPlanCounts(empty))
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
	assert.True(t, tableExists(t, s, "plan_todos"))
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

// TestListPlansFilterAndPaging pins the plan list's filter/paging contract (F-d):
// status, project and q (plan-id PREFIX or title substring, case-insensitive) combine,
// limit/offset walk one stable newest-first order, and CountPlans answers the total
// under exactly the same conditions — never the page size.
func TestListPlansFilterAndPaging(t *testing.T) {
	s := openTest(t)
	for _, p := range []Plan{
		{PlanID: "plan-a1", Title: "Alpha rollout", Status: PlanOpen, ProjectKey: "self", CreatedAt: 100},
		{PlanID: "plan-a2", Title: "beta cleanup", Status: PlanOpen, ProjectKey: "other", CreatedAt: 200},
		{PlanID: "plan-b1", Title: "ALPHA follow-up", Status: PlanDone, ProjectKey: "self", CreatedAt: 300},
		{PlanID: "plan-b2", Title: "gamma 50% done", Status: PlanOpen, ProjectKey: "self", CreatedAt: 400},
		{PlanID: "plan-c1", Title: "delta", Status: PlanArchived, ProjectKey: "self", CreatedAt: 500},
	} {
		p.UpdatedAt = p.CreatedAt
		assert.NoErr(t, s.InsertPlan(p))
	}

	// Newest first, no filter.
	all, err := s.ListPlans(PlanFilter{})
	assert.NoErr(t, err)
	assert.Len(t, all, 5)
	assert.Eq(t, "plan-c1", all[0].PlanID)
	assert.Eq(t, "plan-a1", all[4].PlanID)

	// status and project combine (AND).
	open, err := s.ListPlans(PlanFilter{Status: PlanOpen})
	assert.NoErr(t, err)
	assert.Len(t, open, 3)
	self, err := s.ListPlans(PlanFilter{Status: PlanOpen, ProjectKey: "self"})
	assert.NoErr(t, err)
	assert.Len(t, self, 2)
	assert.Eq(t, "plan-b2", self[0].PlanID)

	// q matches a plan-id PREFIX or a title substring, case-insensitively.
	byPrefix, err := s.ListPlans(PlanFilter{Q: "plan-a"})
	assert.NoErr(t, err)
	assert.Len(t, byPrefix, 2)
	byTitle, err := s.ListPlans(PlanFilter{Q: "alpha"})
	assert.NoErr(t, err)
	assert.Len(t, byTitle, 2) // "Alpha rollout" + "ALPHA follow-up"
	assert.Eq(t, "plan-b1", byTitle[0].PlanID)
	// An id fragment that is not a prefix matches nothing (it is not a substring search
	// on ids) — the two kinds of match are deliberately different.
	mid, err := s.ListPlans(PlanFilter{Q: "a1"})
	assert.NoErr(t, err)
	assert.Len(t, mid, 0)
	// LIKE wildcards in the query are literals: "%" must not match every plan.
	pct, err := s.ListPlans(PlanFilter{Q: "50%"})
	assert.NoErr(t, err)
	assert.Len(t, pct, 1)
	assert.Eq(t, "plan-b2", pct[0].PlanID)

	// Paging: one stable order across pages, offset past the end is an empty page.
	page1, err := s.ListPlans(PlanFilter{Status: PlanOpen, Limit: 2})
	assert.NoErr(t, err)
	assert.Len(t, page1, 2)
	assert.Eq(t, "plan-b2", page1[0].PlanID)
	assert.Eq(t, "plan-a2", page1[1].PlanID)
	page2, err := s.ListPlans(PlanFilter{Status: PlanOpen, Limit: 2, Offset: 2})
	assert.NoErr(t, err)
	assert.Len(t, page2, 1)
	assert.Eq(t, "plan-a1", page2[0].PlanID)
	empty, err := s.ListPlans(PlanFilter{Status: PlanOpen, Offset: 99})
	assert.NoErr(t, err)
	assert.Len(t, empty, 0)

	// total counts the FILTER, not the page.
	total, err := s.CountPlans(PlanFilter{Status: PlanOpen})
	assert.NoErr(t, err)
	assert.Eq(t, 3, total)
	total, err = s.CountPlans(PlanFilter{Status: PlanOpen, ProjectKey: "self", Q: "plan-b"})
	assert.NoErr(t, err)
	assert.Eq(t, 1, total)

	// Two plans sharing created_at keep a deterministic (insertion-newest-first) order.
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-t1", Status: PlanOpen, CreatedAt: 900, UpdatedAt: 900}))
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-t2", Status: PlanOpen, CreatedAt: 900, UpdatedAt: 900}))
	tie, err := s.ListPlans(PlanFilter{Status: PlanOpen, Limit: 2})
	assert.NoErr(t, err)
	assert.Eq(t, "plan-t2", tie[0].PlanID)
	assert.Eq(t, "plan-t1", tie[1].PlanID)

	// The page size is the caller's, within [default, cap].
	assert.Eq(t, 20, NormalizePlanLimit(0))
	assert.Eq(t, 20, NormalizePlanLimit(-5))
	assert.Eq(t, 7, NormalizePlanLimit(7))
	assert.Eq(t, 100, NormalizePlanLimit(100))
	assert.Eq(t, 100, NormalizePlanLimit(5000))
}

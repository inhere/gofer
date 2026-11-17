package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestPlanDriverActionRoundTrip: an action is stored with its risk, rule and would_pause
// flag, listed oldest first, and its outcome is stamped once (a second stamp is a no-op).
func TestPlanDriverActionRoundTrip(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-act", Title: "p", Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))

	id1, err := s.InsertPlanDriverAction(PlanDriverAction{
		PlanID: "plan-act", TodoID: "t1", JobID: "j1", EventKind: "advance", Action: "complete_todo",
		ParamsJSON: `{"k":1}`, Risk: "low", Rule: "job_done_complete", CreatedAt: 100,
	})
	assert.NoErr(t, err)
	id2, err := s.InsertPlanDriverAction(PlanDriverAction{
		PlanID: "plan-act", Action: "pause_budget", Risk: "medium", Rule: "budget_exhausted", WouldPause: true, CreatedAt: 200,
	})
	assert.NoErr(t, err)

	all, err := s.ListPlanDriverActions("plan-act", false)
	assert.NoErr(t, err)
	assert.Eq(t, 2, len(all))
	assert.Eq(t, id1, all[0].ID)
	assert.Eq(t, "complete_todo", all[0].Action)
	assert.Eq(t, `{"k":1}`, all[0].ParamsJSON)
	assert.Eq(t, "", all[0].Outcome)
	assert.Eq(t, "{}", all[1].ParamsJSON, "no params reads as an empty object")
	assert.True(t, all[1].WouldPause)

	ok, err := s.ResolvePlanDriverAction(id1, "match", 150)
	assert.NoErr(t, err)
	assert.True(t, ok)
	ok, err = s.ResolvePlanDriverAction(id1, "mismatch", 160)
	assert.NoErr(t, err)
	assert.False(t, ok, "an outcome is stamped once")

	open, err := s.ListPlanDriverActions("plan-act", true)
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(open))
	assert.Eq(t, id2, open[0].ID)
	all, _ = s.ListPlanDriverActions("plan-act", false)
	assert.Eq(t, "match", all[0].Outcome)
	assert.Eq(t, int64(150), all[0].MatchedAt)

	_, err = s.InsertPlanDriverAction(PlanDriverAction{PlanID: "plan-act", Action: "retry", Risk: "low"})
	assert.Err(t, err, "a rule is required")
}

// TestPlanDriverActionsDieWithArchivedPlan: archiving a plan removes its action log along
// with its driver row.
func TestPlanDriverActionsDieWithArchivedPlan(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-arch", Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))
	assert.NoErr(t, s.UpsertPlanDriver(PlanDriver{PlanID: "plan-arch", Level: "record"}))
	_, err := s.InsertPlanDriverAction(PlanDriverAction{PlanID: "plan-arch", Action: "retry", Risk: "low", Rule: "r"})
	assert.NoErr(t, err)
	assert.NoErr(t, s.SetPlanStatus("plan-arch", PlanArchived, -1))
	got, err := s.ListPlanDriverActions("plan-arch", false)
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(got))
}

// TestUpdatePlanDriverSpentKeepsWindow: the accounting write changes spent_json only.
func TestUpdatePlanDriverSpentKeepsWindow(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-sp", Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))
	assert.NoErr(t, s.UpsertPlanDriver(PlanDriver{PlanID: "plan-sp", Level: "record", BudgetJSON: `{"max_jobs":3}`, EnabledAt: 50, UpdatedAt: 50}))
	ok, err := s.UpdatePlanDriverSpent("plan-sp", `{"jobs":2}`)
	assert.NoErr(t, err)
	assert.True(t, ok)
	d, _, _ := s.GetPlanDriver("plan-sp")
	assert.Eq(t, `{"jobs":2}`, d.SpentJSON)
	assert.Eq(t, `{"max_jobs":3}`, d.BudgetJSON)
	assert.Eq(t, int64(50), d.EnabledAt)
	assert.Eq(t, int64(50), d.UpdatedAt)
	ok, err = s.UpdatePlanDriverSpent("plan-none", `{}`)
	assert.NoErr(t, err)
	assert.False(t, ok)
}

// TestPlanJobSpendSince counts the jobs of a plan's items queued since a time, with the
// check resumes they recorded; jobs of other plans and older jobs are not counted.
func TestPlanJobSpendSince(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-js", Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-other", Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))
	assert.NoErr(t, s.InsertTodo(PlanTodo{TodoID: "td-js", PlanID: "plan-js", Title: "a", Status: TodoDoing, CreatedAt: 1, UpdatedAt: 1}))
	assert.NoErr(t, s.InsertTodo(PlanTodo{TodoID: "td-other", PlanID: "plan-other", Title: "b", Status: TodoDoing, CreatedAt: 1, UpdatedAt: 1}))
	for _, r := range []JobRecord{
		{ID: "js-old", TodoID: "td-js", Status: "done", QueuedAt: 10},
		{ID: "js-new1", TodoID: "td-js", Status: "done", QueuedAt: 100, CheckResumeCount: 1},
		{ID: "js-new2", TodoID: "td-js", Status: "failed", StartedAt: 120, CheckResumeCount: 2},
		{ID: "js-other", TodoID: "td-other", Status: "done", QueuedAt: 100},
	} {
		assert.NoErr(t, s.UpsertJob(r))
	}
	sp, err := s.PlanJobSpendSince("plan-js", 50)
	assert.NoErr(t, err)
	assert.Eq(t, 2, sp.Jobs)
	assert.Eq(t, 3, sp.CheckResumes)
}

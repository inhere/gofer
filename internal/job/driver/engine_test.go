package driver

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// testHost is the driver's host in a unit test: a real store and a clock the test moves
// (G052: the 24-hour window is crossed by setting the clock, never by sleeping).
type testHost struct {
	meta  *jobstore.Store
	clock atomic.Int64
}

func (h *testHost) Meta() *jobstore.Store { return h.meta }
func (h *testHost) Now() time.Time        { return time.Unix(h.clock.Load(), 0) }
func (h *testHost) advance(d time.Duration) {
	h.clock.Add(int64(d / time.Second))
}

const baseTime = int64(1_000_000)

func newHost(t *testing.T) (*testHost, *Engine) {
	t.Helper()
	meta, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = meta.Close() })
	h := &testHost{meta: meta}
	h.clock.Store(baseTime)
	return h, NewEngine(h)
}

// seedChain makes plan p with todos t1 (done) -> t2 -> t3, all assigned and automatic.
func seedChain(t *testing.T, h *testHost, planID string) {
	t.Helper()
	assert.NoErr(t, h.meta.InsertPlan(jobstore.Plan{PlanID: planID, Title: planID, Status: jobstore.PlanOpen, CreatedAt: 1, UpdatedAt: 1}))
	for i, td := range []jobstore.PlanTodo{
		{TodoID: planID + "-t1", Title: "one", Status: jobstore.TodoDoing},
		{TodoID: planID + "-t2", Title: "two", Status: jobstore.TodoPending, After: []string{planID + "-t1"}},
		{TodoID: planID + "-t3", Title: "three", Status: jobstore.TodoPending, After: []string{planID + "-t2"}},
	} {
		td.PlanID, td.Assignee, td.Auto, td.Sort, td.CreatedAt, td.UpdatedAt = planID, "ok", true, (i+1)*10, 1, 1
		assert.NoErr(t, h.meta.InsertTodo(td))
	}
}

func enable(t *testing.T, h *testHost, planID string, b job.PlanDriverBudget) {
	t.Helper()
	assert.NoErr(t, h.meta.UpsertPlanDriver(jobstore.PlanDriver{
		PlanID: planID, Level: job.PlanDriverLevelRecord,
		BudgetJSON: fmt.Sprintf(`{"max_jobs":%d,"max_rework":%d,"max_hours":%g}`, b.MaxJobs, b.MaxRework, b.MaxHours),
		EnabledAt:  h.clock.Load(), UpdatedAt: h.clock.Load(),
	}))
}

// fire runs one event the way the job layer does: capture, (the host's processing is the
// test's own store writes made before / after), observe.
func fire(e *Engine, ev job.PlanEventView) {
	tk := e.BeforePlanEvent(context.Background(), ev)
	e.OnPlanEvent(context.Background(), ev, tk)
}

func listActions(t *testing.T, h *testHost, planID string) []jobstore.PlanDriverAction {
	t.Helper()
	got, err := h.meta.ListPlanDriverActions(planID, false)
	assert.NoErr(t, err)
	return got
}

func setTodo(t *testing.T, h *testHost, todoID, status string) {
	t.Helper()
	_, err := h.meta.UpdateTodoStatus(todoID, status, nil)
	assert.NoErr(t, err)
}

func putJob(t *testing.T, h *testHost, rec jobstore.JobRecord) {
	t.Helper()
	if rec.QueuedAt == 0 {
		rec.QueuedAt = h.clock.Load()
	}
	if rec.StartedAt == 0 {
		rec.StartedAt = rec.QueuedAt
	}
	assert.NoErr(t, h.meta.UpsertJob(rec))
	if rec.ResultDecision != "" { // the column has its own writer
		_, err := h.meta.SetJobSelfResult(rec.ID, rec.ResultDecision, "because", "{}")
		assert.NoErr(t, err)
	}
}

// TestEngineJobRulesRecordAndObserve: each rule of the rule layer fires for the stored
// facts of a finished job, is recorded with its rule, risk and the rule's reading, and is
// settled against what the host really did (the todo / plan state the test left behind).
func TestEngineJobRulesRecordAndObserve(t *testing.T) {
	type tc struct {
		name    string
		kind    string
		job     jobstore.JobRecord
		setup   func(t *testing.T, h *testHost, planID string)
		budget  job.PlanDriverBudget
		action  string
		rule    string
		outcome string
	}
	completed := func(t *testing.T, h *testHost, p string) { setTodo(t, h, p+"-t1", jobstore.TodoDone) }
	blocked := func(t *testing.T, h *testHost, p string) {
		assert.NoErr(t, h.meta.SetPlanBlocked(p, p+"-t1"))
	}
	cases := []tc{
		{name: "job done completes", kind: job.PlanEventAdvance, job: jobstore.JobRecord{Status: job.StatusDone}, setup: completed,
			action: ActionCompleteTodo, rule: RuleJobDoneComplete, outcome: OutcomeMatch},
		{name: "check pass completes", kind: job.PlanEventAdvance, job: jobstore.JobRecord{Status: job.StatusDone, CheckJSON: `{"verdict":"pass"}`}, setup: completed,
			action: ActionCompleteTodo, rule: RuleCheckPassedComplete, outcome: OutcomeMatch},
		{name: "self pass completes", kind: job.PlanEventAdvance, job: jobstore.JobRecord{Status: job.StatusDone, ResultDecision: job.ResultPass}, setup: completed,
			action: ActionCompleteTodo, rule: RuleSelfPassComplete, outcome: OutcomeMatch},
		{name: "self human is an escalate the host did not honour", kind: job.PlanEventAdvance, job: jobstore.JobRecord{Status: job.StatusDone, ResultDecision: job.ResultHuman}, setup: completed,
			action: ActionEscalate, rule: RuleSelfHumanEscalate, outcome: OutcomeMismatch},
		{name: "self fail on exit 0", kind: job.PlanEventAdvance, job: jobstore.JobRecord{Status: job.StatusDone, ResultDecision: job.ResultFail}, setup: completed,
			action: ActionEscalate, rule: RuleSelfFailEscalate, outcome: OutcomeMismatch},
		{name: "self rework", kind: job.PlanEventAdvance, job: jobstore.JobRecord{Status: job.StatusDone, ResultDecision: job.ResultRework}, setup: completed,
			action: ActionResumeWithNotes, rule: RuleSelfReworkResume, outcome: OutcomeMismatch},
		{name: "check failed", kind: job.PlanEventAdvance, job: jobstore.JobRecord{Status: job.StatusDone, CheckJSON: `{"verdict":"fail"}`}, setup: completed,
			action: ActionResumeWithNotes, rule: RuleCheckFailedResume, outcome: OutcomeMismatch},
		{name: "rework budget used up", kind: job.PlanEventAdvance, job: jobstore.JobRecord{Status: job.StatusDone, ResultDecision: job.ResultRework, CheckResumeCount: 1}, setup: completed,
			budget: job.PlanDriverBudget{MaxRework: 1}, action: ActionEscalate, rule: RuleReworkBudgetEscalate, outcome: OutcomeMismatch},
		{name: "transient failure the host blocked on", kind: job.PlanEventBlock, job: jobstore.JobRecord{Status: job.StatusFailed, FailureClass: job.FailureClassTransient}, setup: blocked,
			action: ActionRetry, rule: RuleFailureTransientRetry, outcome: OutcomeMismatch},
		{name: "transient failure the host auto-resumed", kind: job.PlanEventBlock, job: jobstore.JobRecord{Status: job.StatusFailed, FailureClass: job.FailureClassTransient, AutoResumedBy: "resumed-job"},
			setup: func(t *testing.T, h *testHost, p string) {
				putJob(t, h, jobstore.JobRecord{ID: p + "-resumed", TodoID: p + "-t1", Status: job.StatusRunning, StartedAt: baseTime + 5})
			},
			action: ActionRetry, rule: RuleFailureTransientRetry, outcome: OutcomeMatch},
		{name: "other failure parks the plan", kind: job.PlanEventBlock, job: jobstore.JobRecord{Status: job.StatusFailed}, setup: blocked,
			action: ActionEscalate, rule: RuleFailureOtherEscalate, outcome: OutcomeMatch},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, e := newHost(t)
			planID := fmt.Sprintf("p%d", i)
			seedChain(t, h, planID)
			enable(t, h, planID, c.budget)
			c.job.ID, c.job.TodoID = planID+"-j1", planID+"-t1"
			putJob(t, h, c.job)
			c.setup(t, h, planID)

			fire(e, job.PlanEventView{PlanID: planID, TodoID: planID + "-t1", JobID: c.job.ID, Kind: c.kind})

			var got *jobstore.PlanDriverAction
			for _, a := range listActions(t, h, planID) {
				if a.Action == c.action && a.Rule == c.rule {
					got = &a
				}
			}
			if got == nil {
				t.Fatalf("no %s/%s action recorded: %+v", c.action, c.rule, listActions(t, h, planID))
			}
			assert.Eq(t, planID+"-t1", got.TodoID)
			assert.Eq(t, c.job.ID, got.JobID)
			assert.Eq(t, c.kind, got.EventKind)
			assert.Eq(t, RiskOf(c.action), got.Risk)
			assert.Eq(t, c.outcome, got.Outcome)
			assert.False(t, got.WouldPause)
		})
	}
}

// TestEngineDispatchRecordedThenObserved: an item the chain would start is recorded
// before the host starts it; once a job exists for it, a later event settles it as a match.
func TestEngineDispatchRecordedThenObserved(t *testing.T) {
	h, e := newHost(t)
	seedChain(t, h, "pd")
	enable(t, h, "pd", job.PlanDriverBudget{})
	setTodo(t, h, "pd-t1", jobstore.TodoDone) // t2 is now due

	ev := job.PlanEventView{PlanID: "pd", Kind: job.PlanEventAdvance}
	tk := e.BeforePlanEvent(context.Background(), ev)
	assert.NotNil(t, tk)
	e.OnPlanEvent(context.Background(), ev, tk) // the host did not start it (yet)

	acts := listActions(t, h, "pd")
	assert.Eq(t, 1, len(acts))
	assert.Eq(t, ActionDispatchTodo, acts[0].Action)
	assert.Eq(t, RuleDispatchReady, acts[0].Rule)
	assert.Eq(t, "pd-t2", acts[0].TodoID)
	assert.Eq(t, "", acts[0].Outcome, "nothing happened to t2 yet")

	putJob(t, h, jobstore.JobRecord{ID: "pd-j2", TodoID: "pd-t2", Status: job.StatusRunning})
	h.advance(time.Minute)
	fire(e, job.PlanEventView{PlanID: "pd", Kind: job.PlanEventSweep})
	acts = listActions(t, h, "pd")
	assert.Eq(t, OutcomeMatch, acts[0].Outcome)
	assert.Eq(t, baseTime+60, acts[0].MatchedAt)
	assert.Eq(t, 1, len(acts), "a started item is not a candidate again")
}

// TestEngineOutcomes: the three outcomes — match, mismatch (the host did something else)
// and no_followup (24 hours with no change), the last one reached by moving the clock.
func TestEngineOutcomes(t *testing.T) {
	setup := func(t *testing.T, planID string) (*testHost, *Engine) {
		h, e := newHost(t)
		seedChain(t, h, planID)
		enable(t, h, planID, job.PlanDriverBudget{})
		setTodo(t, h, planID+"-t1", jobstore.TodoDone)
		fire(e, job.PlanEventView{PlanID: planID, Kind: job.PlanEventAdvance})
		assert.Eq(t, "", listActions(t, h, planID)[0].Outcome)
		return h, e
	}

	t.Run("mismatch", func(t *testing.T) {
		h, e := setup(t, "pm")
		setTodo(t, h, "pm-t2", jobstore.TodoSkipped) // a human skipped what the driver would run
		fire(e, job.PlanEventView{PlanID: "pm", Kind: job.PlanEventSweep})
		assert.Eq(t, OutcomeMismatch, listActions(t, h, "pm")[0].Outcome)
	})
	t.Run("no followup only after the window", func(t *testing.T) {
		h, e := setup(t, "pn")
		h.advance(ObserveWindow - time.Second)
		fire(e, job.PlanEventView{PlanID: "pn", Kind: job.PlanEventSweep})
		assert.Eq(t, "", listActions(t, h, "pn")[0].Outcome, "still inside the window")
		h.advance(2 * time.Second)
		fire(e, job.PlanEventView{PlanID: "pn", Kind: job.PlanEventSweep})
		a := listActions(t, h, "pn")[0]
		assert.Eq(t, OutcomeNoFollowup, a.Outcome)
		assert.Eq(t, h.clock.Load(), a.MatchedAt)
	})
	t.Run("the report settles a plan nothing advances any more", func(t *testing.T) {
		h, e := setup(t, "pr")
		h.advance(ObserveWindow + time.Hour)
		rep, err := e.PlanDriverReport(context.Background(), "pr")
		assert.NoErr(t, err)
		assert.Eq(t, 0, rep.Pending)
		assert.Eq(t, 1, rep.ByAction[0].NoFollowup)
	})
}

// TestEngineBudgetOverrunOnlyRecords: a used-up ceiling records a would-pause action (once
// per state) and the accounting, and pauses nothing.
func TestEngineBudgetOverrunOnlyRecords(t *testing.T) {
	h, e := newHost(t)
	seedChain(t, h, "pb")
	enable(t, h, "pb", job.PlanDriverBudget{MaxJobs: 2, MaxHours: 1})
	putJob(t, h, jobstore.JobRecord{ID: "pb-j1", TodoID: "pb-t1", Status: job.StatusDone})
	putJob(t, h, jobstore.JobRecord{ID: "pb-j2", TodoID: "pb-t1", Status: job.StatusFailed})
	setTodo(t, h, "pb-t1", jobstore.TodoDone)
	fire(e, job.PlanEventView{PlanID: "pb", Kind: job.PlanEventSweep})
	fire(e, job.PlanEventView{PlanID: "pb", Kind: job.PlanEventSweep}) // same state: no second row

	var pauses []jobstore.PlanDriverAction
	for _, a := range listActions(t, h, "pb") {
		if a.Action == ActionPauseBudget {
			pauses = append(pauses, a)
		}
	}
	assert.Eq(t, 1, len(pauses))
	assert.True(t, pauses[0].WouldPause)
	assert.Eq(t, RuleBudgetExhausted, pauses[0].Rule)
	assert.StrContains(t, pauses[0].ParamsJSON, "max_jobs")
	d, _, _ := h.meta.GetPlanDriver("pb")
	assert.StrContains(t, d.SpentJSON, `"jobs":2`)

	// One more job is a new state worth one more row; the hours ceiling joins at 1h.
	putJob(t, h, jobstore.JobRecord{ID: "pb-j3", TodoID: "pb-t1", Status: job.StatusDone})
	h.advance(time.Hour)
	before := snapshot(t, h, "pb")
	fire(e, job.PlanEventView{PlanID: "pb", Kind: job.PlanEventSweep})
	n := 0
	for _, a := range listActions(t, h, "pb") {
		if a.Action == ActionPauseBudget {
			n++
		}
	}
	assert.Eq(t, 2, n)
	// Nothing but the driver's own tables changed.
	assert.Eq(t, before, snapshot(t, h, "pb"))
	assert.StrContains(t, before, "paused=false")
}

// TestEngineReworkCountsRecordedResumes: a recorded resume_with_notes is a rework round,
// so the second self-reported rework meets a ceiling of one.
func TestEngineReworkCountsRecordedResumes(t *testing.T) {
	h, e := newHost(t)
	seedChain(t, h, "pw")
	enable(t, h, "pw", job.PlanDriverBudget{MaxRework: 1})
	setTodo(t, h, "pw-t1", jobstore.TodoDone)
	for _, id := range []string{"pw-j1", "pw-j2"} {
		putJob(t, h, jobstore.JobRecord{ID: id, TodoID: "pw-t1", Status: job.StatusDone, ResultDecision: job.ResultRework})
		fire(e, job.PlanEventView{PlanID: "pw", TodoID: "pw-t1", JobID: id, Kind: job.PlanEventAdvance})
	}
	var rules []string
	for _, a := range listActions(t, h, "pw") {
		if a.JobID != "" {
			rules = append(rules, a.Rule)
		}
	}
	assert.Eq(t, []string{RuleSelfReworkResume, RuleReworkBudgetEscalate}, rules)
}

// TestEngineOffPlanIsNotObserved: a plan without a driver gets no ticket and no actions.
func TestEngineOffPlanIsNotObserved(t *testing.T) {
	h, e := newHost(t)
	seedChain(t, h, "po")
	setTodo(t, h, "po-t1", jobstore.TodoDone)
	assert.Nil(t, e.BeforePlanEvent(context.Background(), job.PlanEventView{PlanID: "po", Kind: job.PlanEventAdvance}))
	fire(e, job.PlanEventView{PlanID: "po", Kind: job.PlanEventAdvance})
	assert.Eq(t, 0, len(listActions(t, h, "po")))
}

// TestEngineReplayDoesNotDuplicate: an event replayed after a crash (the same job-bound
// event twice) records its action once.
func TestEngineReplayDoesNotDuplicate(t *testing.T) {
	h, e := newHost(t)
	seedChain(t, h, "pq")
	enable(t, h, "pq", job.PlanDriverBudget{})
	putJob(t, h, jobstore.JobRecord{ID: "pq-j1", TodoID: "pq-t1", Status: job.StatusDone})
	setTodo(t, h, "pq-t1", jobstore.TodoDone)
	ev := job.PlanEventView{PlanID: "pq", TodoID: "pq-t1", JobID: "pq-j1", Kind: job.PlanEventAdvance}
	fire(e, ev)
	fire(e, ev)
	n := 0
	for _, a := range listActions(t, h, "pq") {
		if a.JobID == "pq-j1" {
			n++
		}
	}
	assert.Eq(t, 1, n)
}

// TestEngineNeverChangesPlanTodoOrJob: the record-level invariant at the store: whatever
// the events are (every rule, a budget overrun, a sweep), the plan, its todos and its jobs
// read exactly as before; only the driver's own rows appear.
func TestEngineNeverChangesPlanTodoOrJob(t *testing.T) {
	h, e := newHost(t)
	seedChain(t, h, "pi")
	enable(t, h, "pi", job.PlanDriverBudget{MaxJobs: 1, MaxRework: 1})
	setTodo(t, h, "pi-t1", jobstore.TodoDone)
	for i, rec := range []jobstore.JobRecord{
		{Status: job.StatusDone, ResultDecision: job.ResultHuman},
		{Status: job.StatusDone, ResultDecision: job.ResultRework},
		{Status: job.StatusDone, CheckJSON: `{"verdict":"fail"}`},
		{Status: job.StatusDone, CheckJSON: `{"verdict":"pass"}`},
		{Status: job.StatusFailed, FailureClass: job.FailureClassTransient},
		{Status: job.StatusFailed},
	} {
		rec.ID, rec.TodoID = fmt.Sprintf("pi-j%d", i), "pi-t1"
		putJob(t, h, rec)
	}
	before := snapshot(t, h, "pi")
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("pi-j%d", i)
		kind := job.PlanEventAdvance
		if i >= 4 {
			kind = job.PlanEventBlock
		}
		fire(e, job.PlanEventView{PlanID: "pi", TodoID: "pi-t1", JobID: id, Kind: kind})
	}
	fire(e, job.PlanEventView{PlanID: "pi", Kind: job.PlanEventSweep})
	h.advance(ObserveWindow + time.Hour)
	fire(e, job.PlanEventView{PlanID: "pi", Kind: job.PlanEventAdvance})
	_, err := e.PlanDriverReport(context.Background(), "pi")
	assert.NoErr(t, err)

	assert.True(t, len(listActions(t, h, "pi")) >= 6, "the events were recorded")
	assert.Eq(t, before, snapshot(t, h, "pi"))
}

// snapshot renders every field of the plan, its todos and its jobs (everything but the
// driver's own tables) so two readings can be compared for equality.
func snapshot(t *testing.T, h *testHost, planID string) string {
	t.Helper()
	p, _, err := h.meta.GetPlan(planID)
	assert.NoErr(t, err)
	out := fmt.Sprintf("plan=%+v paused=%v\n", p, p.Paused)
	todos, err := h.meta.ListTodosByPlan(planID)
	assert.NoErr(t, err)
	for _, td := range todos {
		out += fmt.Sprintf("todo=%+v\n", td)
		jobs, err := h.meta.ListJobsByTodo(td.TodoID, 100)
		assert.NoErr(t, err)
		for _, j := range jobs {
			out += fmt.Sprintf("job=%+v\n", j)
		}
	}
	return out
}

// TestReportSummarizesAndSuggestsUpgrade: by-action and by-rule totals, the agreement rate
// (a no_followup is not agreement) and the "20 in a row at >= 90%" condition per action.
func TestReportSummarizesAndSuggestsUpgrade(t *testing.T) {
	h, e := newHost(t)
	seedChain(t, h, "ps")
	enable(t, h, "ps", job.PlanDriverBudget{MaxJobs: 9})
	add := func(action, rule, outcome string) {
		_, err := h.meta.InsertPlanDriverAction(jobstore.PlanDriverAction{
			PlanID: "ps", TodoID: "ps-t3", Action: action, Risk: RiskOf(action), Rule: rule, Outcome: outcome,
			MatchedAt: 1, CreatedAt: baseTime,
		})
		assert.NoErr(t, err)
	}
	// dispatch_todo: 2 mismatches long ago, then 20 matches -> the latest 20 agree 100%.
	add(ActionDispatchTodo, RuleDispatchReady, OutcomeMismatch)
	add(ActionDispatchTodo, RuleDispatchReady, OutcomeMismatch)
	for i := 0; i < 20; i++ {
		add(ActionDispatchTodo, RuleDispatchReady, OutcomeMatch)
	}
	// retry: 20 resolved but only 17 match -> below 90%.
	for i := 0; i < 17; i++ {
		add(ActionRetry, RuleFailureTransientRetry, OutcomeMatch)
	}
	add(ActionRetry, RuleFailureTransientRetry, OutcomeMismatch)
	add(ActionRetry, RuleFailureTransientRetry, OutcomeMismatch)
	add(ActionRetry, RuleFailureTransientRetry, OutcomeNoFollowup)
	// escalate: too few resolved to judge, one still open (settled by the report: the
	// todo t3 is untouched and the window is not over, so it stays pending).
	add(ActionEscalate, RuleSelfHumanEscalate, OutcomeMatch)
	add(ActionEscalate, RuleFailureOtherEscalate, "")

	rep, err := e.PlanDriverReport(context.Background(), "ps")
	assert.NoErr(t, err)
	assert.True(t, rep.Enabled)
	assert.Eq(t, 9, rep.Budget.MaxJobs)
	assert.Eq(t, 44, rep.Total)
	assert.Eq(t, 1, rep.Pending)
	rows := map[string]job.PlanDriverReportRow{}
	for _, r := range rep.ByAction {
		rows[r.Action] = r
	}
	d := rows[ActionDispatchTodo]
	assert.Eq(t, 22, d.Total)
	assert.Eq(t, 20, d.Match)
	assert.Eq(t, 2, d.Mismatch)
	assert.Eq(t, 20, d.RecentN)
	assert.Eq(t, 20, d.RecentMatch)
	assert.True(t, d.UpgradeSuggested)
	r := rows[ActionRetry]
	assert.Eq(t, 17, r.Match)
	assert.Eq(t, 1, r.NoFollowup)
	assert.Eq(t, 17.0/20.0, r.Rate)
	assert.False(t, r.UpgradeSuggested)
	es := rows[ActionEscalate]
	assert.Eq(t, 1, es.Pending)
	assert.False(t, es.UpgradeSuggested)
	byRule := map[string]job.PlanDriverReportRow{}
	for _, x := range rep.ByRule {
		byRule[x.Action+"/"+x.Rule] = x
	}
	assert.Eq(t, 1, byRule[ActionEscalate+"/"+RuleFailureOtherEscalate].Pending)
	assert.Eq(t, 3, len(rep.ByAction))
	assert.Eq(t, 4, len(rep.ByRule))
	// The line-by-line view: newest first, every action while under the cap.
	assert.Eq(t, 44, len(rep.Recent))
	assert.Eq(t, ActionEscalate, rep.Recent[0].Action)
	assert.Eq(t, RuleFailureOtherEscalate, rep.Recent[0].Rule)
	assert.Eq(t, ActionDispatchTodo, rep.Recent[43].Action)
	assert.Eq(t, OutcomeMismatch, rep.Recent[43].Outcome)
	assert.True(t, rep.Recent[0].ID > rep.Recent[43].ID)
}

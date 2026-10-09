package today

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

var t0 = time.Date(2026, 10, 9, 10, 0, 0, 0, time.Local)

type fixture struct {
	st  *jobstore.Store
	svc *work.Service
	b   *LanesBuilder
	now time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "t.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = st.Close() })
	f := &fixture{st: st, now: t0}
	clock := func() time.Time { return f.now }
	st.SetClock(clock)
	f.svc = work.New(st)
	f.svc.SetNow(clock)
	f.svc.SetConfigFn(func() config.WorkConfig { return config.WorkConfig{StallAfter: "4h"} })
	f.b = NewLanesBuilder(f.svc)
	f.b.SetNow(clock)
	return f
}

func (f *fixture) plan(t *testing.T, id, title, status string, todos ...jobstore.PlanTodo) {
	t.Helper()
	assert.NoErr(t, f.st.InsertPlan(jobstore.Plan{PlanID: id, Title: title, Status: status, CreatedAt: f.now.Unix(), UpdatedAt: f.now.Unix()}))
	for i, td := range todos {
		td.PlanID = id
		td.Sort = i
		td.CreatedAt, td.UpdatedAt = f.now.Unix(), f.now.Unix()
		assert.NoErr(t, f.st.InsertTodo(td))
	}
}

func (f *fixture) job(t *testing.T, id, agent, status, planID string) {
	t.Helper()
	assert.NoErr(t, f.st.UpsertJob(jobstore.JobRecord{ID: id, ProjectKey: "p", Agent: agent, Runner: "local", Status: status,
		ResultDir: "/tmp/" + id, StartedAt: f.now.Unix(), UpdatedAt: f.now.Unix(), PlanID: planID,
		UsageJSON: `{"total_tokens":1000,"cost_usd":0.5}`}))
}

func laneByID(v LanesView, id string) (Lane, bool) {
	for _, l := range v.Lanes {
		if l.ID == id {
			return l, true
		}
	}
	return Lane{}, false
}

func TestTopoTodos(t *testing.T) {
	todos := []jobstore.PlanTodo{
		{TodoID: "c", After: []string{"b"}},
		{TodoID: "a"},
		{TodoID: "b", After: []string{"a", "missing"}},
		{TodoID: "d"},
	}
	var ids []string
	for _, td := range TopoTodos(todos) {
		ids = append(ids, td.TodoID)
	}
	assert.Eq(t, []string{"a", "b", "c", "d"}, ids)

	// A cycle does not hang: everything still comes out once.
	cyc := []jobstore.PlanTodo{{TodoID: "x", After: []string{"y"}}, {TodoID: "y", After: []string{"x"}}, {TodoID: "z"}}
	assert.Len(t, TopoTodos(cyc), 3)
}

func TestBuildLanes(t *testing.T) {
	f := newFixture(t)

	// A work item with a running terminal session and a linked plan (folded into it).
	_, err := f.st.UpsertAgentSession(jobstore.AgentSession{SessionID: "sess-run1", Agent: "claude", State: jobstore.SessionRunning})
	assert.NoErr(t, err)
	wRun, err := f.st.CreateWorkItem(jobstore.WorkItemInput{Title: "导出接口", ProjectKey: "orders", SessionIDs: []string{"sess-run1"}, By: "human"})
	assert.NoErr(t, err)
	f.plan(t, "plan-fold0001", "导出", jobstore.PlanOpen,
		jobstore.PlanTodo{TodoID: "t2", Title: "分页", After: []string{"t1"}, JobID: "job-t2"},
		jobstore.PlanTodo{TodoID: "t1", Title: "CSV", Status: jobstore.TodoDone},
		jobstore.PlanTodo{TodoID: "t3", Title: "文档", After: []string{"t2"}},
	)
	f.job(t, "job-t2", "codex", "running", "plan-fold0001")
	_, err = f.st.AddWorkLink(wRun.ID, jobstore.WorkLinkPlan, "plan-fold0001", "human")
	assert.NoErr(t, err)

	// A parked item (excluded) and a quiet active one (stalled later).
	wPark, err := f.st.CreateWorkItem(jobstore.WorkItemInput{Title: "等机器", By: "human"})
	assert.NoErr(t, err)
	_, err = f.svc.Park(wPark.ID, 0, "", "human")
	assert.NoErr(t, err)
	wQuiet, err := f.st.CreateWorkItem(jobstore.WorkItemInput{Title: "CI 提速", By: "human"})
	assert.NoErr(t, err)

	// A blocked standalone plan, a running standalone plan and an untouched one.
	f.plan(t, "plan-blk00001", "N2", jobstore.PlanBlocked,
		jobstore.PlanTodo{TodoID: "b1", Title: "后端", Status: jobstore.TodoDone},
		jobstore.PlanTodo{TodoID: "b2", Title: "Windows 测试", After: []string{"b1"}},
	)
	assert.NoErr(t, f.st.SetPlanBlocked("plan-blk00001", "b2"))
	f.plan(t, "plan-run00001", "迁移", jobstore.PlanOpen,
		jobstore.PlanTodo{TodoID: "r1", Title: "改表", Status: jobstore.TodoDoing},
		jobstore.PlanTodo{TodoID: "r2", Title: "回填"},
	)
	f.plan(t, "plan-new00001", "还没开始", jobstore.PlanOpen, jobstore.PlanTodo{TodoID: "n1", Title: "x"})

	// Five hours later: the quiet item stalls; the running session keeps wRun healthy.
	f.now = f.now.Add(5 * time.Hour)
	_, _, err = f.st.TouchAgentSession("sess-run1", jobstore.SessionHeartbeat{State: jobstore.SessionRunning})
	assert.NoErr(t, err)

	v, err := f.b.Build()
	assert.NoErr(t, err)
	var ids []string
	for _, l := range v.Lanes {
		ids = append(ids, l.ID)
	}
	// blocked plan > the two stalled lanes (same activity: by id) > the healthy lane.
	assert.Eq(t, []string{"plan-blk00001", "plan-run00001", wQuiet.ID, wRun.ID}, ids)

	blk, _ := laneByID(v, "plan-blk00001")
	assert.Eq(t, work.HealthBlocked, blk.Health)
	assert.Eq(t, "卡在「Windows 测试」", blk.HealthReason)
	assert.Eq(t, "Windows 测试", blk.Progress.Current)
	assert.Eq(t, []string{PipDone, PipFailed}, pipStates(blk))

	quiet, _ := laneByID(v, wQuiet.ID)
	assert.Eq(t, work.HealthStalled, quiet.Health)
	assert.Eq(t, "创建工作项", quiet.Progress.Current) // the latest milestone
	assert.Len(t, quiet.Agents, 0)

	run, _ := laneByID(v, wRun.ID)
	assert.Eq(t, LaneWork, run.Kind)
	assert.Eq(t, work.HealthOK, run.Health)
	assert.Eq(t, "plan-fold0001", run.Links.PlanID)
	assert.Eq(t, []string{PipDone, PipRunning, PipPending}, pipStates(run)) // topological: t1, t2, t3
	assert.Eq(t, "分页", run.Progress.Current)
	assert.Eq(t, 2, len(run.Agents)) // the session and the plan's running job
	assert.Eq(t, int64(5*3600), run.ElapsedSec)
	assert.NotNil(t, run.Usage)
	assert.Eq(t, int64(1000), run.Usage.TotalTokens)

	_, folded := laneByID(v, "plan-fold0001")
	assert.False(t, folded)
	_, parked := laneByID(v, wPark.ID)
	assert.False(t, parked)
	_, untouched := laneByID(v, "plan-new00001")
	assert.False(t, untouched)

	pr, _ := laneByID(v, "plan-run00001")
	assert.Eq(t, LanePlan, pr.Kind)
	assert.Eq(t, "改表", pr.Progress.Current)
	// An open plan with nothing moving for 5h is stalled too.
	assert.Eq(t, work.HealthStalled, pr.Health)

	assert.Eq(t, LanesSummary{Total: 4, AgentsRunning: 2, Attention: 3}, v.Summary)
}

func pipStates(l Lane) []string {
	var out []string
	for _, p := range l.Progress.Pips {
		out = append(out, p.Status)
	}
	return out
}

func TestSortLanes(t *testing.T) {
	lanes := []Lane{
		{ID: "ok-old", Health: work.HealthOK, ActivityAt: 1},
		{ID: "ok-running", Health: work.HealthOK, ActivityAt: 0, Agents: []LaneAgent{{State: AgentRunning}}},
		{ID: "risk", Health: work.HealthAtRisk},
		{ID: "ok-new", Health: work.HealthOK, ActivityAt: 9},
		{ID: "stalled", Health: work.HealthStalled},
		{ID: "blocked", Health: work.HealthBlocked},
		{ID: "ok-idle", Health: work.HealthOK, ActivityAt: 5, Agents: []LaneAgent{{State: AgentIdle}}},
	}
	SortLanes(lanes)
	var ids []string
	for _, l := range lanes {
		ids = append(ids, l.ID)
	}
	assert.Eq(t, []string{"blocked", "stalled", "risk", "ok-running", "ok-new", "ok-idle", "ok-old"}, ids)
	assert.Eq(t, LanesSummary{Total: 7, AgentsRunning: 1, Attention: 3}, Summarize(lanes))
}

func TestAgentStateMapping(t *testing.T) {
	for st, want := range map[string]string{"running": AgentRunning, "queued": AgentRunning, "pending_interaction": AgentAwaitingInput, "awaiting_input": AgentIdle} {
		got, ok := jobAgentState(st)
		assert.True(t, ok, st)
		assert.Eq(t, want, got, st)
	}
	for _, st := range []string{"done", "failed", "needs_review", "cancelled"} {
		_, ok := jobAgentState(st)
		assert.False(t, ok, st)
	}
	got, ok := sessionAgentState(work.SessionBrief{State: jobstore.SessionWaitingReply})
	assert.True(t, ok)
	assert.Eq(t, AgentAwaitingInput, got)
	_, ok = sessionAgentState(work.SessionBrief{State: jobstore.SessionEnded})
	assert.False(t, ok)
}

// TestParkedItemPlanStillShows: a blocked / running plan linked to a PARKED work item
// is not folded into a lane that never shows — it gets its own plan lane.
func TestParkedItemPlanStillShows(t *testing.T) {
	f := newFixture(t)
	w, err := f.st.CreateWorkItem(jobstore.WorkItemInput{Title: "等机器", By: "human"})
	assert.NoErr(t, err)
	f.plan(t, "plan-park0001", "迁移", jobstore.PlanOpen,
		jobstore.PlanTodo{TodoID: "p1", Title: "改表", JobID: "job-p1"},
	)
	f.job(t, "job-p1", "codex", "running", "plan-park0001")
	_, err = f.st.AddWorkLink(w.ID, jobstore.WorkLinkPlan, "plan-park0001", "human")
	assert.NoErr(t, err)
	_, err = f.svc.Park(w.ID, 0, "", "human")
	assert.NoErr(t, err)

	v, err := f.b.Build()
	assert.NoErr(t, err)
	_, parked := laneByID(v, w.ID)
	assert.False(t, parked)
	pl, ok := laneByID(v, "plan-park0001")
	assert.True(t, ok)
	assert.Eq(t, LanePlan, pl.Kind)
	assert.Eq(t, []string{PipRunning}, pipStates(pl))
}

package job

import (
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// TestPlanNotifyMakesDispatchedJobsNotifyTheSupervisor: `plan create --notify` /
// `plan set --notify on` is ONE decision that covers every job the plan dispatches —
// each of them tells the plan's supervising session that it ended (gofer-3db4).
func TestPlanNotifyMakesDispatchedJobsNotifyTheSupervisor(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	svc := newTestService(t, root)
	_, err := svc.meta.UpsertAgentSession(jobstore.AgentSession{
		SessionID: "plan-notify-sup", Agent: "claude", ProjectKey: "self",
		Runner: "local", Cwd: root, CallerID: "caller-a",
	})
	assert.NoErr(t, err)
	assert.NoErr(t, svc.meta.InsertPlan(jobstore.Plan{
		PlanID: "plan-notify-dispatch", Owner: "caller-a", ProjectKey: "self",
		SupervisorSessionID: "plan-notify-sup", Notify: jobstore.PlanNotifyOn,
	}))
	assert.NoErr(t, svc.meta.InsertTodo(jobstore.PlanTodo{
		TodoID: "todo-notify-dispatch", PlanID: "plan-notify-dispatch", Title: "build",
		Assignee: "exec", Cmd: []string{"go", "version"}, Runner: "local", Cwd: ".",
	}))

	if _, err := svc.RunPlan("plan-notify-dispatch", "caller-a"); err != nil {
		t.Fatal(err)
	}
	todo, ok, err := svc.meta.GetTodo("todo-notify-dispatch")
	assert.NoErr(t, err)
	assert.True(t, ok)
	if todo.JobID == "" {
		t.Fatalf("todo not dispatched: status=%s error=%q", todo.Status, todo.DispatchError)
	}
	_, ok = svc.Wait(todo.JobID)
	assert.True(t, ok)

	rec, ok, err := svc.meta.GetJob(todo.JobID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	rebuilt := fromRecord(rec)
	assert.True(t, rebuilt.Notify, "the plan's switch must reach the dispatched job")
	assert.Eq(t, "plan-notify-sup", rebuilt.NotifySession)
	// The pending record is the supervisor session's watch — the same row the source
	// claim uses here, so exactly one is registered.
	watches, err := svc.meta.ListSessionJobWatches("plan-notify-sup")
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(watches))
	assert.Eq(t, todo.JobID, watches[0].JobID)
}

// TestPlanNotifyWithoutSupervisorNotifiesNobody: a plan with the switch on and no
// supervisor session has no address, so its jobs dispatch as usual and notify nobody
// (instead of failing the dispatch over a notification that could not be sent anyway).
func TestPlanNotifyWithoutSupervisorNotifiesNobody(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	svc := newTestService(t, root)
	assert.NoErr(t, svc.meta.InsertPlan(jobstore.Plan{
		PlanID: "plan-notify-unbound", Owner: "caller-a", ProjectKey: "self",
		Notify: jobstore.PlanNotifyOn,
	}))
	assert.NoErr(t, svc.meta.InsertTodo(jobstore.PlanTodo{
		TodoID: "todo-notify-unbound", PlanID: "plan-notify-unbound", Title: "build",
		Assignee: "exec", Cmd: []string{"go", "version"}, Runner: "local", Cwd: ".",
	}))

	if _, err := svc.RunPlan("plan-notify-unbound", "caller-a"); err != nil {
		t.Fatal(err)
	}
	todo, ok, err := svc.meta.GetTodo("todo-notify-unbound")
	assert.NoErr(t, err)
	assert.True(t, ok)
	if todo.JobID == "" {
		t.Fatalf("todo not dispatched: status=%s error=%q", todo.Status, todo.DispatchError)
	}
	_, ok = svc.Wait(todo.JobID)
	assert.True(t, ok)
	rec, ok, err := svc.meta.GetJob(todo.JobID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	rebuilt := fromRecord(rec)
	assert.False(t, rebuilt.Notify)
	assert.Eq(t, "", rebuilt.NotifySession)
}

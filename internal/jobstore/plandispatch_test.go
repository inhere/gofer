package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestPlanProjectKeyAndTodoDispatchFieldsRoundTrip (PLAN-02 P2): a plan carries the
// project its todos are dispatched into, and a todo carries the whole dispatch
// request it hands to Submit (assignee + project override + template/vars + verify +
// review + runner + cwd + timeout). Every one of them must survive the store
// unchanged — a dropped field here means a dispatched job silently runs with the
// wrong agent or in the wrong directory.
func TestPlanProjectKeyAndTodoDispatchFieldsRoundTrip(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertPlan(Plan{
		PlanID: "plan-proj", Title: "phase", Status: PlanOpen,
		ProjectKey: "shop-floor", CreatedAt: 1, UpdatedAt: 1,
	}))
	got, ok, err := s.GetPlan("plan-proj")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "shop-floor", got.ProjectKey)

	// A plan without a project reads back "" (no fabricated default).
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-noproj", Status: PlanOpen, CreatedAt: 2, UpdatedAt: 2}))
	bare, _, err := s.GetPlan("plan-noproj")
	assert.NoErr(t, err)
	assert.Eq(t, "", bare.ProjectKey)

	td := PlanTodo{
		TodoID: "todo-dispatch", PlanID: "plan-proj", Title: "step 1",
		Assignee: "omp", ProjectKey: "elsewhere", Template: "impl-batch",
		Vars:   map[string]string{"tasks": "T-1"},
		Verify: []string{"go", "test", "./..."},
		Review: true, Runner: "w-kzl", Cwd: "sub/dir", TimeoutSec: 1800,
		CreatedAt: 10, UpdatedAt: 10,
	}
	assert.NoErr(t, s.InsertTodo(td))

	back, ok, err := s.GetTodo("todo-dispatch")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "omp", back.Assignee)
	assert.Eq(t, "elsewhere", back.ProjectKey)
	assert.Eq(t, "impl-batch", back.Template)
	assert.Eq(t, "T-1", back.Vars["tasks"])
	assert.Eq(t, 3, len(back.Verify))
	assert.Eq(t, "./...", back.Verify[2])
	assert.True(t, back.Review)
	assert.Eq(t, "w-kzl", back.Runner)
	assert.Eq(t, "sub/dir", back.Cwd)
	assert.Eq(t, 1800, back.TimeoutSec)
	assert.Eq(t, "", back.DispatchError)

	// The plan's todo listing carries the same fields (the plan detail is where a
	// reader sees who a todo is assigned to).
	list, err := s.ListTodosByPlan("plan-proj")
	assert.NoErr(t, err)
	assert.Len(t, list, 1)
	assert.Eq(t, "omp", list[0].Assignee)
	assert.Eq(t, "T-1", list[0].Vars["tasks"])

	// Undispatched defaults stay empty rather than inventing a runner/cwd.
	plain := PlanTodo{TodoID: "todo-plain2", PlanID: "plan-proj", Title: "plain", CreatedAt: 11, UpdatedAt: 11}
	assert.NoErr(t, s.InsertTodo(plain))
	gotPlain, _, err := s.GetTodo("todo-plain2")
	assert.NoErr(t, err)
	assert.Eq(t, "", gotPlain.Assignee)
	assert.Eq(t, 0, gotPlain.TimeoutSec)
	assert.False(t, gotPlain.Review)
	assert.Len(t, gotPlain.Verify, 0)
	assert.Len(t, gotPlain.Vars, 0)
}

// TestTodoDispatchPatchAndError (PLAN-02 P2): UpdateTodoPatch changes only the fields
// it is given (nil = keep, an empty value = clear), and the dispatch failure message
// is a first-class todo field that a successful dispatch clears again.
func TestTodoDispatchPatchAndError(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-p", Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))
	assert.NoErr(t, s.InsertTodo(PlanTodo{
		TodoID: "todo-p", PlanID: "plan-p", Title: "step", Note: "keep me",
		Assignee: "codex", Runner: "local", TimeoutSec: 60,
		Vars:   map[string]string{"a": "1"},
		Verify: []string{"true"},
		Review: true, CreatedAt: 1, UpdatedAt: 1,
	}))

	assignee, cwd, timeout := "omp", "sub", 900
	var (
		tpl  = "impl-batch"
		proj = "shop-floor"
	)
	ok, err := s.UpdateTodoPatch("todo-p", TodoPatch{
		Assignee: &assignee, Cwd: &cwd, TimeoutSec: &timeout,
		Template: &tpl, ProjectKey: &proj,
		Vars:   map[string]string{"b": "2"},
		Verify: []string{"go", "build", "./..."},
	})
	assert.NoErr(t, err)
	assert.True(t, ok)

	got, _, err := s.GetTodo("todo-p")
	assert.NoErr(t, err)
	assert.Eq(t, "omp", got.Assignee)
	assert.Eq(t, "sub", got.Cwd)
	assert.Eq(t, 900, got.TimeoutSec)
	assert.Eq(t, "impl-batch", got.Template)
	assert.Eq(t, "shop-floor", got.ProjectKey)
	assert.Eq(t, "2", got.Vars["b"])
	assert.Eq(t, 3, len(got.Verify))
	assert.Eq(t, "./...", got.Verify[2])
	// Fields the patch did not mention keep their value.
	assert.Eq(t, "local", got.Runner)
	assert.True(t, got.Review)
	assert.Eq(t, "keep me", got.Note)

	// An empty patch is a no-op (not an error): the caller sent nothing to change.
	ok, err = s.UpdateTodoPatch("todo-p", TodoPatch{})
	assert.NoErr(t, err)
	assert.True(t, ok)

	// An empty value CLEARS: that is how `--cwd ""` / a runner taken back reads.
	empty := ""
	ok, err = s.UpdateTodoPatch("todo-p", TodoPatch{Cwd: &empty, Runner: &empty, Vars: map[string]string{}, Verify: []string{}})
	assert.NoErr(t, err)
	assert.True(t, ok)
	got, _, err = s.GetTodo("todo-p")
	assert.NoErr(t, err)
	assert.Eq(t, "", got.Cwd)
	assert.Eq(t, "", got.Runner)
	assert.Len(t, got.Vars, 0)
	assert.Len(t, got.Verify, 0)

	// Dispatch failure text is stored and cleared; an unknown todo reports false.
	ok, err = s.SetTodoDispatchError("todo-p", "no project")
	assert.NoErr(t, err)
	assert.True(t, ok)
	got, _, err = s.GetTodo("todo-p")
	assert.NoErr(t, err)
	assert.Eq(t, "no project", got.DispatchError)
	ok, err = s.SetTodoDispatchError("todo-p", "")
	assert.NoErr(t, err)
	assert.True(t, ok)
	got, _, err = s.GetTodo("todo-p")
	assert.NoErr(t, err)
	assert.Eq(t, "", got.DispatchError)

	ok, err = s.SetTodoDispatchError("todo-nope", "x")
	assert.NoErr(t, err)
	assert.False(t, ok)
	ok, err = s.UpdateTodoPatch("todo-nope", TodoPatch{Assignee: &assignee})
	assert.NoErr(t, err)
	assert.False(t, ok)
}

// TestTodoReadyStatusLifecycle (PLAN-02 P2, decision 5): `ready` sits between
// `pending` (backlog, never dispatched) and `doing` (a job is running). Entering it
// changes nothing but the queue position — it is not a start, so started_at stays
// untouched — while a redo (done/doing → ready) drops the completed stamp so a
// re-queued item is not shown as finished.
func TestTodoReadyStatusLifecycle(t *testing.T) {
	s := openTest(t)
	assert.True(t, ValidTodoStatus(TodoReady))
	assert.False(t, ValidTodoStatus("queued"))

	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-ready", Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))
	assert.NoErr(t, s.InsertTodo(PlanTodo{TodoID: "todo-ready", PlanID: "plan-ready", Title: "step", CreatedAt: 1, UpdatedAt: 1}))

	newest := func() PlanTodo {
		td, ok, err := s.GetTodo("todo-ready")
		assert.NoErr(t, err)
		assert.True(t, ok)
		return td
	}

	// pending → ready: queued, not started, not finished.
	ok, err := s.UpdateTodoStatus("todo-ready", TodoReady, nil)
	assert.NoErr(t, err)
	assert.True(t, ok)
	td := newest()
	assert.Eq(t, TodoReady, td.Status)
	assert.False(t, td.Done)
	assert.Eq(t, int64(0), td.StartedAt)
	assert.Eq(t, int64(0), td.DoneAt)

	// → doing stamps the start.
	ok, err = s.UpdateTodoStatus("todo-ready", TodoDoing, nil)
	assert.NoErr(t, err)
	assert.True(t, ok)
	td = newest()
	assert.Eq(t, TodoDoing, td.Status)
	assert.True(t, td.StartedAt > 0)

	// → done stamps the finish.
	ok, err = s.UpdateTodoStatus("todo-ready", TodoDone, nil)
	assert.NoErr(t, err)
	assert.True(t, ok)
	td = newest()
	assert.True(t, td.Done)
	assert.True(t, td.DoneAt > 0)
	startedAt := td.StartedAt

	// done → ready (redo, design §四.1): back in the queue — the done flag drops and
	// the completion stamp is cleared, while the original start stays (this item HAS
	// been worked on before, and its start is the fact worth keeping).
	ok, err = s.UpdateTodoStatus("todo-ready", TodoReady, nil)
	assert.NoErr(t, err)
	assert.True(t, ok)
	td = newest()
	assert.Eq(t, TodoReady, td.Status)
	assert.False(t, td.Done)
	assert.Eq(t, int64(0), td.DoneAt)
	assert.Eq(t, startedAt, td.StartedAt)

	// → pending is the full reset (both stamps cleared).
	ok, err = s.UpdateTodoStatus("todo-ready", TodoPending, nil)
	assert.NoErr(t, err)
	assert.True(t, ok)
	td = newest()
	assert.Eq(t, TodoPending, td.Status)
	assert.Eq(t, int64(0), td.StartedAt)
	assert.Eq(t, int64(0), td.DoneAt)
}

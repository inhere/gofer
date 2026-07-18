package jobstore

import (
	"database/sql"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestTodoInsertGetListDoneAndDelete(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-todo", Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-other", Status: PlanOpen, CreatedAt: 2, UpdatedAt: 2}))

	plain := PlanTodo{
		TodoID: "todo-plain", PlanID: "plan-todo", Title: "plain",
		Done: false, Sort: 20, CreatedAt: 20, UpdatedAt: 20,
	}
	bound := PlanTodo{
		TodoID: "todo-bound", PlanID: "plan-todo", JobID: "job-1", Title: "bound",
		Done: true, Sort: 10, CreatedAt: 10, UpdatedAt: 10,
	}
	other := PlanTodo{
		TodoID: "todo-other", PlanID: "plan-other", Title: "other",
		Sort: 1, CreatedAt: 1, UpdatedAt: 1,
	}
	assert.NoErr(t, s.InsertTodo(plain))
	assert.NoErr(t, s.InsertTodo(bound))
	assert.NoErr(t, s.InsertTodo(other))

	gotPlain, ok, err := s.GetTodo("todo-plain")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", gotPlain.JobID)
	assert.False(t, gotPlain.Done)
	assert.Eq(t, 20, gotPlain.Sort)
	var storedJobID sql.NullString
	assert.NoErr(t, s.db.QueryRow(`SELECT job_id FROM plan_todos WHERE todo_id='todo-plain'`).Scan(&storedJobID))
	assert.False(t, storedJobID.Valid)

	gotBound, ok, err := s.GetTodo("todo-bound")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "job-1", gotBound.JobID)
	assert.True(t, gotBound.Done)

	list, err := s.ListTodosByPlan("plan-todo")
	assert.NoErr(t, err)
	assert.Len(t, list, 2)
	assert.Eq(t, "todo-bound", list[0].TodoID)
	assert.Eq(t, "todo-plain", list[1].TodoID)
	for _, todo := range list {
		assert.Eq(t, "plan-todo", todo.PlanID)
	}
	none, err := s.ListTodosByPlan("missing-plan")
	assert.NoErr(t, err)
	assert.NotNil(t, none)
	assert.Len(t, none, 0)

	changed, err := s.SetTodoDone("todo-plain", true)
	assert.NoErr(t, err)
	assert.True(t, changed)
	gotPlain, _, err = s.GetTodo("todo-plain")
	assert.NoErr(t, err)
	assert.True(t, gotPlain.Done)

	changed, err = s.SetTodoDone("todo-plain", false)
	assert.NoErr(t, err)
	assert.True(t, changed)
	gotPlain, _, err = s.GetTodo("todo-plain")
	assert.NoErr(t, err)
	assert.False(t, gotPlain.Done)

	changed, err = s.SetTodoDone("missing-todo", true)
	assert.NoErr(t, err)
	assert.False(t, changed)

	deleted, err := s.DeleteTodo("todo-bound")
	assert.NoErr(t, err)
	assert.True(t, deleted)
	_, ok, err = s.GetTodo("todo-bound")
	assert.NoErr(t, err)
	assert.False(t, ok)
	deleted, err = s.DeleteTodo("todo-bound")
	assert.NoErr(t, err)
	assert.False(t, deleted)
}

func TestInsertTodoRequiresIDs(t *testing.T) {
	s := openTest(t)
	assert.Err(t, s.InsertTodo(PlanTodo{PlanID: "plan-1", CreatedAt: 1, UpdatedAt: 1}))
	assert.Err(t, s.InsertTodo(PlanTodo{TodoID: "todo-1", CreatedAt: 1, UpdatedAt: 1}))
}

// TestTodoLifecycleTimestampsAndNote (Part C §C2): status transitions stamp
// started_at/done_at automatically, note updates independently, and the legacy
// done flag stays in lockstep with status.
func TestTodoLifecycleTimestampsAndNote(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-lc", Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))
	assert.NoErr(t, s.InsertTodo(PlanTodo{
		TodoID: "todo-lc", PlanID: "plan-lc", Title: "step 1", CreatedAt: 1, UpdatedAt: 1,
	}))

	// Fresh insert defaults to pending, no timestamps.
	td, ok, err := s.GetTodo("todo-lc")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, TodoPending, td.Status)
	assert.Eq(t, int64(0), td.StartedAt)
	assert.Eq(t, int64(0), td.DoneAt)

	// → doing stamps started_at.
	okUp, err := s.UpdateTodoStatus("todo-lc", TodoDoing, nil)
	assert.NoErr(t, err)
	assert.True(t, okUp)
	td, _, _ = s.GetTodo("todo-lc")
	assert.Eq(t, TodoDoing, td.Status)
	assert.True(t, td.StartedAt > 0)
	assert.False(t, td.Done)
	startedAt := td.StartedAt

	// → done stamps done_at, keeps started_at, syncs Done.
	note := "验收通过"
	okUp, err = s.UpdateTodoStatus("todo-lc", TodoDone, &note)
	assert.NoErr(t, err)
	assert.True(t, okUp)
	td, _, _ = s.GetTodo("todo-lc")
	assert.Eq(t, TodoDone, td.Status)
	assert.True(t, td.Done)
	assert.True(t, td.DoneAt > 0)
	assert.Eq(t, startedAt, td.StartedAt)
	assert.Eq(t, "验收通过", td.Note)

	// done → doing (redo): done_at cleared, original started_at kept, note kept.
	okUp, err = s.UpdateTodoStatus("todo-lc", TodoDoing, nil)
	assert.NoErr(t, err)
	assert.True(t, okUp)
	td, _, _ = s.GetTodo("todo-lc")
	assert.Eq(t, TodoDoing, td.Status)
	assert.False(t, td.Done)
	assert.Eq(t, int64(0), td.DoneAt)
	assert.Eq(t, startedAt, td.StartedAt)
	assert.Eq(t, "验收通过", td.Note)

	// Note-only update (status "") keeps the lifecycle state.
	note2 := "returning to it"
	okUp, err = s.UpdateTodoStatus("todo-lc", "", &note2)
	assert.NoErr(t, err)
	assert.True(t, okUp)
	td, _, _ = s.GetTodo("todo-lc")
	assert.Eq(t, TodoDoing, td.Status)
	assert.Eq(t, "returning to it", td.Note)

	// → skipped is terminal but NOT done.
	okUp, err = s.UpdateTodoStatus("todo-lc", TodoSkipped, nil)
	assert.NoErr(t, err)
	assert.True(t, okUp)
	td, _, _ = s.GetTodo("todo-lc")
	assert.Eq(t, TodoSkipped, td.Status)
	assert.False(t, td.Done)
	assert.True(t, td.DoneAt > 0)

	// → pending resets both timestamps.
	okUp, err = s.UpdateTodoStatus("todo-lc", TodoPending, nil)
	assert.NoErr(t, err)
	assert.True(t, okUp)
	td, _, _ = s.GetTodo("todo-lc")
	assert.Eq(t, TodoPending, td.Status)
	assert.Eq(t, int64(0), td.StartedAt)
	assert.Eq(t, int64(0), td.DoneAt)

	// Invalid status rejected; unknown todo reports ok=false.
	_, err = s.UpdateTodoStatus("todo-lc", "bogus", nil)
	assert.Err(t, err)
	okUp, err = s.UpdateTodoStatus("todo-nope", TodoDone, nil)
	assert.NoErr(t, err)
	assert.False(t, okUp)

	// Legacy SetTodoDone keeps working through the lifecycle mapping.
	okUp, err = s.SetTodoDone("todo-lc", true)
	assert.NoErr(t, err)
	assert.True(t, okUp)
	td, _, _ = s.GetTodo("todo-lc")
	assert.Eq(t, TodoDone, td.Status)
	assert.True(t, td.Done)
}

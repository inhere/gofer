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

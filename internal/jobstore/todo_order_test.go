package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestTodoOrderInsertion pins the insertion-order tie-break: todos created in the
// same second with the same sort come back in the order they were added, not by
// their random id suffix (ids below are deliberately out of lexical order).
func TestTodoOrderInsertion(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-order", Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))
	ids := []string{"todo-x-9f", "todo-x-1a", "todo-x-c3", "todo-x-05", "todo-x-7e"}
	for _, id := range ids {
		assert.NoErr(t, s.InsertTodo(PlanTodo{TodoID: id, PlanID: "plan-order", Title: id, CreatedAt: 100, UpdatedAt: 100}))
	}
	got, err := s.ListTodosByPlan("plan-order")
	assert.NoErr(t, err)
	if len(got) != len(ids) {
		t.Fatalf("got %d todos, want %d", len(got), len(ids))
	}
	for i, td := range got {
		if td.TodoID != ids[i] {
			t.Fatalf("todo[%d]=%s, want insertion order %v", i, td.TodoID, ids)
		}
	}
}

// TestTodoOrderPlansNewestFirst pins the same tie-break for plans: plans created in
// the same second list newest-inserted first, not by plan id.
func TestTodoOrderPlansNewestFirst(t *testing.T) {
	s := openTest(t)
	ids := []string{"plan-zz-first", "plan-aa-second", "plan-mm-third"}
	for _, id := range ids {
		assert.NoErr(t, s.InsertPlan(Plan{PlanID: id, Status: PlanOpen, CreatedAt: 100, UpdatedAt: 100}))
	}
	got, err := s.ListPlans("", 0)
	assert.NoErr(t, err)
	want := []string{"plan-mm-third", "plan-aa-second", "plan-zz-first"}
	if len(got) != len(want) {
		t.Fatalf("got %d plans, want %d", len(got), len(want))
	}
	for i, p := range got {
		if p.PlanID != want[i] {
			t.Fatalf("plan[%d]=%s, want %v", i, p.PlanID, want)
		}
	}
}

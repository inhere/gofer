package jobstore

import (
	"fmt"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestPlanCompletionRule(t *testing.T) {
	pct := func(p *int) int {
		if p == nil {
			return -1
		}
		return *p
	}
	cases := []struct {
		name        string
		jobs        PlanCounts
		todos       PlanTodoCounts
		basis       string
		done, total int
		percent     int // -1 = nil
	}{
		{"nothing to measure", PlanCounts{}, PlanTodoCounts{}, CompletionNone, 0, 0, -1},
		{"jobs only", PlanCounts{Total: 11, Done: 10, Failed: 1}, PlanTodoCounts{}, CompletionJobs, 10, 11, 91},
		{"todos only, skipped counts as complete", PlanCounts{}, PlanTodoCounts{Total: 6, Done: 4, Skipped: 1, Doing: 1}, CompletionTodos, 5, 6, 83},
		{"todos win over jobs", PlanCounts{Total: 11, Done: 1, Failed: 10}, PlanTodoCounts{Total: 2, Done: 2}, CompletionTodos, 2, 2, 100},
		{"all todos pending is 0%, not none", PlanCounts{}, PlanTodoCounts{Total: 3, Pending: 3}, CompletionTodos, 0, 3, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RollupPlanCompletion(tc.jobs, tc.todos)
			if got.Basis != tc.basis || got.Done != tc.done || got.Total != tc.total || pct(got.Percent) != tc.percent {
				t.Fatalf("got %+v (percent %d), want basis=%s %d/%d percent=%d", got, pct(got.Percent), tc.basis, tc.done, tc.total, tc.percent)
			}
		})
	}
}

func TestPlanCompletionCountTodos(t *testing.T) {
	got := CountTodos([]PlanTodo{
		{Status: TodoPending}, {Status: TodoDoing}, {Status: TodoDone},
		{Status: TodoSkipped}, {Status: TodoDone}, {Status: ""},
	})
	want := PlanTodoCounts{Total: 6, Pending: 2, Doing: 1, Done: 2, Skipped: 1}
	if got != want {
		t.Fatalf("CountTodos = %+v, want %+v", got, want)
	}
}

func TestPlanCompletionTodoCountsByPlan(t *testing.T) {
	s := openTest(t)
	for _, id := range []string{"plan-cnt-a", "plan-cnt-b", "plan-cnt-c", "plan-cnt-other"} {
		assert.NoErr(t, s.InsertPlan(Plan{PlanID: id, Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))
	}
	addTodo := func(plan, id, status string) {
		assert.NoErr(t, s.InsertTodo(PlanTodo{TodoID: id, PlanID: plan, Title: id, CreatedAt: 10, UpdatedAt: 10}))
		if status != TodoPending {
			ok, err := s.UpdateTodoStatus(id, status, nil)
			assert.NoErr(t, err)
			assert.True(t, ok)
		}
	}
	addTodo("plan-cnt-a", "todo-a1", TodoDone)
	addTodo("plan-cnt-a", "todo-a2", TodoDoing)
	addTodo("plan-cnt-a", "todo-a3", TodoPending)
	addTodo("plan-cnt-b", "todo-b1", TodoSkipped)
	addTodo("plan-cnt-other", "todo-o1", TodoDone) // not requested below

	// More ids than one chunk, so the chunked IN (...) path is exercised.
	ids := []string{"plan-cnt-a", "plan-cnt-b", "plan-cnt-c"}
	for i := 0; i < todoCountsChunk+20; i++ {
		ids = append(ids, fmt.Sprintf("plan-missing-%04d", i))
	}
	got, err := s.PlanTodoCountsByPlan(ids)
	assert.NoErr(t, err)
	if a := got["plan-cnt-a"]; a != (PlanTodoCounts{Total: 3, Pending: 1, Doing: 1, Done: 1}) {
		t.Fatalf("plan-cnt-a = %+v", a)
	}
	if b := got["plan-cnt-b"]; b != (PlanTodoCounts{Total: 1, Skipped: 1}) {
		t.Fatalf("plan-cnt-b = %+v", b)
	}
	if c, ok := got["plan-cnt-c"]; ok || c != (PlanTodoCounts{}) {
		t.Fatalf("plan without todos should be absent/zero, got %+v (present=%v)", c, ok)
	}
	if _, ok := got["plan-cnt-other"]; ok {
		t.Fatal("unrequested plan must not be counted")
	}
	empty, err := s.PlanTodoCountsByPlan(nil)
	assert.NoErr(t, err)
	if len(empty) != 0 {
		t.Fatalf("no ids should yield an empty map, got %v", empty)
	}
}

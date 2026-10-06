package work

import (
	"errors"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestToTodoNewPlanLinksBothWays(t *testing.T) {
	svc, st, _ := newSvc(t)
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "补导出按钮", Goal: "订单页要能导出", By: "human:a"})
	assert.NoErr(t, err)

	res, err := svc.ToTodo(w.ID, ToTodoInput{By: "human:a"})
	assert.NoErr(t, err)
	assert.True(t, res.PlanCreated)
	assert.NotEmpty(t, res.TodoID)

	todo, ok, err := st.GetTodo(res.TodoID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "补导出按钮", todo.Title)
	assert.Contains(t, todo.Note, "订单页要能导出")
	assert.Eq(t, res.PlanID, todo.PlanID)
	plan, _, _ := st.GetPlan(res.PlanID)
	assert.Eq(t, "补导出按钮", plan.Title)

	links, _ := st.ListWorkLinks(w.ID)
	kinds := map[string]string{}
	for _, l := range links {
		kinds[l.Kind] = l.Ref
	}
	assert.Eq(t, res.TodoID, kinds[jobstore.WorkLinkTodo])
	assert.Eq(t, res.PlanID, kinds[jobstore.WorkLinkPlan])
}

func TestToTodoExistingPlanAndDuplicate(t *testing.T) {
	svc, st, _ := newSvc(t)
	w, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "t1", By: "human:a"})
	assert.NoErr(t, st.InsertPlan(jobstore.Plan{PlanID: "plan-existing-1", Title: "P", Status: jobstore.PlanOpen}))

	res, err := svc.ToTodo(w.ID, ToTodoInput{PlanID: "plan-existing-1"})
	assert.NoErr(t, err)
	assert.False(t, res.PlanCreated)
	assert.Eq(t, "plan-existing-1", res.PlanID)

	_, err = svc.ToTodo(w.ID, ToTodoInput{PlanID: "plan-existing-1"})
	var dup *AlreadyTodoError
	assert.True(t, errors.As(err, &dup))
	assert.Eq(t, res.TodoID, dup.TodoID)
	assert.Eq(t, "plan-existing-1", dup.PlanID)
}

func TestToTodoUnknownPlanAndItem(t *testing.T) {
	svc, st, _ := newSvc(t)
	w, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "t1", By: "human:a"})
	_, err := svc.ToTodo(w.ID, ToTodoInput{PlanID: "plan-nope-0001"})
	assert.True(t, errors.Is(err, ErrPlanNotFound))
	// a refused conversion leaves no link behind
	links, _ := st.ListWorkLinks(w.ID)
	assert.Len(t, links, 0)
	_, err = svc.ToTodo("w-missing", ToTodoInput{})
	assert.True(t, errors.Is(err, jobstore.ErrWorkItemNotFound))
}

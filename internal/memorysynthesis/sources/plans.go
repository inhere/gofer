package sources

import (
	"context"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/memorysynthesis"
)

// PlanReader is the part of *jobstore.Store the plan adapter reads.
type PlanReader interface {
	ListPlans(jobstore.PlanFilter) ([]jobstore.Plan, error)
	ListTodosByPlan(planID string) ([]jobstore.PlanTodo, error)
}

// Plans collects plans of the project that changed inside the window and the todos that
// finished inside it, with their outcome note (the conclusion) and acceptance summary.
// Only plans of the project are read, and a todo that names another project is skipped.
type Plans struct {
	Config
	Store    PlanReader
	MaxPlans int
}

func (s Plans) Collect(_ context.Context, project, day string) ([]memorysynthesis.Item, error) {
	w, err := s.window(day)
	if err != nil {
		return nil, err
	}
	maxPlans := s.MaxPlans
	if maxPlans <= 0 {
		maxPlans = 100
	}
	plans, err := s.Store.ListPlans(jobstore.PlanFilter{ProjectKey: project, Limit: maxPlans})
	if err != nil {
		return nil, err
	}
	var out []memorysynthesis.Item
	for _, p := range plans {
		if p.ProjectKey != project {
			continue
		}
		todos, err := s.Store.ListTodosByPlan(p.PlanID)
		if err != nil {
			return nil, err
		}
		var changed []memorysynthesis.Item
		for _, t := range todos {
			if t.ProjectKey != "" && t.ProjectKey != project {
				continue
			}
			if !w.Contains(t.DoneAt) && !w.Contains(t.UpdatedAt) {
				continue
			}
			changed = append(changed, memorysynthesis.Item{
				Kind: KindPlanTodo, OccurredAt: stamp(w, max(t.DoneAt, t.UpdatedAt)), Title: t.Title,
				SourceRef: "todo:" + t.TodoID,
				Text:      kv("plan", p.PlanID, "status", t.Status, "conclusion", t.Note, "acceptance", t.Acceptance),
			})
		}
		if !w.Contains(p.UpdatedAt) && len(changed) == 0 {
			continue
		}
		out = append(out, memorysynthesis.Item{
			Kind: KindPlan, OccurredAt: stamp(w, p.UpdatedAt), Title: p.Title, SourceRef: "plan:" + p.PlanID,
			Text: kv("status", p.Status, "description", p.Description),
		})
		out = append(out, changed...)
	}
	return out, nil
}

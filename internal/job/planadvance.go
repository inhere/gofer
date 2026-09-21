package job

// planadvance.go is PLAN-03: the todo CHAIN. A plan whose items declare `after`
// dependencies advances itself — a finished item queues the ones waiting for it — so
// "建好链，只管验收" is one `plan run` instead of one `job run --todo` per item.
//
// The rules (design §一.2) are deliberately small:
//
//   - advancePlan is the ONE mover. It runs after a todo became done/skipped (the
//     terminal hooks of the job that carried it, or a human's `plan set-todo`), and
//     it only ever touches items that are still `pending`.
//   - an item with NO dependencies is a ROOT: the chain never starts it by itself
//     (adding a todo must not run work), so `plan run` — or a human setting it ready —
//     is what starts a chain. That is also why advancePlan skips empty `after`.
//   - a FAILED chain job parks the whole plan (`plan.blocked`) instead of silently
//     skipping ahead: the checklist's later items assume this one delivered. A
//     takeover (auto-resume / fallback) is not a failure of the chain — the work is
//     still moving — so only a job that really ended failed blocks.
//   - every transition is a single conditional UPDATE plus a best-effort event, so
//     two terminal outcomes racing on the same plan cannot double-queue an item.

import (
	"fmt"
	"log/slog"

	"github.com/inhere/gofer/internal/jobstore"
)

// planScopePrefix is the scope-id prefix of a plan's own event stream (`plan:<id>`),
// owned by PlanEventScope and parsed back by the notification renderer (which turns the
// scope into the /plans/{id} link of an IM message).
const planScopePrefix = "plan:"

// RunPlan starts a plan's chain: `plan run <plan>` (PLAN-03). It releases a pause and
// a block, then queues every pending item whose dependencies are satisfied AND that has
// an assignee — roots included, which is exactly what advancePlan refuses to do on its
// own. The auto flag is NOT consulted here: a human typing `plan run` is asking for the
// ready work to start, and `--no-auto` exists to keep the CHAIN from starting an item,
// not to stop an explicit run.
func (s *Service) RunPlan(planID, by string) (jobstore.Plan, error) {
	p, ok, err := s.meta.GetPlan(planID)
	if err != nil {
		return jobstore.Plan{}, err
	}
	if !ok {
		return jobstore.Plan{}, fmt.Errorf("%w: unknown plan %q", ErrInvalidRequest, planID)
	}
	if p.Paused {
		if err := s.meta.SetPlanPaused(planID, false); err != nil {
			return jobstore.Plan{}, err
		}
	}
	if err := s.meta.ClearPlanBlocked(planID); err != nil {
		return jobstore.Plan{}, err
	}
	todos, err := s.meta.ListTodosByPlan(planID)
	if err != nil {
		return jobstore.Plan{}, err
	}
	byID := todosByID(todos)
	var queued []jobstore.PlanTodo
	for _, t := range todos {
		if t.Status != jobstore.TodoPending || !depsSatisfied(t, byID) {
			continue
		}
		if t.Assignee == "" {
			s.todoUnassigned(p, t)
			continue
		}
		changed, err := s.meta.SetTodoReadyIfPending(t.TodoID)
		if err != nil {
			slog.Warn("plan run: queue todo", "plan_id", planID, "todo_id", t.TodoID, "err", err)
			continue
		}
		if !changed {
			continue
		}
		s.recordPlanAdvanced(p, t)
		queued = append(queued, t)
	}
	// Dispatch AFTER every queue write, so an item that fails to start does not leave
	// the rest of the roots unqueued.
	for _, t := range queued {
		if _, err := s.MaybeDispatchTodo(t.TodoID, by); err != nil {
			slog.Warn("plan run: dispatch todo", "plan_id", planID, "todo_id", t.TodoID, "err", err)
		}
	}
	s.advancePlan(planID, by)
	return s.planOrEmpty(planID)
}

// PausePlan holds a plan's automatic chain advance: a todo that finishes while paused
// stays finished, but nothing after it starts (PLAN-03). advancePlan records
// plan.advance_paused instead of moving, so the pause is visible in the event stream.
func (s *Service) PausePlan(planID string) (jobstore.Plan, error) {
	if _, ok, err := s.meta.GetPlan(planID); err != nil {
		return jobstore.Plan{}, err
	} else if !ok {
		return jobstore.Plan{}, fmt.Errorf("%w: unknown plan %q", ErrInvalidRequest, planID)
	}
	if err := s.meta.SetPlanPaused(planID, true); err != nil {
		return jobstore.Plan{}, err
	}
	return s.planOrEmpty(planID)
}

// ResumePlan releases a pause AND a block, then advances: it is the human's "carry on"
// for a plan that stopped for either reason (PLAN-03). The item a block named keeps its
// status — resume continues from wherever the chain is, it does not re-run the failed
// item (that is `plan set-todo <todo> --status ready`).
func (s *Service) ResumePlan(planID, by string) (jobstore.Plan, error) {
	if _, ok, err := s.meta.GetPlan(planID); err != nil {
		return jobstore.Plan{}, err
	} else if !ok {
		return jobstore.Plan{}, fmt.Errorf("%w: unknown plan %q", ErrInvalidRequest, planID)
	}
	if err := s.meta.SetPlanPaused(planID, false); err != nil {
		return jobstore.Plan{}, err
	}
	if err := s.meta.ClearPlanBlocked(planID); err != nil {
		return jobstore.Plan{}, err
	}
	s.advancePlan(planID, by)
	return s.planOrEmpty(planID)
}

// PlanTodoChanged reacts to a todo STATUS write (PLAN-03). Every write path that moves
// an item calls it after storing what the caller sent, because the two things it does
// depend on the item having moved:
//
//   - an item that was the one a plan blocked on releases the block (ready/skipped/done
//     are all a human's answer — "I fixed it", "skip it", "it is done after all");
//   - the chain then advances, so `plan set-todo <todo> --status done|skipped` moves
//     the plan exactly like a job's terminal hook does.
//
// It never fails its caller: a todo write must not be undone by a plan write.
func (s *Service) PlanTodoChanged(todoID, status, by string) {
	todo, ok, err := s.meta.GetTodo(todoID)
	if err != nil || !ok {
		return
	}
	p, ok, err := s.meta.GetPlan(todo.PlanID)
	if err != nil || !ok {
		return
	}
	switch status {
	case jobstore.TodoReady, jobstore.TodoSkipped, jobstore.TodoDone:
		if p.BlockedTodo == todoID {
			if err := s.meta.ClearPlanBlocked(todo.PlanID); err != nil {
				slog.Warn("unblock plan", "plan_id", todo.PlanID, "todo_id", todoID, "err", err)
				return
			}
		}
	}
	s.advancePlan(todo.PlanID, by)
}

// advancePlan moves a plan's chain forward after one of its items finished. It is the
// single place the advance rule lives; the job terminal hooks and the todo write paths
// all funnel into it.
func (s *Service) advancePlan(planID, by string) {
	p, ok, err := s.meta.GetPlan(planID)
	if err != nil || !ok {
		return
	}
	if p.Paused {
		s.RecordScopedEvent(PlanEventScope(planID), EventPlanAdvancePaused, p.ProjectKey,
			map[string]any{"plan_id": planID})
		return
	}
	todos, err := s.meta.ListTodosByPlan(planID)
	if err != nil {
		slog.Warn("advance plan: list todos", "plan_id", planID, "err", err)
		return
	}
	byID := todosByID(todos)
	for _, t := range todos {
		// Only an item that is WAITING is a candidate: `ready`/`doing` are already
		// moving, and done/skipped are finished. An empty `after` is a root, which the
		// chain never starts (see the file comment).
		if t.Status != jobstore.TodoPending || len(t.After) == 0 || !depsSatisfied(t, byID) {
			continue
		}
		if t.Assignee == "" {
			s.todoUnassigned(p, t)
			continue
		}
		if !t.Auto {
			continue
		}
		changed, err := s.meta.SetTodoReadyIfPending(t.TodoID)
		if err != nil {
			slog.Warn("advance plan: queue todo", "plan_id", planID, "todo_id", t.TodoID, "err", err)
			continue
		}
		if !changed {
			continue
		}
		s.recordPlanAdvanced(p, t)
		if _, err := s.MaybeDispatchTodo(t.TodoID, by); err != nil {
			slog.Warn("advance plan: dispatch todo", "plan_id", planID, "todo_id", t.TodoID, "err", err)
		}
	}
	if len(todos) == 0 || !todosFinished(todos) || p.Status == jobstore.PlanDone {
		return
	}
	if err := s.meta.SetPlanStatus(planID, jobstore.PlanDone, -1); err != nil {
		slog.Warn("advance plan: complete", "plan_id", planID, "err", err)
		return
	}
	s.RecordScopedEvent(PlanEventScope(planID), EventPlanCompleted, p.ProjectKey,
		map[string]any{"plan_id": planID})
}

// maybeBlockPlan parks a plan whose CHAIN job ended in failure (PLAN-03, design
// §一.2). It is called from the two places a job's outcome becomes final — the tail of
// finish(), after the takeover attempts, and the review verdict path — because the
// markers that decide whether the failure is being handed over are written by those
// attempts.
//
// It refuses to act when:
//   - the job carries no todo, or its status is not a failure (done/needs_review are
//     not failures of the chain);
//   - the job was taken over (auto_resumed_by / fell_back_to on the PERSISTED row):
//     the work is still moving, so the chain must not park on a failure that is
//     already being continued;
//   - the plan does not USE dependencies (no item has `after`): a flat checklist is
//     not a chain, and blocking it would be a surprise (design §一.2);
//   - the plan is already blocked on this very item (the second terminal event of a
//     re-run must not re-record it).
func (s *Service) maybeBlockPlan(snap JobResult) {
	if snap.TodoID == "" {
		return
	}
	switch snap.Status {
	case StatusFailed, StatusTimeout, StatusCancelled, StatusRejected:
	default:
		return
	}
	if rec, ok, err := s.meta.GetJob(snap.ID); err == nil && ok {
		if rec.AutoResumedBy != "" || rec.FellBackTo != "" {
			return
		}
	}
	todo, ok, err := s.meta.GetTodo(snap.TodoID)
	if err != nil || !ok {
		return
	}
	plan, ok, err := s.meta.GetPlan(todo.PlanID)
	if err != nil || !ok {
		return
	}
	if plan.Status == jobstore.PlanBlocked && plan.BlockedTodo == snap.TodoID {
		return
	}
	todos, err := s.meta.ListTodosByPlan(todo.PlanID)
	if err != nil {
		slog.Warn("block plan: list todos", "plan_id", todo.PlanID, "err", err)
		return
	}
	if !chainPlan(todos) {
		return
	}
	if err := s.meta.SetPlanBlocked(todo.PlanID, snap.TodoID); err != nil {
		slog.Warn("block plan", "plan_id", todo.PlanID, "todo_id", snap.TodoID, "err", err)
		return
	}
	reason := firstRunes(snap.Error, maxTodoNoteErrorRunes)
	if reason == "" {
		reason = snap.Status
	}
	s.RecordScopedEvent(PlanEventScope(todo.PlanID), EventPlanBlocked, plan.ProjectKey, map[string]any{
		"todo_id": snap.TodoID, "job": snap.ID, "reason": reason,
	})
}

// recordPlanAdvanced announces one queued item (PLAN-03): the plan's own event stream
// is where "why did this start" is answered without opening every job.
func (s *Service) recordPlanAdvanced(p jobstore.Plan, t jobstore.PlanTodo) {
	s.RecordScopedEvent(PlanEventScope(p.PlanID), EventPlanTodoAdvanced, p.ProjectKey, map[string]any{
		"todo_id": t.TodoID, "after": t.After,
	})
}

// todoUnassigned announces an item that came due with nobody to run it (PLAN-03): the
// chain cannot move it, and a human has to assign one — the event is the only signal,
// since nothing failed.
func (s *Service) todoUnassigned(p jobstore.Plan, t jobstore.PlanTodo) {
	s.RecordScopedEvent(PlanEventScope(p.PlanID), EventPlanTodoUnassigned, p.ProjectKey,
		map[string]any{"todo_id": t.TodoID})
}

// planOrEmpty re-reads a plan for a caller that just changed it; a vanished plan (the
// caller deleted it mid-call) reads as the zero value rather than an error — the change
// it asked for was already applied.
func (s *Service) planOrEmpty(planID string) (jobstore.Plan, error) {
	p, _, err := s.meta.GetPlan(planID)
	return p, err
}

// todosByID indexes a plan's items for the dependency lookups.
func todosByID(todos []jobstore.PlanTodo) map[string]jobstore.PlanTodo {
	out := make(map[string]jobstore.PlanTodo, len(todos))
	for _, t := range todos {
		out[t.TodoID] = t
	}
	return out
}

// depsSatisfied reports whether every item this todo waits for is done or skipped. An
// id the plan does not hold (a dependency deleted after the fact) counts as NOT
// satisfied: starting work whose prerequisite is unknown would silently break the
// chain's contract, and parking it is visible and fixable.
func depsSatisfied(t jobstore.PlanTodo, byID map[string]jobstore.PlanTodo) bool {
	for _, dep := range t.After {
		d, ok := byID[dep]
		if !ok {
			return false
		}
		if d.Status != jobstore.TodoDone && d.Status != jobstore.TodoSkipped {
			return false
		}
	}
	return true
}

// todosFinished reports whether every item of a plan is done or skipped (the plan's
// own completion condition).
func todosFinished(todos []jobstore.PlanTodo) bool {
	for _, t := range todos {
		if t.Status != jobstore.TodoDone && t.Status != jobstore.TodoSkipped {
			return false
		}
	}
	return true
}

// chainPlan reports whether a plan USES dependencies at all — the precondition for the
// blocked behaviour, so a flat checklist keeps behaving exactly as it did before
// PLAN-03 (a failed item on it is the human's business, not a parked chain).
func chainPlan(todos []jobstore.PlanTodo) bool {
	for _, t := range todos {
		if len(t.After) > 0 {
			return true
		}
	}
	return false
}

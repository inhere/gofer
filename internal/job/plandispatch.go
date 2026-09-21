package job

// plandispatch.go is PLAN-02 P2: "assign an agent to a todo and it runs".
//
// The rule is deliberately small — a todo that is BOTH `ready` and assigned, and has no
// live job, becomes a job right now — and it is evaluated at the WRITE paths (HTTP,
// MCP, CLI), never by a sweeper: the design's decision 5 is that a server restart does
// NOT re-dispatch the ready items it finds (that would surprise a human who parked an
// item on purpose, and it would run work nobody asked for at boot). Every write path
// calls MaybeDispatchTodo after it has stored what the caller sent; the explicit
// `plan dispatch` / gofer_dispatch_todo fallback calls DispatchTodo, which skips the
// status check but not the assignee/active-job ones.
//
// A dispatch is a normal Submit: the todo's fields are the request, so verify / review /
// runner / cwd / timeout / task book all behave exactly as if a human had typed them.
// The only thing this file adds to the job is provenance: Channel "plan" and the todo's
// plan linkage, which SUP-01 C then walks to its outcome.

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/jobstore"
)

// channelPlan is the submission channel (JobRequest.Channel) a dispatched todo stamps
// on the job it starts: "a plan item did this", readable in `job show` / the web
// timeline next to cli / web / mcp.
const channelPlan = "plan"

// activeTodoScanLimit bounds the jobs consulted when asking "does this todo already
// have a live job". The runs are newest-first and the live one is always the newest,
// so a small window is enough; the bound only exists so a pathological history cannot
// turn one PATCH into a full table scan.
const activeTodoScanLimit = 20

// TodoDispatch is the outcome of one dispatch attempt. Job is nil when no job was
// created — Reason then says whether a precondition was simply not met yet (the todo
// was not ready, nobody was assigned, a job is already running) or the Submit itself
// was refused (and the same text is stored on the todo as dispatch_error).
type TodoDispatch struct {
	Todo   jobstore.PlanTodo
	Job    *JobResult
	Reason string
}

// MaybeDispatchTodo dispatches a todo that is BOTH ready and assigned and has no live
// job (PLAN-02 P2, decision 5). It is what a todo WRITE path calls after storing what
// the caller sent, so the two conditions may arrive in either order. An unknown todo id
// is ErrInvalidRequest; everything else (not ready yet, nobody assigned, a job already
// running) is a normal "nothing to do" result with Reason set and no side effect.
func (s *Service) MaybeDispatchTodo(todoID, by string) (TodoDispatch, error) {
	return s.dispatchTodo(todoID, by, false)
}

// DispatchTodo is the EXPLICIT dispatch (`plan dispatch <todo>`, gofer_dispatch_todo):
// it ignores the todo's status (a `pending` item is a legitimate target — that is the
// whole point of the fallback) but still requires an assignee and no live job, and it
// reports a missing assignee as ErrInvalidRequest rather than dispatching nothing.
func (s *Service) DispatchTodo(todoID, by string) (TodoDispatch, error) {
	return s.dispatchTodo(todoID, by, true)
}

func (s *Service) dispatchTodo(todoID, by string, explicit bool) (TodoDispatch, error) {
	todo, ok, err := s.meta.GetTodo(todoID)
	if err != nil {
		return TodoDispatch{}, err
	}
	if !ok {
		return TodoDispatch{}, fmt.Errorf("%w: unknown todo_id %q", ErrInvalidRequest, todoID)
	}
	if !explicit && todo.Status != jobstore.TodoReady {
		return TodoDispatch{Todo: todo, Reason: fmt.Sprintf("todo is %s, not ready", todo.Status)}, nil
	}
	if todo.Assignee == "" {
		if explicit {
			return TodoDispatch{Todo: todo}, fmt.Errorf(
				"%w: todo %q has no assignee; set one with --assign", ErrInvalidRequest, todoID)
		}
		return TodoDispatch{Todo: todo, Reason: "todo has no assignee"}, nil
	}
	// A live job means "someone is already on this": a failed run leaves the todo
	// `doing`, and a delivery awaiting review is still the previous run's business.
	// Re-running is a human decision — set the todo ready again once the job is over.
	if holder, ok := s.todoActiveJob(todoID); ok {
		return TodoDispatch{Todo: todo, Reason: "todo already has an active job " + holder}, nil
	}

	plan, _, err := s.meta.GetPlan(todo.PlanID)
	if err != nil {
		return TodoDispatch{}, err
	}
	projectKey := todo.ProjectKey
	if projectKey == "" {
		projectKey = plan.ProjectKey
	}
	if projectKey == "" {
		return s.todoDispatchFailed(todo, "", "no project"), nil
	}

	req := JobRequest{
		ProjectKey: projectKey,
		Agent:      todo.Assignee,
		Runner:     todo.Runner,
		Cwd:        todo.Cwd,
		TimeoutSec: todo.TimeoutSec,
		Title:      todo.Title,
		// The linkage (SUP-01 C) is what makes the item follow its job: Submit resolves
		// nothing here, it just carries the ids the terminal hooks write back.
		TodoID: todo.TodoID,
		PlanID: todo.PlanID,
		// Provenance: which interface asked for this job. CallerID is the human the
		// dispatch was triggered by (the CLI's caller, the web session's caller).
		Channel:  channelPlan,
		CallerID: by,
		Verify:   todo.Verify,
		Review:   todo.Review,
		// The task book replaces the default prompt entirely: the todo's vars plus the
		// plan/todo builtins are what it renders with (see todoTemplateBuiltins).
		Template:     todo.Template,
		TemplateVars: todo.Vars,
	}
	if todo.Runner == "" {
		// A dispatch carries no --runner flag of its own, and validate requires one: the
		// built-in local runner is what `job run` defaults to, so an item that names no
		// machine runs on the server. A todo that wants a worker says so.
		req.Runner = builtinLocalRunner
	}
	// PLAN-03: an `exec` item IS its argv — the chain's last step is typically a
	// build/test command rather than an agent turn, so the dispatcher hands the item's
	// --cmd to the exec agent and refuses an exec item that names none (an exec job
	// without a command has nothing to run, and the refusal is what tells the human
	// which item to fix instead of a job that fails on an empty argv).
	if todo.Assignee == agent.ExecAgentKey {
		if len(todo.Cmd) == 0 {
			return s.todoDispatchFailed(todo, projectKey, "exec todo needs --cmd"), nil
		}
		req.Cmd = todo.Cmd
		req.Prompt = ""
	} else if todo.Template == "" {
		req.Prompt = defaultTodoPrompt(plan, todo)
	}

	res, err := s.Submit(req)
	if err != nil {
		return s.todoDispatchFailed(todo, projectKey, err.Error()), nil
	}
	s.recordEvent(res.ID, EventPlanTodoDispatched, map[string]any{
		"todo_id": todo.TodoID, "job_id": res.ID, "agent": todo.Assignee,
	})
	// The attempt succeeded: the reason the PREVIOUS one failed is history, and leaving
	// it on the item would have a human chase a problem that is already gone.
	if _, err := s.meta.SetTodoDispatchError(todo.TodoID, ""); err != nil {
		slog.Warn("clear todo dispatch error", "todo_id", todo.TodoID, "err", err)
	}
	// The submit-time linkage has already moved the item to `doing`; re-read so the
	// caller sees the state its dispatch produced.
	if fresh, ok, err := s.meta.GetTodo(todo.TodoID); err == nil && ok {
		todo = fresh
	}
	return TodoDispatch{Todo: todo, Job: &res}, nil
}

// todoDispatchFailed records a refused dispatch: the todo KEEPS its status (a ready
// item stays ready — the next attempt, or a human fixing the reason, must not have to
// re-queue it), the reason is stored on the item where a human reads it, and the event
// goes to the PLAN's scope: there is no job to hang it on, and a plan-level subscriber
// is exactly who wants to know that nothing started.
func (s *Service) todoDispatchFailed(todo jobstore.PlanTodo, projectKey, reason string) TodoDispatch {
	if _, err := s.meta.SetTodoDispatchError(todo.TodoID, reason); err != nil {
		slog.Warn("store todo dispatch error", "todo_id", todo.TodoID, "err", err)
	}
	s.RecordScopedEvent(PlanEventScope(todo.PlanID), EventPlanTodoDispatchFailed, projectKey, map[string]any{
		"todo_id": todo.TodoID, "reason": reason,
	})
	if fresh, ok, err := s.meta.GetTodo(todo.TodoID); err == nil && ok {
		todo = fresh
	}
	return TodoDispatch{Todo: todo, Reason: reason}
}

// todoActiveJob reports the id of a job of this todo that is still live: not terminal,
// and therefore — per design §四.1 — a `needs_review` delivery whose verdict is still
// pending counts too (the previous run's work is not finished as far as the checklist
// is concerned).
func (s *Service) todoActiveJob(todoID string) (string, bool) {
	recs, err := s.meta.ListJobsByTodo(todoID, activeTodoScanLimit)
	if err != nil {
		// Failing open here would dispatch a SECOND job onto work that may be running;
		// the safe reading of "we cannot tell" is "assume it is busy".
		slog.Warn("todo dispatch: list jobs by todo", "todo_id", todoID, "err", err)
		return "", true
	}
	for _, rec := range recs {
		if !IsTerminal(rec.Status) {
			return rec.ID, true
		}
	}
	return "", false
}

// PlanEventScope is the synthetic event-scope id of a plan (`plan:<id>`), mirroring
// xfer.EventJobID: an event that belongs to no job is still recorded in the shared
// event log, and its scope is what a reader filters on.
func PlanEventScope(planID string) string { return planScopePrefix + planID }

// defaultTodoPrompt is the prompt of a dispatched item that names no task book: the
// plan says WHAT the work is and why, the item says which piece of it this run owns.
// Empty sections are dropped rather than left as blank headings — a plan with no
// description must not hand the agent an empty paragraph to interpret.
func defaultTodoPrompt(plan jobstore.Plan, todo jobstore.PlanTodo) string {
	var b strings.Builder
	b.WriteString("# " + plan.Title)
	if plan.Description != "" {
		b.WriteString("\n\n" + plan.Description)
	}
	b.WriteString("\n\n## 本任务\n" + todo.Title)
	if todo.Note != "" {
		b.WriteString("\n\n" + todo.Note)
	}
	return b.String()
}

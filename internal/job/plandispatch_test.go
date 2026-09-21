package job

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// newDispatchService builds a service over three resumable cli-agents — "omp" and
// "other" echo their argv and exit 0, "slow" keeps printing for a few seconds — plus
// the project "self" that allows all three. It is the fixture the PLAN-02 tests drive
// a dispatched todo with: the dispatch path submits a normal job, so every property a
// job can have (agent, project, verify, review, runner, cwd, timeout) has to reach it.
func newDispatchService(t *testing.T, root string) *Service {
	t.Helper()
	bin := testcmd.Path(t)
	agentOf := func(args []string) config.AgentConfig {
		return config.AgentConfig{Type: agent.TypeCLIAgent, Command: bin, Args: args}
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"omp", "other", "slow"},
				AllowedRunners: []string{"local"},
				// A todo may carry a verify argv (PLAN-02 P2), and verify is an exec
				// surface: the project has to allow it.
				AllowExec: true,
			},
			"elsewhere": {
				HostPath:       root,
				AllowedAgents:  []string{"omp"},
				AllowedRunners: []string{"local"},
			},
		},
		Agents: map[string]config.AgentConfig{
			"omp":   agentOf([]string{"argv", "{{prompt}}"}),
			"other": agentOf([]string{"argv", "{{prompt}}"}),
			// A job that stays alive long enough to be an ACTIVE job (the gate the
			// dispatch rule must respect): ~4s of output, so it is never stalled.
			"slow": agentOf([]string{"stdout-lines", "tick", "20", "200ms"}),
		},
	}
	return newServiceFromCfg(t, root, cfg)
}

// seedDispatchPlan inserts a plan plus one todo of it into the service's store (the
// dispatcher reads them; it never creates them).
func seedDispatchPlan(t *testing.T, s *Service, plan jobstore.Plan, todo jobstore.PlanTodo) {
	t.Helper()
	todo.PlanID = plan.PlanID
	if err := s.Meta().InsertPlan(plan); err != nil {
		t.Fatalf("insert plan %s: %v", plan.PlanID, err)
	}
	if err := s.Meta().InsertTodo(todo); err != nil {
		t.Fatalf("insert todo %s: %v", todo.TodoID, err)
	}
}

// setTodoFields applies a dispatch-field patch to a seeded todo.
func setTodoFields(t *testing.T, s *Service, todoID string, p jobstore.TodoPatch) {
	t.Helper()
	if ok, err := s.Meta().UpdateTodoPatch(todoID, p); err != nil || !ok {
		t.Fatalf("UpdateTodoPatch(%s): ok=%v err=%v", todoID, ok, err)
	}
}

// setTodoStatus moves a seeded todo along its lifecycle.
func setTodoStatus(t *testing.T, s *Service, todoID, status string) {
	t.Helper()
	if ok, err := s.Meta().UpdateTodoStatus(todoID, status, nil); err != nil || !ok {
		t.Fatalf("UpdateTodoStatus(%s, %s): ok=%v err=%v", todoID, status, ok, err)
	}
}

// ptrStr/ptrInt build the pointer fields of a TodoPatch.
func ptrStr(s string) *string { return &s }
func ptrInt(n int) *int       { return &n }

// requestOf decodes a finished job's persisted request_json.
func requestOf(t *testing.T, snap JobResult) JobRequest {
	t.Helper()
	var req JobRequest
	if err := json.Unmarshal([]byte(snap.RequestJSON), &req); err != nil {
		t.Fatalf("request_json of %s is not JSON: %v", snap.ID, err)
	}
	return req
}

// todoEvents returns the events recorded under a synthetic scope (a plan, when the
// dispatch of one of its todos could not be submitted).
func todoEvents(t *testing.T, s *Service, scope string) []jobstore.JobEvent {
	t.Helper()
	evs, err := s.ListJobEvents(scope, 0)
	if err != nil {
		t.Fatalf("ListJobEvents(%s): %v", scope, err)
	}
	return evs
}

// TestTodoDispatchOnReadyWithAssignee (PLAN-02 P2, decision 5): a todo that is BOTH
// ready and assigned turns into a job on the spot — with the plan's project, the
// assignee as the agent, the plan/todo linkage, the plan channel and the triggering
// caller — and the SUP-01 C linkage then walks it doing → done as the job runs.
func TestTodoDispatchOnReadyWithAssignee(t *testing.T) {
	s := newDispatchService(t, t.TempDir())
	seedDispatchPlan(t, s,
		jobstore.Plan{PlanID: "plan-d", Title: "Dispatch plan", Status: jobstore.PlanOpen, ProjectKey: "self", CreatedAt: 1, UpdatedAt: 1},
		jobstore.PlanTodo{TodoID: "todo-d", Title: "step one", CreatedAt: 1, UpdatedAt: 1})

	// ready WITHOUT an assignee dispatches nothing: the rule needs both conditions.
	setTodoStatus(t, s, "todo-d", jobstore.TodoReady)
	d, err := s.MaybeDispatchTodo("todo-d", "alice")
	if err != nil {
		t.Fatalf("MaybeDispatchTodo(ready, no assignee): %v", err)
	}
	if d.Job != nil {
		t.Fatalf("a ready todo with no assignee must not be dispatched: %+v", d)
	}
	if td := getTodo(t, s, "todo-d"); td.Status != jobstore.TodoReady {
		t.Fatalf("status = %q, want the todo left ready", td.Status)
	}
	if len(todoEvents(t, s, "plan:plan-d")) != 0 {
		t.Fatalf("a precondition that was simply not met must not record a dispatch failure")
	}

	setTodoFields(t, s, "todo-d", jobstore.TodoPatch{Assignee: ptrStr("omp")})
	d, err = s.MaybeDispatchTodo("todo-d", "alice")
	if err != nil {
		t.Fatalf("MaybeDispatchTodo: %v", err)
	}
	if d.Job == nil {
		t.Fatalf("a ready+assigned todo must dispatch: reason=%q", d.Reason)
	}
	job := *d.Job
	if job.Agent != "omp" || job.ProjectKey != "self" {
		t.Fatalf("dispatched job ran as %s/%s, want omp/self", job.Agent, job.ProjectKey)
	}
	if job.TodoID != "todo-d" || job.PlanID != "plan-d" {
		t.Fatalf("dispatched job lost its linkage: todo_id=%q plan_id=%q", job.TodoID, job.PlanID)
	}
	if job.Channel != "plan" || job.CallerID != "alice" {
		t.Fatalf("dispatched job provenance = %q/%q, want plan/alice", job.Channel, job.CallerID)
	}

	// The submit-time linkage marks it doing and points it at the new job.
	td := getTodo(t, s, "todo-d")
	if td.Status != jobstore.TodoDoing {
		t.Fatalf("todo status = %q, want doing once its job is running", td.Status)
	}
	if td.JobID != job.ID {
		t.Fatalf("todo job_id = %q, want %q", td.JobID, job.ID)
	}
	// A successful dispatch leaves no failure text behind.
	if td.DispatchError != "" {
		t.Fatalf("dispatch_error = %q, want it empty after a successful dispatch", td.DispatchError)
	}

	final, ok := s.Wait(job.ID)
	if !ok {
		t.Fatalf("Wait: job %s not found", job.ID)
	}
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if td := getTodo(t, s, "todo-d"); td.Status != jobstore.TodoDone {
		t.Fatalf("todo status = %q, want done after its job finished", td.Status)
	}

	// The dispatch itself is on the new job's timeline, naming todo, job and agent.
	events := todoEvents(t, s, job.ID)
	found := false
	for _, ev := range events {
		if ev.Type != EventPlanTodoDispatched {
			continue
		}
		found = true
		var detail map[string]any
		if err := json.Unmarshal([]byte(ev.Detail), &detail); err != nil {
			t.Fatalf("plan.todo_dispatched detail is not JSON: %v", err)
		}
		if detail["todo_id"] != "todo-d" || detail["job_id"] != job.ID || detail["agent"] != "omp" {
			t.Fatalf("plan.todo_dispatched detail = %v, want todo_id/job_id/agent", detail)
		}
	}
	if !found {
		t.Fatalf("job %s has no %s event: %+v", job.ID, EventPlanTodoDispatched, events)
	}
}

// TestTodoDispatchOnAssignWhenAlreadyReady: the two conditions are ANDed and either
// one may arrive last — assigning an already-ready todo dispatches, and assigning a
// todo that is NOT ready only stores the assignee.
func TestTodoDispatchOnAssignWhenAlreadyReady(t *testing.T) {
	s := newDispatchService(t, t.TempDir())
	seedDispatchPlan(t, s,
		jobstore.Plan{PlanID: "plan-a", Title: "Assign plan", Status: jobstore.PlanOpen, ProjectKey: "self", CreatedAt: 1, UpdatedAt: 1},
		jobstore.PlanTodo{TodoID: "todo-a", Title: "step", CreatedAt: 1, UpdatedAt: 1})
	seedDispatchPlan(t, s,
		jobstore.Plan{PlanID: "plan-b", Title: "Backlog plan", Status: jobstore.PlanOpen, ProjectKey: "self", CreatedAt: 2, UpdatedAt: 2},
		jobstore.PlanTodo{TodoID: "todo-b", Title: "backlog step", CreatedAt: 2, UpdatedAt: 2})

	// ready first, assignee second.
	setTodoStatus(t, s, "todo-a", jobstore.TodoReady)
	setTodoFields(t, s, "todo-a", jobstore.TodoPatch{Assignee: ptrStr("omp")})
	d, err := s.MaybeDispatchTodo("todo-a", "bob")
	if err != nil {
		t.Fatalf("MaybeDispatchTodo: %v", err)
	}
	if d.Job == nil || d.Job.Agent != "omp" {
		t.Fatalf("assigning a ready todo must dispatch to that agent: %+v", d)
	}
	if final, _ := s.Wait(d.Job.ID); final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}

	// assignee first, but the todo stays in the backlog → nothing to dispatch.
	setTodoFields(t, s, "todo-b", jobstore.TodoPatch{Assignee: ptrStr("omp")})
	d, err = s.MaybeDispatchTodo("todo-b", "bob")
	if err != nil {
		t.Fatalf("MaybeDispatchTodo(pending+assignee): %v", err)
	}
	if d.Job != nil {
		t.Fatalf("a pending todo must not dispatch, even with an assignee: %+v", d)
	}
	if d.Reason == "" {
		t.Fatalf("a skipped dispatch must say why: %+v", d)
	}
	if td := getTodo(t, s, "todo-b"); td.Status != jobstore.TodoPending || td.Assignee != "omp" {
		t.Fatalf("the assignee must be stored without dispatching: %+v", td)
	}
}

// TestTodoDispatchNeedsProject: neither the todo nor its plan named a project, so
// there is nothing to run in: the todo stays ready, the reason is stored on it (that
// is where a human reads it) and the failure is recorded under the PLAN scope —
// there is no job to hang it on.
func TestTodoDispatchNeedsProject(t *testing.T) {
	s := newDispatchService(t, t.TempDir())
	seedDispatchPlan(t, s,
		jobstore.Plan{PlanID: "plan-np", Title: "No project", Status: jobstore.PlanOpen, CreatedAt: 1, UpdatedAt: 1},
		jobstore.PlanTodo{TodoID: "todo-np", Title: "step", Assignee: "omp", CreatedAt: 1, UpdatedAt: 1})
	setTodoStatus(t, s, "todo-np", jobstore.TodoReady)

	d, err := s.MaybeDispatchTodo("todo-np", "alice")
	if err != nil {
		t.Fatalf("MaybeDispatchTodo: %v", err)
	}
	if d.Job != nil {
		t.Fatalf("a project-less todo must not be dispatched: %+v", d)
	}
	if !strings.Contains(d.Reason, "no project") {
		t.Fatalf("reason = %q, want it to name the missing project", d.Reason)
	}
	td := getTodo(t, s, "todo-np")
	if td.Status != jobstore.TodoReady {
		t.Fatalf("status = %q, want the todo left ready for a retry", td.Status)
	}
	if !strings.Contains(td.DispatchError, "no project") {
		t.Fatalf("dispatch_error = %q, want the reason stored on the todo", td.DispatchError)
	}
	if len(todoEvents(t, s, "plan:plan-np")) == 0 {
		t.Fatalf("a failed dispatch must be recorded under the plan scope")
	}

	// The todo's own project override is what the dispatcher uses when the plan has
	// none — and a successful dispatch clears the stored failure.
	setTodoFields(t, s, "todo-np", jobstore.TodoPatch{ProjectKey: ptrStr("elsewhere")})
	d, err = s.MaybeDispatchTodo("todo-np", "alice")
	if err != nil {
		t.Fatalf("MaybeDispatchTodo(project override): %v", err)
	}
	if d.Job == nil || d.Job.ProjectKey != "elsewhere" {
		t.Fatalf("the todo's project override must win over the plan's: %+v", d)
	}
	if td := getTodo(t, s, "todo-np"); td.DispatchError != "" {
		t.Fatalf("a later successful dispatch must clear dispatch_error, got %q", td.DispatchError)
	}
	if final, _ := s.Wait(d.Job.ID); final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
}

// TestTodoDispatchSkipsWhileActiveJob: a todo whose previous run is still live is NOT
// dispatched again — re-setting it ready while the job runs (or while a delivery
// awaits review) must not start a second agent on the same work.
func TestTodoDispatchSkipsWhileActiveJob(t *testing.T) {
	s := newDispatchService(t, t.TempDir())
	seedDispatchPlan(t, s,
		jobstore.Plan{PlanID: "plan-s", Title: "Slow plan", Status: jobstore.PlanOpen, ProjectKey: "self", CreatedAt: 1, UpdatedAt: 1},
		jobstore.PlanTodo{TodoID: "todo-s", Title: "step", Assignee: "slow", CreatedAt: 1, UpdatedAt: 1})
	setTodoStatus(t, s, "todo-s", jobstore.TodoReady)

	first, err := s.MaybeDispatchTodo("todo-s", "alice")
	if err != nil || first.Job == nil {
		t.Fatalf("first dispatch: %+v err=%v", first, err)
	}

	// The todo is doing now; putting it back in the queue must not start a second job
	// while the first is still running.
	setTodoStatus(t, s, "todo-s", jobstore.TodoReady)
	d, err := s.MaybeDispatchTodo("todo-s", "alice")
	if err != nil {
		t.Fatalf("MaybeDispatchTodo(while active): %v", err)
	}
	if d.Job != nil {
		t.Fatalf("an active job must block redispatch: %+v", d)
	}
	if !strings.Contains(d.Reason, first.Job.ID) {
		t.Fatalf("reason = %q, want it to name the job holding the todo", d.Reason)
	}
	runs, err := s.Meta().ListJobsByTodo("todo-s", 10)
	if err != nil {
		t.Fatalf("ListJobsByTodo: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("todo has %d jobs, want exactly the one still running", len(runs))
	}

	// The gate is about the ACTIVE job, not about any job: once the run is over the
	// same todo dispatches again. Wait (not a status poll) because the gate reads the
	// STORE: the job's row — and its todo linkage — are written before its goroutine ends.
	_ = s.Cancel(first.Job.ID)
	if final, ok := s.Wait(first.Job.ID); !ok || final.Status != StatusCancelled {
		t.Fatalf("cancelled job = %+v ok=%v, want cancelled", final, ok)
	}
	setTodoStatus(t, s, "todo-s", jobstore.TodoReady)
	second, err := s.MaybeDispatchTodo("todo-s", "alice")
	if err != nil || second.Job == nil {
		t.Fatalf("redispatch after a terminal job: %+v err=%v", second, err)
	}
	_ = s.Cancel(second.Job.ID)
	if final, ok := s.Wait(second.Job.ID); !ok || final.Status != StatusCancelled {
		t.Fatalf("cancelled job = %+v ok=%v, want cancelled", final, ok)
	}
}

// TestTodoDispatchDefaultPromptContainsPlanAndTodo: with no task book the dispatched
// job is driven by the default prompt, which must carry what the agent needs to work
// on its own: which plan, why, and which item of it.
func TestTodoDispatchDefaultPromptContainsPlanAndTodo(t *testing.T) {
	s := newDispatchService(t, t.TempDir())
	seedDispatchPlan(t, s,
		jobstore.Plan{
			PlanID: "plan-p", Title: "Fix the flaky test", Description: "Because CI is red every morning",
			Status: jobstore.PlanOpen, ProjectKey: "self", CreatedAt: 1, UpdatedAt: 1,
		},
		jobstore.PlanTodo{
			TodoID: "todo-p", Title: "T1 investigate the retry loop", Note: "start from the log tail",
			Assignee: "omp", CreatedAt: 1, UpdatedAt: 1,
		})
	setTodoStatus(t, s, "todo-p", jobstore.TodoReady)

	d, err := s.MaybeDispatchTodo("todo-p", "alice")
	if err != nil || d.Job == nil {
		t.Fatalf("dispatch: %+v err=%v", d, err)
	}
	final, _ := s.Wait(d.Job.ID)
	req := requestOf(t, final)
	for _, want := range []string{
		"# Fix the flaky test", "Because CI is red every morning",
		"## 本任务", "T1 investigate the retry loop", "start from the log tail",
	} {
		if !strings.Contains(req.Prompt, want) {
			t.Fatalf("default prompt %q is missing %q", req.Prompt, want)
		}
	}
	if req.Template != "" {
		t.Fatalf("a todo with no template must not send one, got %q", req.Template)
	}
	if req.Title != "T1 investigate the retry loop" {
		t.Fatalf("job title = %q, want the todo's title", req.Title)
	}
}

// TestTodoDispatchWithTemplateVars: a todo may name a task book instead of taking the
// default prompt — and the plan/todo builtins are resolved for that render, so ONE
// template serves every item of a plan.
func TestTodoDispatchWithTemplateVars(t *testing.T) {
	root := t.TempDir()
	s := newDispatchService(t, root)
	writeTemplate(t, root, "todo-tpl",
		"Plan: {{plan_title}}\nDesc: {{plan_description}}\nTodo: {{todo_title}} ({{todo_id}})\nNote: {{todo_note}}\nExtra: {{extra}}\n")
	seedDispatchPlan(t, s,
		jobstore.Plan{
			PlanID: "plan-t", Title: "Tpl plan", Description: "tpl desc",
			Status: jobstore.PlanOpen, ProjectKey: "self", CreatedAt: 1, UpdatedAt: 1,
		},
		jobstore.PlanTodo{
			TodoID: "todo-t", Title: "tpl todo", Note: "tpl note", Assignee: "omp",
			Template: "todo-tpl", Vars: map[string]string{"extra": "E"}, CreatedAt: 1, UpdatedAt: 1,
		})
	setTodoStatus(t, s, "todo-t", jobstore.TodoReady)

	d, err := s.MaybeDispatchTodo("todo-t", "alice")
	if err != nil || d.Job == nil {
		t.Fatalf("dispatch: %+v err=%v", d, err)
	}
	final, _ := s.Wait(d.Job.ID)
	req := requestOf(t, final)
	want := "Plan: Tpl plan\nDesc: tpl desc\nTodo: tpl todo (todo-t)\nNote: tpl note\nExtra: E"
	if req.Prompt != want {
		t.Fatalf("rendered prompt = %q, want %q", req.Prompt, want)
	}
	// The origin stays on the job (the audit trail says which task book produced it).
	if req.Template != "todo-tpl" || req.TemplateVars["extra"] != "E" {
		t.Fatalf("request template/vars = %q/%v, want them kept", req.Template, req.TemplateVars)
	}
}

// TestTodoRedispatchAfterTerminal: a finished item is re-runnable — setting it ready
// again (redo) starts a fresh job rather than silently doing nothing, and the item
// points at the newest run.
func TestTodoRedispatchAfterTerminal(t *testing.T) {
	s := newDispatchService(t, t.TempDir())
	seedDispatchPlan(t, s,
		jobstore.Plan{PlanID: "plan-r", Title: "Redo plan", Status: jobstore.PlanOpen, ProjectKey: "self", CreatedAt: 1, UpdatedAt: 1},
		jobstore.PlanTodo{TodoID: "todo-r", Title: "step", Assignee: "omp", CreatedAt: 1, UpdatedAt: 1})
	setTodoStatus(t, s, "todo-r", jobstore.TodoReady)

	first, err := s.MaybeDispatchTodo("todo-r", "alice")
	if err != nil || first.Job == nil {
		t.Fatalf("first dispatch: %+v err=%v", first, err)
	}
	if final, _ := s.Wait(first.Job.ID); final.Status != StatusDone {
		t.Fatalf("first job status = %s (err=%s), want done", final.Status, final.Error)
	}
	if td := getTodo(t, s, "todo-r"); td.Status != jobstore.TodoDone {
		t.Fatalf("todo status = %q, want done", td.Status)
	}

	// Redo: the item goes back to the queue and a NEW job is started.
	setTodoStatus(t, s, "todo-r", jobstore.TodoReady)
	second, err := s.MaybeDispatchTodo("todo-r", "alice")
	if err != nil {
		t.Fatalf("redispatch: %v", err)
	}
	if second.Job == nil {
		t.Fatalf("a re-queued todo must start a new job: reason=%q", second.Reason)
	}
	if second.Job.ID == first.Job.ID {
		t.Fatalf("redispatch returned the same job id %s", second.Job.ID)
	}
	if td := getTodo(t, s, "todo-r"); td.JobID != second.Job.ID {
		t.Fatalf("todo job_id = %q, want the newest run %q", td.JobID, second.Job.ID)
	}
	if final, _ := s.Wait(second.Job.ID); final.Status != StatusDone {
		t.Fatalf("second job status = %s (err=%s), want done", final.Status, final.Error)
	}
}

// TestTodoDispatchInheritsVerifyReviewRunner: everything the todo declares about HOW
// the work must run — verify step, human review, runner, cwd, timeout — reaches the
// dispatched job instead of being dropped on the way.
func TestTodoDispatchInheritsVerifyReviewRunner(t *testing.T) {
	s := newDispatchService(t, t.TempDir())
	verify := []string{testcmd.Path(t), "exit", "0"}
	seedDispatchPlan(t, s,
		jobstore.Plan{PlanID: "plan-i", Title: "Inherit plan", Status: jobstore.PlanOpen, ProjectKey: "self", CreatedAt: 1, UpdatedAt: 1},
		jobstore.PlanTodo{
			TodoID: "todo-i", Title: "step", Assignee: "omp", Runner: "local",
			Verify: verify, Review: true, Cwd: ".", TimeoutSec: 45, CreatedAt: 1, UpdatedAt: 1,
		})
	setTodoStatus(t, s, "todo-i", jobstore.TodoReady)

	d, err := s.MaybeDispatchTodo("todo-i", "alice")
	if err != nil || d.Job == nil {
		t.Fatalf("dispatch: %+v err=%v", d, err)
	}
	if d.Job.Runner != "local" {
		t.Fatalf("runner = %q, want the todo's local", d.Job.Runner)
	}
	// Review=true parks the job in needs_review (the todo keeps it doing): the
	// decision to review was inherited, not re-derived.
	waitForStatus(t, s, d.Job.ID, StatusNeedsReview, 15*time.Second)
	final, ok := s.Get(d.Job.ID)
	if !ok {
		t.Fatalf("Get: job %s not found", d.Job.ID)
	}
	req := requestOf(t, final)
	if len(req.Verify) != len(verify) || req.Verify[1] != "exit" {
		t.Fatalf("request verify = %v, want the todo's %v", req.Verify, verify)
	}
	if !req.Review {
		t.Fatalf("request review = false, want the todo's true")
	}
	if req.Runner != "local" {
		t.Fatalf("request runner = %q, want local", req.Runner)
	}
	if req.TimeoutSec != 45 {
		t.Fatalf("request timeout_sec = %d, want the todo's 45", req.TimeoutSec)
	}
	if td := getTodo(t, s, "todo-i"); td.Status != jobstore.TodoDoing {
		t.Fatalf("todo status = %q, want doing while the delivery awaits review", td.Status)
	}
	// A human accepting the delivery closes the item, exactly like a normal finish.
	if _, err := s.AcceptJob(final.ID, "alice", "lgtm"); err != nil {
		t.Fatalf("AcceptJob: %v", err)
	}
	if td := getTodo(t, s, "todo-i"); td.Status != jobstore.TodoDone {
		t.Fatalf("todo status = %q, want done after acceptance", td.Status)
	}
}

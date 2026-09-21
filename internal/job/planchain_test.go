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

// newChainService builds a service over four cli-agents — "ok" (echoes its argv and
// exits 0), "boom" (always exits 1), "slow" (prints for ~8s, i.e. a job that is still
// live while a test asserts on the plan) and the built-in exec — plus the project
// "self" that allows all of them. It is the fixture the PLAN-03 chain tests drive: a
// chain is only interesting when the items actually RUN.
func newChainService(t *testing.T, root string) *Service {
	t.Helper()
	bin := testcmd.Path(t)
	agentOf := func(args ...string) config.AgentConfig {
		return config.AgentConfig{Type: agent.TypeCLIAgent, Command: bin, Args: args}
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"ok", "boom", "slow", agent.ExecAgentKey},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"ok":   agentOf("argv", "{{prompt}}"),
			"boom": agentOf("exit", "1"),
			"slow": agentOf("stdout-lines", "tick", "40", "200ms"),
		},
	}
	return newServiceFromCfg(t, root, cfg)
}

// chainTodo builds one item of a chain: pending, assigned, auto-advancing unless the
// test says otherwise.
func chainTodo(todoID, title, assignee string, after ...string) jobstore.PlanTodo {
	return jobstore.PlanTodo{
		TodoID: todoID, Title: title, Status: jobstore.TodoPending,
		Assignee: assignee, After: after, Auto: true, CreatedAt: 1, UpdatedAt: 1,
	}
}

// seedChain inserts a plan and its items (in the given order).
func seedChain(t *testing.T, s *Service, planID string, todos ...jobstore.PlanTodo) {
	t.Helper()
	if err := s.Meta().InsertPlan(jobstore.Plan{
		PlanID: planID, Title: planID, Status: jobstore.PlanOpen, ProjectKey: "self",
		CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("insert plan %s: %v", planID, err)
	}
	for i, td := range todos {
		td.PlanID = planID
		if td.Sort == 0 {
			td.Sort = (i + 1) * 10
		}
		if err := s.Meta().InsertTodo(td); err != nil {
			t.Fatalf("insert todo %s: %v", td.TodoID, err)
		}
	}
}

// waitPlanStatus polls a plan until it reaches want (or the deadline passes), and
// returns the last reading either way so the caller's assertion prints what it saw.
func waitPlanStatus(t *testing.T, s *Service, planID, want string, timeout time.Duration) jobstore.Plan {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last jobstore.Plan
	for time.Now().Before(deadline) {
		p, ok, err := s.Meta().GetPlan(planID)
		if err != nil {
			t.Fatalf("get plan %s: %v", planID, err)
		}
		if !ok {
			t.Fatalf("plan %s vanished", planID)
		}
		last = p
		if p.Status == want {
			return p
		}
		time.Sleep(20 * time.Millisecond)
	}
	return last
}

// todoStatus reads one item's lifecycle status.
func todoStatus(t *testing.T, s *Service, todoID string) string {
	t.Helper()
	td, ok, err := s.Meta().GetTodo(todoID)
	if err != nil || !ok {
		t.Fatalf("get todo %s: ok=%v err=%v", todoID, ok, err)
	}
	return td.Status
}

// waitTodoStatus polls one item until it reaches want.
func waitTodoStatus(t *testing.T, s *Service, todoID, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if todoStatus(t, s, todoID) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("todo %s status = %q, want %q", todoID, todoStatus(t, s, todoID), want)
}

// planEvents returns the plan-scope events (oldest first) with their details decoded.
func planEvents(t *testing.T, s *Service, planID string) []struct {
	Type   string
	Detail map[string]any
} {
	t.Helper()
	evs, err := s.ListJobEvents(PlanEventScope(planID), 0)
	if err != nil {
		t.Fatalf("ListJobEvents(%s): %v", planID, err)
	}
	out := make([]struct {
		Type   string
		Detail map[string]any
	}, 0, len(evs))
	for _, ev := range evs {
		d := map[string]any{}
		if ev.Detail != "" {
			if err := json.Unmarshal([]byte(ev.Detail), &d); err != nil {
				t.Fatalf("event %s detail %q is not JSON: %v", ev.Type, ev.Detail, err)
			}
		}
		out = append(out, struct {
			Type   string
			Detail map[string]any
		}{Type: ev.Type, Detail: d})
	}
	return out
}

// countPlanEvent counts the plan-scope events of one type.
func countPlanEvent(t *testing.T, s *Service, planID, eventType string) int {
	t.Helper()
	n := 0
	for _, ev := range planEvents(t, s, planID) {
		if ev.Type == eventType {
			n++
		}
	}
	return n
}

// waitPlanEventCount waits until the plan's event stream holds exactly want events of
// one type. A job's terminal hook writes its todo/plan rows and its plan events in
// sequence, so a status poll can win the race against the event insert; a test that
// asserts on the event must wait for the EVENT.
func waitPlanEventCount(t *testing.T, s *Service, planID, eventType string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if n := countPlanEvent(t, s, planID, eventType); n == want {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("%s events = %d, want %d", eventType, n, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// planEventMentions reports whether an event of this type names this todo.
func planEventMentions(t *testing.T, s *Service, planID, eventType, todoID string) bool {
	t.Helper()
	for _, ev := range planEvents(t, s, planID) {
		if ev.Type == eventType && ev.Detail["todo_id"] == todoID {
			return true
		}
	}
	return false
}

// todoJobID returns the newest job attached to a todo ("" when none ran).
func todoJobID(t *testing.T, s *Service, todoID string) string {
	t.Helper()
	recs, err := s.Meta().ListJobsByTodo(todoID, 5)
	if err != nil {
		t.Fatalf("ListJobsByTodo(%s): %v", todoID, err)
	}
	if len(recs) == 0 {
		return ""
	}
	return recs[0].ID
}

// retryTodo is the `plan set-todo <todo> --status ready` path as the HTTP handler runs
// it: re-dispatch the item (the write path's dispatch trigger) and then let the chain
// react (release a block, advance). It is spelled out here so a test says exactly what
// a human's unblock does.
func retryTodo(t *testing.T, s *Service, todoID, status string) {
	t.Helper()
	if _, err := s.MaybeDispatchTodo(todoID, "alice"); err != nil {
		t.Fatalf("MaybeDispatchTodo(%s): %v", todoID, err)
	}
	s.PlanTodoChanged(todoID, status, "alice")
}

// TestPlanChainAdvancesOnDone (PLAN-03): three chained items A→B→C, one `plan run`, and
// the chain walks itself to the end — each item starts only after the previous one is
// done, the plan completes, and the plan's own event stream says what moved when.
func TestPlanChainAdvancesOnDone(t *testing.T) {
	s := newChainService(t, t.TempDir())
	seedChain(t, s, "plan-chain-run",
		chainTodo("todo-a", "first", "ok"),
		chainTodo("todo-b", "second", "ok", "todo-a"),
		chainTodo("todo-c", "third", "ok", "todo-b"),
	)

	p, err := s.RunPlan("plan-chain-run", "alice")
	if err != nil {
		t.Fatalf("RunPlan: %v", err)
	}
	if p.Status != jobstore.PlanOpen {
		t.Fatalf("plan status after run = %q, want open", p.Status)
	}
	// The root is the only thing run starts: B and C wait for their dependency.
	waitTodoStatus(t, s, "todo-a", jobstore.TodoDoing, 5*time.Second)
	if got := todoStatus(t, s, "todo-b"); got != jobstore.TodoPending {
		t.Fatalf("todo-b status = %q while todo-a runs, want pending", got)
	}

	if got := waitPlanStatus(t, s, "plan-chain-run", jobstore.PlanDone, 30*time.Second); got.Status != jobstore.PlanDone {
		t.Fatalf("plan status = %q, want done", got.Status)
	}
	for _, id := range []string{"todo-a", "todo-b", "todo-c"} {
		if got := todoStatus(t, s, id); got != jobstore.TodoDone {
			t.Fatalf("%s status = %q, want done", id, got)
		}
	}
	// B and C were queued by the chain (A was queued by `plan run` itself).
	waitPlanEventCount(t, s, "plan-chain-run", EventPlanTodoAdvanced, 3, 10*time.Second)
	if !planEventMentions(t, s, "plan-chain-run", EventPlanTodoAdvanced, "todo-b") {
		t.Fatal("no plan.todo_advanced event names todo-b")
	}
	waitPlanEventCount(t, s, "plan-chain-run", EventPlanCompleted, 1, 10*time.Second)
	if n := countPlanEvent(t, s, "plan-chain-run", EventPlanBlocked); n != 0 {
		t.Fatalf("a clean chain must not block: %d plan.blocked events", n)
	}
}

// TestPlanRunOnlyStartsRoots (PLAN-03): `plan run` starts the items with no unmet
// dependency — a dependent item stays pending until its predecessor finishes, and a
// dependent item's job is never started early.
func TestPlanRunOnlyStartsRoots(t *testing.T) {
	s := newChainService(t, t.TempDir())
	// A slow root keeps the window open: B must stay pending while A is running.
	seedChain(t, s, "plan-roots",
		chainTodo("todo-root", "root", "slow"),
		chainTodo("todo-leaf", "leaf", "ok", "todo-root"),
	)
	if _, err := s.RunPlan("plan-roots", "alice"); err != nil {
		t.Fatalf("RunPlan: %v", err)
	}
	waitTodoStatus(t, s, "todo-root", jobstore.TodoDoing, 5*time.Second)

	if got := todoStatus(t, s, "todo-leaf"); got != jobstore.TodoPending {
		t.Fatalf("todo-leaf status = %q, want pending (its dependency is still running)", got)
	}
	if jobID := todoJobID(t, s, "todo-leaf"); jobID != "" {
		t.Fatalf("todo-leaf got job %s before its dependency finished", jobID)
	}
	if planEventMentions(t, s, "plan-roots", EventPlanTodoAdvanced, "todo-leaf") {
		t.Fatal("todo-leaf must not be reported as advanced while its dependency runs")
	}
	if n := countPlanEvent(t, s, "plan-roots", EventPlanCompleted); n != 0 {
		t.Fatal("a plan with a running item is not complete")
	}
}

// TestPlanChainPausedHolds (PLAN-03): a paused plan does not advance — a finished item
// leaves its dependents pending, and the plan's event stream records WHY nothing moved
// (plan.advance_paused) instead of silently doing nothing.
func TestPlanChainPausedHolds(t *testing.T) {
	s := newChainService(t, t.TempDir())
	seedChain(t, s, "plan-paused",
		chainTodo("todo-p1", "done already", "ok"),
		chainTodo("todo-p2", "waiting", "ok", "todo-p1"),
	)
	if _, err := s.PausePlan("plan-paused"); err != nil {
		t.Fatalf("PausePlan: %v", err)
	}
	// A finished item normally moves the chain; paused, it must not.
	if ok, err := s.Meta().UpdateTodoStatus("todo-p1", jobstore.TodoDone, nil); err != nil || !ok {
		t.Fatalf("mark todo-p1 done: ok=%v err=%v", ok, err)
	}
	s.PlanTodoChanged("todo-p1", jobstore.TodoDone, "alice")

	if got := todoStatus(t, s, "todo-p2"); got != jobstore.TodoPending {
		t.Fatalf("todo-p2 status = %q, want pending while the plan is paused", got)
	}
	if n := countPlanEvent(t, s, "plan-paused", EventPlanAdvancePaused); n == 0 {
		t.Fatal("a paused advance must record plan.advance_paused")
	}
	p, _, _ := s.Meta().GetPlan("plan-paused")
	if !p.Paused || p.Status != jobstore.PlanOpen {
		t.Fatalf("paused plan = %+v, want paused+open", p)
	}

	// Resuming lets the chain move again.
	if _, err := s.ResumePlan("plan-paused", "alice"); err != nil {
		t.Fatalf("ResumePlan: %v", err)
	}
	waitTodoStatus(t, s, "todo-p2", jobstore.TodoDone, 30*time.Second)
}

// TestPlanChainBlocksOnFailure (PLAN-03): a failed chain item parks the whole plan on
// itself (status=blocked + plan.blocked event naming the item and the job), the later
// items do NOT start, and releasing the item re-dispatches it and carries the chain on.
func TestPlanChainBlocksOnFailure(t *testing.T) {
	s := newChainService(t, t.TempDir())
	seedChain(t, s, "plan-block",
		chainTodo("todo-a", "first", "ok"),
		chainTodo("todo-b", "flaky", "boom", "todo-a"),
		chainTodo("todo-c", "third", "ok", "todo-b"),
	)
	if _, err := s.RunPlan("plan-block", "alice"); err != nil {
		t.Fatalf("RunPlan: %v", err)
	}
	p := waitPlanStatus(t, s, "plan-block", jobstore.PlanBlocked, 30*time.Second)
	if p.Status != jobstore.PlanBlocked || p.BlockedTodo != "todo-b" {
		t.Fatalf("plan = %+v, want blocked on todo-b", p)
	}
	if got := todoStatus(t, s, "todo-c"); got != jobstore.TodoPending {
		t.Fatalf("todo-c status = %q, want pending (the chain is parked)", got)
	}
	waitPlanEventCount(t, s, "plan-block", EventPlanBlocked, 1, 10*time.Second)
	for _, ev := range planEvents(t, s, "plan-block") {
		if ev.Type != EventPlanBlocked {
			continue
		}
		if ev.Detail["todo_id"] != "todo-b" {
			t.Fatalf("plan.blocked names %v, want todo-b", ev.Detail["todo_id"])
		}
		if ev.Detail["job"] == "" || ev.Detail["reason"] == "" {
			t.Fatalf("plan.blocked detail is incomplete: %v", ev.Detail)
		}
	}

	// The human's fix: point the item at a working agent and re-queue it. The plan
	// leaves `blocked`, the item re-runs, and C follows it.
	okAgent := "ok"
	if ok, err := s.Meta().UpdateTodoPatch("todo-b", jobstore.TodoPatch{Assignee: &okAgent}); err != nil || !ok {
		t.Fatalf("reassign todo-b: ok=%v err=%v", ok, err)
	}
	if ok, err := s.Meta().UpdateTodoStatus("todo-b", jobstore.TodoReady, nil); err != nil || !ok {
		t.Fatalf("set todo-b ready: ok=%v err=%v", ok, err)
	}
	retryTodo(t, s, "todo-b", jobstore.TodoReady)

	if got := waitPlanStatus(t, s, "plan-block", jobstore.PlanDone, 30*time.Second); got.Status != jobstore.PlanDone {
		t.Fatalf("plan status = %q, want done after the retry", got.Status)
	}
	if got := todoStatus(t, s, "todo-c"); got != jobstore.TodoDone {
		t.Fatalf("todo-c status = %q, want done", got)
	}
	if p, _, _ := s.Meta().GetPlan("plan-block"); p.BlockedTodo != "" {
		t.Fatalf("plan still names a blocked item: %+v", p)
	}
}

// TestPlanChainSkipUnblocks (PLAN-03): a human can skip the failed item instead of
// re-running it — the chain moves on to what waited for it.
func TestPlanChainSkipUnblocks(t *testing.T) {
	s := newChainService(t, t.TempDir())
	seedChain(t, s, "plan-skip",
		chainTodo("todo-a", "first", "ok"),
		chainTodo("todo-b", "flaky", "boom", "todo-a"),
		chainTodo("todo-c", "third", "ok", "todo-b"),
	)
	if _, err := s.RunPlan("plan-skip", "alice"); err != nil {
		t.Fatalf("RunPlan: %v", err)
	}
	if p := waitPlanStatus(t, s, "plan-skip", jobstore.PlanBlocked, 30*time.Second); p.Status != jobstore.PlanBlocked {
		t.Fatalf("plan status = %q, want blocked", p.Status)
	}
	if ok, err := s.Meta().UpdateTodoStatus("todo-b", jobstore.TodoSkipped, nil); err != nil || !ok {
		t.Fatalf("skip todo-b: ok=%v err=%v", ok, err)
	}
	s.PlanTodoChanged("todo-b", jobstore.TodoSkipped, "alice")

	if p := waitPlanStatus(t, s, "plan-skip", jobstore.PlanDone, 30*time.Second); p.Status != jobstore.PlanDone {
		t.Fatalf("plan status = %q, want done after the skip", p.Status)
	}
	if got := todoStatus(t, s, "todo-c"); got != jobstore.TodoDone {
		t.Fatalf("todo-c status = %q, want done", got)
	}
	if got := todoStatus(t, s, "todo-b"); got != jobstore.TodoSkipped {
		t.Fatalf("todo-b status = %q, want skipped", got)
	}
}

// TestPlanChainUnassignedTodoWaits (PLAN-03): an item that comes due with nobody
// assigned cannot be started — it stays pending and the plan records
// plan.todo_unassigned, which is the only signal a human gets (nothing failed).
func TestPlanChainUnassignedTodoWaits(t *testing.T) {
	s := newChainService(t, t.TempDir())
	seedChain(t, s, "plan-unassigned",
		chainTodo("todo-a", "first", "ok"),
		chainTodo("todo-b", "nobody", "", "todo-a"),
	)
	if _, err := s.RunPlan("plan-unassigned", "alice"); err != nil {
		t.Fatalf("RunPlan: %v", err)
	}
	waitTodoStatus(t, s, "todo-a", jobstore.TodoDone, 30*time.Second)

	if got := todoStatus(t, s, "todo-b"); got != jobstore.TodoPending {
		t.Fatalf("todo-b status = %q, want pending", got)
	}
	waitPlanEventCount(t, s, "plan-unassigned", EventPlanTodoUnassigned, 1, 10*time.Second)
	if !planEventMentions(t, s, "plan-unassigned", EventPlanTodoUnassigned, "todo-b") {
		t.Fatal("plan.todo_unassigned must name the item that came due")
	}
	if n := countPlanEvent(t, s, "plan-unassigned", EventPlanCompleted); n != 0 {
		t.Fatal("a plan with a waiting item is not complete")
	}
	if jobID := todoJobID(t, s, "todo-b"); jobID != "" {
		t.Fatalf("unassigned todo-b got job %s", jobID)
	}
}

// TestPlanChainWaitsForReviewAccept (PLAN-03): a reviewed item parks the chain — its
// dependents wait for the HUMAN verdict, not for the process — and accepting it starts
// the next item.
func TestPlanChainWaitsForReviewAccept(t *testing.T) {
	s := newChainService(t, t.TempDir())
	reviewed := chainTodo("todo-b", "needs a verdict", "ok", "todo-a")
	reviewed.Review = true
	seedChain(t, s, "plan-review",
		chainTodo("todo-a", "first", "ok"),
		reviewed,
		chainTodo("todo-c", "third", "ok", "todo-b"),
	)
	if _, err := s.RunPlan("plan-review", "alice"); err != nil {
		t.Fatalf("RunPlan: %v", err)
	}

	// B's run ends in needs_review: its process is gone but the item is still `doing`.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if jobID := todoJobID(t, s, "todo-b"); jobID != "" {
			if jr, ok := s.Get(jobID); ok && jr.Status == StatusNeedsReview {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	jobID := todoJobID(t, s, "todo-b")
	jr, ok := s.Get(jobID)
	if !ok || jr.Status != StatusNeedsReview {
		t.Fatalf("todo-b job = %+v, want needs_review", jr)
	}
	if got := todoStatus(t, s, "todo-b"); got != jobstore.TodoDoing {
		t.Fatalf("todo-b status = %q, want doing while the verdict is pending", got)
	}
	if got := todoStatus(t, s, "todo-c"); got != jobstore.TodoPending {
		t.Fatalf("todo-c status = %q, want pending until the verdict", got)
	}

	if _, err := s.AcceptJob(jobID, "alice", "lgtm"); err != nil {
		t.Fatalf("AcceptJob: %v", err)
	}
	if p := waitPlanStatus(t, s, "plan-review", jobstore.PlanDone, 30*time.Second); p.Status != jobstore.PlanDone {
		t.Fatalf("plan status = %q, want done after the accept", p.Status)
	}
	if got := todoStatus(t, s, "todo-c"); got != jobstore.TodoDone {
		t.Fatalf("todo-c status = %q, want done", got)
	}
}

// TestPlanChainIgnoresAutoResumedFailure (PLAN-03): a chain item whose job failed
// TRANSIENTLY and is being continued by an automatic resume has not failed the chain —
// the plan must not park on it (the continuation is still doing the work).
func TestPlanChainIgnoresAutoResumedFailure(t *testing.T) {
	autoMax := 1
	// "flaky" dies with the transient bait; its resume template exits 0, so the
	// automatic continuation finishes the item normally.
	s := newReviewService(t, t.TempDir(), reviewServiceOpts{
		successCode: 0, stderrText: reviewTransientText, autoMax: &autoMax,
	})
	seedChain(t, s, "plan-resume",
		chainTodo("todo-a", "flaky", "flaky"),
		chainTodo("todo-b", "after", "codex", "todo-a"),
	)
	// The dispatch path submits a job with no session id, and auto-resume requires one,
	// so the item is submitted directly with the todo linkage — the shape the rule is
	// about (a todo-attached job that is being continued).
	source := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "flaky", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30,
		SessionID: "sess-chain", TodoID: "todo-a", PlanID: "plan-resume",
	})
	if source.Status != StatusFailed {
		t.Fatalf("source job status = %q, want failed", source.Status)
	}
	if rec, ok, _ := s.Meta().GetJob(source.ID); !ok || rec.AutoResumedBy == "" {
		t.Fatalf("source job %s was not auto-resumed (auto_resumed_by=%q)", source.ID, rec.AutoResumedBy)
	}
	if p, _, _ := s.Meta().GetPlan("plan-resume"); p.Status == jobstore.PlanBlocked {
		t.Fatalf("a taken-over failure parked the plan: %+v", p)
	}
	if n := countPlanEvent(t, s, "plan-resume", EventPlanBlocked); n != 0 {
		t.Fatalf("plan.blocked events = %d, want 0 for a taken-over failure", n)
	}
	// The continuation finishes the item, so the chain still reaches its end.
	if p := waitPlanStatus(t, s, "plan-resume", jobstore.PlanDone, 30*time.Second); p.Status != jobstore.PlanDone {
		t.Fatalf("plan status = %q, want done", p.Status)
	}
}

// TestExecTodoDispatchUsesCmd (PLAN-03): an `exec` item runs its `--cmd` argv (that is
// how a chain ends in a build/test step instead of an agent turn), and an exec item that
// names no command is refused with the reason stored on the item — not a job that dies
// on an empty argv.
func TestExecTodoDispatchUsesCmd(t *testing.T) {
	s := newChainService(t, t.TempDir())
	bin := testcmd.Path(t)
	seedChain(t, s, "plan-exec",
		jobstore.PlanTodo{
			TodoID: "todo-exec", Title: "verify", Status: jobstore.TodoPending,
			Assignee: agent.ExecAgentKey, Auto: true,
			Cmd: []string{bin, "argv", "chain-verify"}, CreatedAt: 1, UpdatedAt: 1,
		},
		jobstore.PlanTodo{
			TodoID: "todo-nocmd", Title: "broken", Status: jobstore.TodoPending,
			Assignee: agent.ExecAgentKey, Auto: true, CreatedAt: 1, UpdatedAt: 1,
		},
	)

	if _, err := s.RunPlan("plan-exec", "alice"); err != nil {
		t.Fatalf("RunPlan: %v", err)
	}
	// The exec item IS its argv: the dispatched job carries exactly that command.
	jobID := ""
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if jobID = todoJobID(t, s, "todo-exec"); jobID != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if jobID == "" {
		t.Fatal("exec todo was never dispatched")
	}
	final, ok := s.Wait(jobID)
	if !ok {
		t.Fatalf("job %s did not finish", jobID)
	}
	if final.Status != StatusDone {
		t.Fatalf("exec job status = %q (%s), want done", final.Status, final.Error)
	}
	if got := requestOf(t, final).Cmd; len(got) != 3 || got[2] != "chain-verify" {
		t.Fatalf("exec job cmd = %v, want the todo's argv", got)
	}

	// No --cmd: the item is queued but the dispatch is refused, and the item says why
	// rather than starting a job that would die on an empty argv.
	td, _, err := s.Meta().GetTodo("todo-nocmd")
	if err != nil {
		t.Fatalf("GetTodo(todo-nocmd): %v", err)
	}
	if !strings.Contains(td.DispatchError, "exec todo needs --cmd") {
		t.Fatalf("todo dispatch_error = %q, want the exec/--cmd message", td.DispatchError)
	}
	if got := todoJobID(t, s, "todo-nocmd"); got != "" {
		t.Fatalf("an exec todo without --cmd started job %s", got)
	}

	// And the refusal is reported when the dispatch is asked for explicitly, too.
	miss, err := s.DispatchTodo("todo-nocmd", "alice")
	if err != nil {
		t.Fatalf("DispatchTodo(todo-nocmd): %v", err)
	}
	if miss.Job != nil {
		t.Fatalf("an exec todo without --cmd started job %s", miss.Job.ID)
	}
	if !strings.Contains(miss.Reason, "exec todo needs --cmd") {
		t.Fatalf("refusal reason = %q, want the exec/--cmd message", miss.Reason)
	}
}

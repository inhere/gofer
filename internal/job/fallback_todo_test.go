package job

import (
	"strings"
	"sync"
	"testing"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/delivery"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	acprunner "github.com/inhere/gofer/internal/runner/acp"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// seedJobLineage persists the rows a lineage walk reads, for the tests that are about
// the WALK itself rather than about running a job: a done job of item A, a continuation
// carrier of item A (same item), and the continuation of item B that started from it —
// i.e. the shape a `--continue prev` job has.
func seedJobLineage(t *testing.T, s *Service) {
	t.Helper()
	for _, snap := range []JobResult{
		{
			ID: "j-item-a", ProjectKey: "self", Agent: "codex", Runner: "local",
			Status: StatusDone, TodoID: "todo-a", SessionID: "sess-a", TimeoutSec: 30,
			RequestJSON: `{"agent":"codex","prompt":"item A work","cwd":".","timeout_sec":30,"todo_id":"todo-a"}`,
		},
		{
			ID: "j-item-a-cont", ProjectKey: "self", Agent: agent.ExecAgentKey, Runner: "local",
			Status: StatusDone, TodoID: "todo-a", ResumedFrom: "j-item-a", SessionID: "sess-a",
			RequestJSON: `{"agent":"exec","cmd":["codex","--resume","sess-a"],"cwd":".","prompt":"item A rework note","todo_id":"todo-a","resumed_from":"j-item-a"}`,
		},
		{
			ID: "j-item-b", ProjectKey: "self", Agent: agent.ExecAgentKey, Runner: "local",
			Status: StatusFailed, TodoID: "todo-b", ResumedFrom: "j-item-a-cont", SessionID: "sess-a",
			RequestJSON: `{"agent":"exec","cmd":["codex","--resume","sess-a"],"cwd":".","prompt":"item B work","todo_id":"todo-b","resumed_from":"j-item-a-cont"}`,
		},
	} {
		if err := s.persist(snap); err != nil {
			t.Fatalf("seed job %s: %v", snap.ID, err)
		}
	}
}

// seedPlanItems inserts one plan with the given items — seedPlanTodo inserts a plan per
// call, so two items of the SAME plan need their own seeding.
func seedPlanItems(t *testing.T, s *Service, planID string, todoIDs ...string) {
	t.Helper()
	if err := s.Meta().InsertPlan(jobstore.Plan{PlanID: planID, Title: planID}); err != nil {
		t.Fatalf("insert plan %s: %v", planID, err)
	}
	for _, id := range todoIDs {
		if err := s.Meta().InsertTodo(jobstore.PlanTodo{TodoID: id, PlanID: planID, Title: id}); err != nil {
			t.Fatalf("insert todo %s: %v", id, err)
		}
	}
}

// TestFallbackBaseWithinStopsAtTheTodoBoundary: a TRANSFER must not walk out of its plan
// item — the request it replays would then be the previous item's task. The full walk
// (what checkResume uses to find the agent that owns the session) still crosses items.
func TestFallbackBaseWithinStopsAtTheTodoBoundary(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	seedJobLineage(t, s)
	itemB, ok := s.Get("j-item-b")
	if !ok {
		t.Fatal("seeded job j-item-b is unreadable")
	}
	contA, ok := s.Get("j-item-a-cont")
	if !ok {
		t.Fatal("seeded job j-item-a-cont is unreadable")
	}

	// The full walk reaches the previous item's own job: that is the reading checkResume
	// depends on (the exec carrier says nothing about which agent ran the session).
	if got := s.fallbackBase(itemB); got.ID != "j-item-a" {
		t.Fatalf("fallbackBase = %s, want the previous item's job j-item-a (the session owner)", got.ID)
	}
	if got := s.fallbackBaseWithin(itemB, false); got.ID != "j-item-a" {
		t.Fatalf("fallbackBaseWithin(false) = %s, want the same walk as fallbackBase", got.ID)
	}
	// Stopped at the boundary: the continuation of item B is a carrier itself, so the
	// transfer's base is that job — never item A's request.
	if got := s.fallbackBaseWithin(itemB, true); got.ID != "j-item-b" {
		t.Fatalf("fallbackBaseWithin(true) = %s, want the job itself (j-item-b): the walk must not cross into todo-a", got.ID)
	}
	// A continuation within the SAME item still resolves its chain's original job.
	if got := s.fallbackBaseWithin(contA, true); got.ID != "j-item-a" {
		t.Fatalf("fallbackBaseWithin(true) = %s, want j-item-a (same item, the walk crosses)", got.ID)
	}
}

// TestDeliveryTaskStopsAtTheTodoBoundary: a branch delivery publishes the task of the
// item it belongs to. The task text comes from the job its chain started at — which now
// stops at the todo boundary, so item B's delivery does not describe item A's work (and
// a rework continuation still delivers its own item's original ask).
func TestDeliveryTaskStopsAtTheTodoBoundary(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	seedJobLineage(t, s)
	itemB, ok := s.Get("j-item-b")
	if !ok {
		t.Fatal("seeded job j-item-b is unreadable")
	}

	plan, err := s.deliveryPlan(s.config(), itemB, DeliverOptions{Mode: delivery.ModeMerge})
	if err != nil {
		t.Fatalf("deliveryPlan: %v", err)
	}
	if plan.Title != "item B work" {
		t.Fatalf("delivery title = %q, want the item's own task", plan.Title)
	}
	if !strings.Contains(plan.Body, "item B work") {
		t.Fatalf("delivery body lacks the item's own task:\n%s", plan.Body)
	}
	if strings.Contains(plan.Body, "item A work") {
		t.Fatalf("delivery body describes the PREVIOUS item's task:\n%s", plan.Body)
	}

	// Same item, one link deeper: the chain's ROOT is still what a rework continuation
	// delivers (the rework note is not the ask that was accepted into the branch).
	contA, ok := s.Get("j-item-a-cont")
	if !ok {
		t.Fatal("seeded job j-item-a-cont is unreadable")
	}
	planA, err := s.deliveryPlan(s.config(), contA, DeliverOptions{Mode: delivery.ModeMerge})
	if err != nil {
		t.Fatalf("deliveryPlan (same item): %v", err)
	}
	if !strings.Contains(planA.Body, "item A work") || strings.Contains(planA.Body, "item A rework note") {
		t.Fatalf("delivery body of a same-item continuation = \n%s\nwant the item's original ask", planA.Body)
	}
}

// newTodoFallbackService builds the service the todo-continuation transfer tests run
// against: a healthy "codex" item agent whose built-in transient table (keyed on the
// agent name) classifies an exec carrier's death exactly as it does in a real chain, a
// prompt-carrying "failbot" that dies of the same provider error and cannot resume
// itself (so its failure goes to the candidate chain), and a healthy candidate "omp".
func newTodoFallbackService(t *testing.T, root string) *Service {
	t.Helper()
	bin := testcmd.Path(t)
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"codex", "failbot", "omp", "exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			// The item's agent: healthy, and the owner of the built-in provider-error
			// table the carrier below inherits through the ResumedFrom hop.
			"codex": {Type: agent.TypeCLIAgent, Command: bin, Args: []string{"printf", "OK"}},
			"failbot": {
				Type: agent.TypeCLIAgent, Command: bin,
				Args:                   []string{"stderr-exit", "1", "at capacity", "{{prompt}}"},
				TransientErrorPatterns: []string{"at capacity"},
			},
			"omp": {Type: agent.TypeCLIAgent, Command: bin, Args: []string{"argv", "{{prompt}}"}},
		},
	}
	return newServiceFromCfg(t, root, cfg)
}

// TestFallbackOfTodoContinuationRebuildsTheItemRequest: the takeover of a failed todo
// CONTINUATION is about THIS item. A cli continuation is an exec carrier that continues
// the previous item's session, so replaying its request would hand the candidate the
// previous item — its request is the resume argv, not this item's task book. The
// transfer therefore rebuilds the item's ordinary request through rebuildTodoRequest
// (the seam the plan dispatch owns); with no seam wired the replay stands, stopped at
// the boundary so it at least stays on the item.
func TestFallbackOfTodoContinuationRebuildsTheItemRequest(t *testing.T) {
	t.Parallel()
	const (
		planC = "plan-c"
		todoA = "todo-a"
		todoB = "todo-b"
	)
	bin := testcmd.Path(t)

	// setup seeds the item pair and runs item A's job — the done run a todo continuation
	// starts from. rebuild (nil = no seam) is wired BEFORE anything runs: a transfer is
	// submitted on the finish path of the job it takes over.
	setup := func(t *testing.T, rebuild func(string) (JobRequest, error)) (*Service, JobResult) {
		t.Helper()
		s := newTodoFallbackService(t, t.TempDir())
		s.rebuildTodoRequest = rebuild
		seedPlanItems(t, s, planC, todoA, todoB)

		prev := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "codex", Runner: "local",
			Prompt: "item A work", Cwd: ".", TimeoutSec: 30, TodoID: todoA,
			SessionID: "sess-item-a", Title: "item A",
		})
		if prev.Status != StatusDone {
			t.Fatalf("setup: previous item = %s (err=%s), want done", prev.Status, prev.Error)
		}
		return s, prev
	}

	t.Run("the item's ordinary request is rebuilt", func(t *testing.T) {
		var (
			mu    sync.Mutex
			asked []string
		)
		s, prev := setup(t, func(todoID string) (JobRequest, error) {
			mu.Lock()
			asked = append(asked, todoID)
			mu.Unlock()
			return JobRequest{
				ProjectKey: "self", Agent: "codex", Runner: "local",
				Prompt: "item B work", Cwd: ".", TimeoutSec: 30,
				TodoID: todoID, Title: "item B", RulesResolved: true,
			}, nil
		})
		// The continuation runs the PREVIOUS item's session through an exec carrier and
		// dies of a provider error: the shape a `--continue prev` cli job has.
		cont := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: agent.ExecAgentKey, Runner: "local",
			ResumeSourceAgent: "codex",
			Cmd:               []string{bin, "stderr-exit", "1", "at capacity"},
			Cwd:               ".", TimeoutSec: 30,
			TodoID: todoB, ResumedFrom: prev.ID, SessionID: prev.SessionID,
			Title: "item B (resumed)", RulesResolved: true,
			FallbackAgents: []string{"omp"},
		})
		if cont.Status != StatusFailed {
			t.Fatalf("setup: continuation = %s (err=%s), want the provider error to fail it", cont.Status, cont.Error)
		}

		cont = waitFellBack(t, s, cont.ID)
		mu.Lock()
		got := append([]string(nil), asked...)
		mu.Unlock()
		if len(got) != 1 || got[0] != todoB {
			t.Fatalf("rebuildTodoRequest called with %v, want exactly [%s]", got, todoB)
		}
		next, ok := s.Get(cont.FellBackTo)
		if !ok {
			t.Fatalf("takeover job %s not found", cont.FellBackTo)
		}
		if next.Agent != "omp" || next.FellBackFrom != cont.ID {
			t.Fatalf("takeover = agent %q from %q, want omp/%s", next.Agent, next.FellBackFrom, cont.ID)
		}
		if next.TodoID != todoB || next.PlanID != planC {
			t.Fatalf("takeover item = %q/%q, want %s/%s (the item it continues)", next.TodoID, next.PlanID, todoB, planC)
		}
		if next.SessionID != "" || next.ResumedFrom != "" || next.AutoResumeAttempt != 0 {
			t.Fatalf("takeover must start a fresh session: session=%q resumed_from=%q attempt=%d",
				next.SessionID, next.ResumedFrom, next.AutoResumeAttempt)
		}
		if next.RequestedAgent != "codex" {
			t.Fatalf("requested_agent = %q, want the agent behind the carrier (codex)", next.RequestedAgent)
		}
		if next.Cwd != cont.Cwd {
			t.Fatalf("takeover cwd = %q, want the failed continuation's %q", next.Cwd, cont.Cwd)
		}
		final, _ := s.Wait(next.ID)
		if final.Status != StatusDone {
			t.Fatalf("takeover status = %s (err=%s, verify=%+v), want done", final.Status, final.Error, final.Verify)
		}
		// The rebuilt task travels with the handover note; the carrier's argv does not.
		for _, want := range []string{"上一次由 exec 执行", "at capacity", "item B work"} {
			if !strings.Contains(final.RenderedCommand, want) {
				t.Fatalf("takeover argv = %q, want it to carry %q", final.RenderedCommand, want)
			}
		}
		if strings.Contains(final.RenderedCommand, "stderr-exit") {
			t.Fatalf("the rebuilt takeover must not replay the carrier argv: %q", final.RenderedCommand)
		}
		// The item that was continued is untouched: its own job stays the one the checklist
		// names, and the takeover is the item-b run.
		if td := getTodo(t, s, todoA); td.JobID != prev.ID || td.Status != jobstore.TodoDone {
			t.Fatalf("the continued item changed: %+v", td)
		}
		if td := getTodo(t, s, todoB); td.JobID != next.ID {
			t.Fatalf("item b job = %q, want the takeover %s", td.JobID, next.ID)
		}
	})

	t.Run("without the seam the replay stands", func(t *testing.T) {
		s, prev := setup(t, nil)
		// A continuation that carries this item's own prompt (the acp shape) and has
		// already spent its own automatic continuation — the point at which the candidate
		// chain takes over. With no seam the transfer replays that request, which is what
		// it did before the seam existed.
		cont := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "failbot", Runner: "local",
			Prompt: "item B work", Cwd: ".", TimeoutSec: 30,
			TodoID: todoB, ResumedFrom: prev.ID, SessionID: prev.SessionID,
			AutoResumeAttempt: 1,
			Title:             "item B (resumed)", FallbackAgents: []string{"omp"},
		})
		if cont.Status != StatusFailed {
			t.Fatalf("setup: continuation = %s (err=%s), want the provider error to fail it", cont.Status, cont.Error)
		}

		cont = waitFellBack(t, s, cont.ID)
		next, ok := s.Get(cont.FellBackTo)
		if !ok {
			t.Fatalf("takeover job %s not found", cont.FellBackTo)
		}
		if next.Agent != "omp" || next.TodoID != todoB {
			t.Fatalf("takeover = agent %q item %q, want omp/%s (the boundary stop alone keeps it on the item)", next.Agent, next.TodoID, todoB)
		}
		final, _ := s.Wait(next.ID)
		if final.Status != StatusDone {
			t.Fatalf("takeover status = %s (err=%s), want done", final.Status, final.Error)
		}
		for _, want := range []string{"上一次由 failbot 执行", "at capacity", "item B work"} {
			if !strings.Contains(final.RenderedCommand, want) {
				t.Fatalf("takeover argv = %q, want it to carry %q", final.RenderedCommand, want)
			}
		}
		// The previous item's request is NOT what the candidate re-runs.
		if strings.Contains(final.RenderedCommand, "item A work") {
			t.Fatalf("the takeover replayed the previous item: %q", final.RenderedCommand)
		}
		if td := getTodo(t, s, todoA); td.JobID != prev.ID {
			t.Fatalf("the continued item changed: %+v", td)
		}
	})
}

// newTodoContinuationACPService wires a Service whose only agent is the fake ACP server
// (every turn dies with a provider error) plus a healthy cli "helper" for the previous
// item. An ACP continuation is the todo continuation that CAN be auto-resumed: a cli
// one is an exec carrier, which has no resume argv of its own.
func newTodoContinuationACPService(t *testing.T, root string) *Service {
	t.Helper()
	const transient = "stream disconnected before completion"
	bin := testcmd.Path(t)
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"acpbot", "helper"},
				AllowedRunners: []string{"local"},
			},
		},
		Agents: map[string]config.AgentConfig{
			"acpbot": {
				Type: agent.TypeACPAgent, Command: bin,
				Args: acptest.CmdArgs(acptest.Options{PromptError: transient}),
				// The built-in transient table is keyed on the agent's command (here the
				// test binary), so the patterns are declared explicitly.
				TransientErrorPatterns: []string{"stream disconnected"},
			},
			"helper": {Type: agent.TypeCLIAgent, Command: bin, Args: []string{"printf", "OK"}},
		},
	}
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{
		localrunner.Name: localrunner.New(),
		acprunner.Name:   acprunner.New(),
	}
	meta, err := jobstore.Open(jobstoreDBPath(root))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	return drainOnClose(t, NewService(cfg, projReg, agentReg, runners, meta, nil))
}

// TestTodoContinuationAutoResumeKeepsTheItem: the automatic continuation of a failed todo
// continuation still works — it continues the SAME session and stays on the item. The
// todo-boundary stop belongs to the transfer only; the resume-side walk that recognises
// the session's agent is untouched.
func TestTodoContinuationAutoResumeKeepsTheItem(t *testing.T) {
	t.Parallel()
	s := newTodoContinuationACPService(t, t.TempDir())
	seedPlanItems(t, s, "plan-c", "todo-a", "todo-b")

	prev := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "helper", Runner: "local",
		Prompt: "item A work", Cwd: ".", TimeoutSec: 30, TodoID: "todo-a",
		SessionID: "sess-item-a", Title: "item A",
	})
	if prev.Status != StatusDone {
		t.Fatalf("setup: previous item = %s (err=%s), want done", prev.Status, prev.Error)
	}
	cont := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local",
		Prompt: acpTestPrompt, Cwd: ".", TimeoutSec: 60,
		TodoID: "todo-b", ResumedFrom: prev.ID, SessionID: acptest.SessionID,
		Title: "item B (resumed)", RulesResolved: true,
	})
	if cont.Status != StatusFailed {
		t.Fatalf("setup: continuation = %s (err=%s), want the provider error to fail it", cont.Status, cont.Error)
	}
	cont = waitAutoResumed(t, s, cont.ID, true)
	resumed, ok := s.Get(cont.AutoResumedBy)
	if !ok {
		t.Fatalf("continuation %s not found", cont.AutoResumedBy)
	}
	if resumed.TodoID != "todo-b" || resumed.PlanID != "plan-c" {
		t.Fatalf("automatic continuation item = %q/%q, want todo-b/plan-c", resumed.TodoID, resumed.PlanID)
	}
	if resumed.ResumedFrom != cont.ID || resumed.AutoResumeAttempt != 1 || resumed.SessionID != acptest.SessionID {
		t.Fatalf("automatic continuation lineage = %q/%d/%q, want %s/1/%s",
			resumed.ResumedFrom, resumed.AutoResumeAttempt, resumed.SessionID, cont.ID, acptest.SessionID)
	}
	if td := getTodo(t, s, "todo-a"); td.JobID != prev.ID {
		t.Fatalf("the continued item changed: %+v", td)
	}
	// The automatic continuation dies the same way (no further attempt is left): wait it
	// out so the TempDir is not removed under a running job.
	_, _ = s.Wait(resumed.ID)
	drainJobs(t, s)
}

// TestTodoContinuationResumeRestoresTheRealAgent: `job resume` of a todo continuation
// still resolves the CLI behind its exec carrier. The carrier continues a DIFFERENT item,
// so a boundary-stopped walk would resolve "exec" (which owns no resume argv) and refuse
// the resume.
func TestTodoContinuationResumeRestoresTheRealAgent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"codex", "exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Type: agent.TypeCLIAgent, Command: bin, Args: []string{"printf", "OK"},
				SessionResume: []string{"argv", "--resume", "{{session_id}}", "{{prompt}}"},
			},
		},
	}
	s := newServiceFromCfg(t, root, cfg)
	seedPlanItems(t, s, "plan-c", "todo-a", "todo-b")

	prev := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "item A work", Cwd: ".", TimeoutSec: 30, TodoID: "todo-a",
		SessionID: "sess-item-a", Title: "item A",
	})
	carrier := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: agent.ExecAgentKey, Runner: "local",
		ResumeSourceAgent: "codex", Cmd: []string{bin, "printf", "OK"},
		Cwd: ".", TimeoutSec: 30,
		TodoID: "todo-b", ResumedFrom: prev.ID, SessionID: prev.SessionID,
		Title: "item B (resumed)", RulesResolved: true,
	})
	if carrier.Status != StatusDone {
		t.Fatalf("setup: continuation carrier = %s (err=%s), want done", carrier.Status, carrier.Error)
	}

	resumed, err := s.ResumeJob(carrier.ID, "item B rework", "", "alice")
	if err != nil {
		t.Fatalf("resuming a todo continuation must resolve the agent behind the carrier, got %v", err)
	}
	if resumed.TodoID != "todo-b" || resumed.ResumedFrom != carrier.ID {
		t.Fatalf("resumed item/lineage = %q/%q, want todo-b/%s", resumed.TodoID, resumed.ResumedFrom, carrier.ID)
	}
	argv := resumeArgv(t, resumed.RequestJSON)
	want := []string{bin, "argv", "--resume", prev.SessionID, "item B rework"}
	if !equalArgs(argv, want) {
		t.Fatalf("resumed argv = %q, want %q (the codex resume template)", argv, want)
	}
	if final, _ := s.Wait(resumed.ID); final.Status != StatusDone {
		t.Fatalf("resumed status = %s (err=%s), want done", final.Status, final.Error)
	}
}

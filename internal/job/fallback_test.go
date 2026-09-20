package job

import (
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/util"
)

// newFallbackService builds a service whose "codex" agent dies with codexStderr and
// exits 1 (a provider-error stand-in) while the candidates "omp"/"claude" succeed —
// so a transfer can be observed end to end. codex declares an agent-level
// fallback_agents: [omp]; a test that needs another chain overrides the config.
func newFallbackService(t *testing.T, root, codexStderr string) *Service {
	t.Helper()
	cfg := fallbackCfg(t, root, codexStderr)
	return newServiceFromCfg(t, root, cfg)
}

// fallbackCfg is the config newFallbackService builds, exposed so a test can tweak
// one field (candidate list, allowed agents) and rebuild the service.
func fallbackCfg(t *testing.T, root, codexStderr string) *config.Config {
	t.Helper()
	bin := testcmd.Path(t)
	return &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"codex", "omp", "claude", "exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Type:    agent.TypeCLIAgent,
				Command: bin,
				Args:    []string{"stderr-exit", "1", codexStderr, "{{prompt}}"},
				// A resume template that dies the same way: the "same agent already
				// tried its own continuation" case.
				SessionResume:  []string{"stderr-exit", "1", codexStderr, "{{prompt}}"},
				FallbackAgents: []string{"omp"},
			},
			// The candidates render the prompt into their own argv (testcmd's `argv`
			// echoes what the child actually received) so a test can assert what the
			// takeover was asked to do; `printf` is the minimal successful mode for the
			// cases that only care that the new agent ran.
			"omp": {
				Type:    agent.TypeCLIAgent,
				Command: bin,
				Args:    []string{"argv", "{{prompt}}"},
			},
			"claude": {
				Type:    agent.TypeCLIAgent,
				Command: bin,
				Args:    []string{"printf", "OK"},
			},
		},
	}
}

// waitFellBack returns the source snapshot once it records the job that took over
// (the transfer is submitted on the finish path, after the terminal row lands).
func waitFellBack(t *testing.T, s *Service, id string) JobResult {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, ok := s.Get(id)
		if !ok {
			t.Fatalf("job %s vanished", id)
		}
		if got.FellBackTo != "" || time.Now().After(deadline) {
			if got.FellBackTo == "" {
				t.Fatalf("job %s recorded no fallback (status=%s class=%q err=%s)", id, got.Status, got.FailureClass, got.Error)
			}
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// seedAgentFailures writes n recent transient failures for an agent straight into
// the store — the health history a pre-dispatch decision reads.
func seedAgentFailures(t *testing.T, s *Service, agentKey string, n int, class string) {
	t.Helper()
	now := s.Now().Unix()
	for i := range n {
		rec := jobstore.JobRecord{
			ID:           "seeded-" + agentKey + "-" + string(rune('a'+i)),
			ProjectKey:   "self",
			Agent:        agentKey,
			Runner:       "local",
			Status:       "failed",
			FailureClass: class,
			StartedAt:    now - 60,
			EndedAt:      now - 30,
			UpdatedAt:    now - 30,
		}
		if err := s.Meta().UpsertJob(rec); err != nil {
			t.Fatalf("seed %s failure: %v", agentKey, err)
		}
	}
}

// TestFallbackAfterTransientFailure: a job whose agent died with a provider error,
// with no session to continue, is taken over by the next candidate — the new job
// runs the SAME work under the备用 agent, names the failure in its prompt, keeps the
// cwd, points back at the source, and the source row reports the transfer instead of
// a terminal failure.
func TestFallbackAfterTransientFailure(t *testing.T) {
	root := t.TempDir()
	s := newFallbackService(t, root, "ERROR: Selected model is at capacity. Please try a different model.")

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30,
		Title: "big task", Tags: []string{"nightly"},
	})
	if src.Status != StatusFailed {
		t.Fatalf("setup: status = %s, want failed", src.Status)
	}
	if src.FailureClass != FailureClassTransient {
		t.Fatalf("failure_class = %q, want %q", src.FailureClass, FailureClassTransient)
	}
	src = waitFellBack(t, s, src.ID)

	next, ok := s.Get(src.FellBackTo)
	if !ok {
		t.Fatalf("fallback job %s not found", src.FellBackTo)
	}
	if next.Agent != "omp" {
		t.Fatalf("fallback agent = %q, want omp", next.Agent)
	}
	if next.FellBackFrom != src.ID {
		t.Fatalf("fell_back_from = %q, want the source %s", next.FellBackFrom, src.ID)
	}
	if next.RequestedAgent != "codex" {
		t.Fatalf("requested_agent = %q, want the agent the caller asked for (codex)", next.RequestedAgent)
	}
	if next.Fallback == nil || strings.Join(next.Fallback.Candidates, ",") != "omp" || next.Fallback.Depth != 1 {
		t.Fatalf("fallback state = %+v, want candidates [omp] at depth 1", next.Fallback)
	}
	if next.Cwd != src.Cwd {
		t.Fatalf("fallback cwd = %q, want the source's %q (the work stays where it was)", next.Cwd, src.Cwd)
	}
	if next.SessionID != "" {
		t.Fatalf("fallback session = %q, want a fresh session", next.SessionID)
	}
	if strings.Join(next.Tags, ",") != "nightly" || next.TimeoutSec != 30 {
		t.Fatalf("fallback must inherit tags/timeout: %+v", next)
	}
	if !strings.Contains(next.Title, "big task") || !strings.Contains(next.Title, "→omp") {
		t.Fatalf("fallback title = %q, want the source title marked with the new agent", next.Title)
	}

	final, _ := s.Wait(next.ID)
	if final.Status != StatusDone {
		t.Fatalf("fallback status = %s (err=%s), want done", final.Status, final.Error)
	}
	// The continuation prompt tells the new agent what happened and forbids redoing
	// committed work; the original prompt still travels with it.
	for _, want := range []string{"上一次由 codex 执行", "at capacity", "do the thing"} {
		if !strings.Contains(final.RenderedCommand, want) {
			t.Fatalf("fallback argv = %q, want it to carry %q", final.RenderedCommand, want)
		}
	}

	// The source is not announced as a terminal failure — the takeover is its outcome.
	types := eventTypes(t, s, src.ID)
	if !hasSubsequence(types, []string{EventJobFellBack}) {
		t.Fatalf("source lacks %s: %v", EventJobFellBack, types)
	}
	if hasSubsequence(types, []string{EventJobTerminal}) {
		t.Fatalf("source must not emit %s when taken over: %v", EventJobTerminal, types)
	}
	if !hasSubsequence(eventTypes(t, s, next.ID), []string{EventJobTerminal}) {
		t.Fatalf("the takeover's own terminal state must be notified normally")
	}
}

// TestFallbackNotOnNonTransient: a real bug (not a provider glitch) must not be
// handed to another agent — the failure is terminal, the class says "other", and
// nothing was submitted.
func TestFallbackNotOnNonTransient(t *testing.T) {
	root := t.TempDir()
	s := newFallbackService(t, root, "panic: nil pointer dereference in the agent")

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "x", Cwd: ".", TimeoutSec: 30,
	})
	if src.Status != StatusFailed {
		t.Fatalf("setup: status = %s", src.Status)
	}
	if src.FailureClass != FailureClassOther {
		t.Fatalf("failure_class = %q, want %q", src.FailureClass, FailureClassOther)
	}
	if src.FellBackTo != "" {
		t.Fatalf("a non-transient failure must not be transferred, got fell_back_to=%q", src.FellBackTo)
	}
	types := eventTypes(t, s, src.ID)
	if hasSubsequence(types, []string{EventJobFellBack}) {
		t.Fatalf("no transfer event expected: %v", types)
	}
	if !hasSubsequence(types, []string{EventJobTerminal}) {
		t.Fatal("a non-transient failure must emit job.terminal")
	}
}

// TestFallbackAfterAutoResumeAlsoFails: when the same agent has already spent its
// automatic continuation and dies again, the chain moves to the备用 agent — and it
// resubmits the ORIGINAL work (the prompt of the job the continuation carried), not
// the continuation's exec argv.
func TestFallbackAfterAutoResumeAlsoFails(t *testing.T) {
	root := t.TempDir()
	s := newFallbackService(t, root, "429 too many requests")

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "finish the migration", Cwd: ".", TimeoutSec: 30, SessionID: "sess-fb",
	})
	if src.Status != StatusFailed {
		t.Fatalf("setup: status = %s", src.Status)
	}
	// First failure: the agent has a session, so it is continued instead of transferred.
	src = waitAutoResumed(t, s, src.ID, true)
	if src.FellBackTo != "" {
		t.Fatalf("the first failure must be continued, not transferred: %+v", src)
	}
	cont, ok := s.Get(src.AutoResumedBy)
	if !ok {
		t.Fatalf("continuation %s not found", src.AutoResumedBy)
	}
	cont = waitFellBack(t, s, cont.ID)

	next, ok := s.Get(cont.FellBackTo)
	if !ok {
		t.Fatalf("fallback job %s not found", cont.FellBackTo)
	}
	if next.Agent != "omp" || next.FellBackFrom != cont.ID || next.RequestedAgent != "codex" {
		t.Fatalf("fallback = agent %q from %q requested %q, want omp/%s/codex", next.Agent, next.FellBackFrom, next.RequestedAgent, cont.ID)
	}
	final, _ := s.Wait(next.ID)
	if final.Status != StatusDone {
		t.Fatalf("fallback status = %s (err=%s), want done", final.Status, final.Error)
	}
	// The original prompt (not the continuation's exec argv) is what the备用 agent gets.
	for _, want := range []string{"上一次由 codex 执行", "finish the migration"} {
		if !strings.Contains(final.RenderedCommand, want) {
			t.Fatalf("fallback argv = %q, want it to carry %q", final.RenderedCommand, want)
		}
	}
	if final.SessionID != "" {
		t.Fatalf("fallback session = %q, want a fresh session", final.SessionID)
	}
	// The continuation itself is not announced as terminal either.
	if hasSubsequence(eventTypes(t, s, cont.ID), []string{EventJobTerminal}) {
		t.Fatalf("the continued job must not emit %s when taken over", EventJobTerminal)
	}
	if !hasSubsequence(eventTypes(t, s, cont.ID), []string{EventJobFellBack}) {
		t.Fatalf("the continued job must record %s", EventJobFellBack)
	}
}

// TestFallbackChainExhaustedRecordsTerminal: the chain is bounded by the candidate
// count — after the last candidate fails the same way the job is a terminal failure
// (no fourth attempt, no self-recursion).
func TestFallbackChainExhaustedRecordsTerminal(t *testing.T) {
	root := t.TempDir()
	cfg := fallbackCfg(t, root, "at capacity")
	ac := cfg.Agents["codex"]
	ac.FallbackAgents = []string{"omp", "helper"}
	cfg.Agents["codex"] = ac
	// Both candidates die the same way, so the chain has to run to its end. `helper`
	// carries explicit patterns and no session support on purpose: a session-capable
	// candidate would be continued once by its OWN automatic resume instead of
	// terminating, which is a different (already covered) path.
	bad := cfg.Agents["omp"]
	bad.Args = []string{"stderr-exit", "1", "at capacity", "{{prompt}}"}
	cfg.Agents["omp"] = bad
	cfg.Agents["helper"] = config.AgentConfig{
		Type:                   agent.TypeCLIAgent,
		Command:                testcmd.Path(t),
		Args:                   []string{"stderr-exit", "1", "at capacity", "{{prompt}}"},
		TransientErrorPatterns: []string{"at capacity"},
	}
	proj := cfg.Projects["self"]
	proj.AllowedAgents = []string{"codex", "omp", "helper", "exec"}
	cfg.Projects["self"] = proj
	s := newServiceFromCfg(t, root, cfg)

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "x", Cwd: ".", TimeoutSec: 30,
	})
	src = waitFellBack(t, s, src.ID)
	second, _ := s.Get(src.FellBackTo)
	if second.Agent != "omp" || second.Fallback == nil || second.Fallback.Depth != 1 {
		t.Fatalf("first hop = agent %q state %+v, want omp at depth 1", second.Agent, second.Fallback)
	}
	second = waitFellBack(t, s, second.ID)
	third, _ := s.Get(second.FellBackTo)
	if third.Agent != "helper" || third.Fallback == nil || third.Fallback.Depth != 2 {
		t.Fatalf("second hop = agent %q state %+v, want helper at depth 2", third.Agent, third.Fallback)
	}
	final, _ := s.Wait(third.ID)
	if final.Status != StatusFailed {
		t.Fatalf("the last candidate's failure must be terminal, got %s", final.Status)
	}
	if final.FellBackTo != "" {
		t.Fatalf("the chain must stop after %d candidates, got another hop to %q", len(third.Fallback.Candidates), final.FellBackTo)
	}
	types := eventTypes(t, s, third.ID)
	if hasSubsequence(types, []string{EventJobFellBack}) {
		t.Fatalf("no further transfer expected: %v", types)
	}
	if !hasSubsequence(types, []string{EventJobTerminal}) {
		t.Fatalf("the exhausted chain must emit %s: %v", EventJobTerminal, types)
	}
}

// TestFallbackSkipsAgentsNotAllowedInProject: a candidate the project does not
// admit is dropped when the candidates are resolved (not at failure time), and when
// nothing is left the job simply fails instead of being handed to an agent the
// project never allowed.
func TestFallbackSkipsAgentsNotAllowedInProject(t *testing.T) {
	t.Run("skips and keeps the allowed one", func(t *testing.T) {
		root := t.TempDir()
		cfg := fallbackCfg(t, root, "at capacity")
		ac := cfg.Agents["codex"]
		ac.FallbackAgents = []string{"claude", "omp"} // claude first, but not allowed
		cfg.Agents["codex"] = ac
		proj := cfg.Projects["self"]
		proj.AllowedAgents = []string{"codex", "omp", "exec"}
		cfg.Projects["self"] = proj
		s := newServiceFromCfg(t, root, cfg)

		src := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "codex", Runner: "local",
			Prompt: "x", Cwd: ".", TimeoutSec: 30,
		})
		src = waitFellBack(t, s, src.ID)
		next, _ := s.Get(src.FellBackTo)
		if next.Agent != "omp" {
			t.Fatalf("fallback agent = %q, want the only allowed candidate omp", next.Agent)
		}
		if next.Fallback == nil || strings.Join(next.Fallback.Candidates, ",") != "omp" {
			t.Fatalf("resolved candidates = %+v, want the admitted list [omp] only", next.Fallback)
		}
	})

	t.Run("no candidate left means no transfer", func(t *testing.T) {
		root := t.TempDir()
		cfg := fallbackCfg(t, root, "at capacity")
		proj := cfg.Projects["self"]
		proj.AllowedAgents = []string{"codex", "exec"} // omp is not admitted either
		cfg.Projects["self"] = proj
		s := newServiceFromCfg(t, root, cfg)

		src := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "codex", Runner: "local",
			Prompt: "x", Cwd: ".", TimeoutSec: 30,
		})
		if src.Status != StatusFailed || src.FellBackTo != "" {
			t.Fatalf("job = status %s fell_back_to %q, want a plain failure with no transfer", src.Status, src.FellBackTo)
		}
		if src.FailureClass != FailureClassTransient {
			t.Fatalf("failure_class = %q, want the transient classification to stand on its own", src.FailureClass)
		}
		if !hasSubsequence(eventTypes(t, s, src.ID), []string{EventJobTerminal}) {
			t.Fatal("with no candidate the failure must emit job.terminal")
		}
	})
}

// TestNoFallbackFlag: --no-fallback turns the transfer off for one job, whatever the
// config says, and the failure is announced as terminal.
func TestNoFallbackFlag(t *testing.T) {
	root := t.TempDir()
	s := newFallbackService(t, root, "at capacity")

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "x", Cwd: ".", TimeoutSec: 30, NoFallback: true,
	})
	if src.Status != StatusFailed || src.FellBackTo != "" {
		t.Fatalf("job = status %s fell_back_to %q, want a plain failure", src.Status, src.FellBackTo)
	}
	types := eventTypes(t, s, src.ID)
	if hasSubsequence(types, []string{EventJobFellBack}) {
		t.Fatalf("--no-fallback must submit nothing: %v", types)
	}
	if !hasSubsequence(types, []string{EventJobTerminal}) {
		t.Fatal("--no-fallback must emit job.terminal")
	}
}

// TestFallbackInheritsWorktreeTodoVerify: the takeover keeps the whole context of the
// work — it continues INSIDE the source's worktree (no second worktree), stays on the
// same checklist item, and carries the verify step the source was submitted with.
func TestFallbackInheritsWorktreeTodoVerify(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)
	bin := testcmd.Path(t)
	s := newFallbackService(t, root, "at capacity")
	seedPlanTodo(t, s, "plan-fb", "todo-fb")

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "x", Cwd: ".", TimeoutSec: 30, Worktree: true,
		TodoID: "todo-fb", Verify: []string{bin, "exit", "0"},
	})
	if src.Status != StatusFailed || src.WorktreePath == "" {
		t.Fatalf("setup: status=%s worktree=%q err=%s", src.Status, src.WorktreePath, src.Error)
	}
	src = waitFellBack(t, s, src.ID)
	next, _ := s.Get(src.FellBackTo)
	if next.WorktreePath != "" {
		t.Fatalf("a fallback continues in the source worktree, it must not create another one (got %q)", next.WorktreePath)
	}
	// Same-directory, possibly different spelling: the worktree path is git's
	// RESOLVED form, the continuation's cwd is resolved against the configured
	// project root (macOS /var vs /private/var).
	if !strings.HasPrefix(util.RealPath(next.Cwd), util.RealPath(src.WorktreePath)) {
		t.Fatalf("fallback cwd = %q, want it inside the source worktree %q", next.Cwd, src.WorktreePath)
	}
	final, _ := s.Wait(next.ID)
	if final.Status != StatusDone {
		t.Fatalf("fallback status = %s (err=%s, verify=%+v), want done", final.Status, final.Error, final.Verify)
	}
	if final.TodoID != "todo-fb" || final.PlanID != "plan-fb" {
		t.Fatalf("fallback must stay on the checklist item: todo=%q plan=%q", final.TodoID, final.PlanID)
	}
	if final.Verify == nil || final.Verify.Status != VerifyPassed {
		t.Fatalf("the inherited verify step must have run and passed: %+v", final.Verify)
	}
	if td := getTodo(t, s, "todo-fb"); td.Status != jobstore.TodoDone {
		t.Fatalf("todo status = %q, want the chain's outcome to land on the item", td.Status)
	}
}

// noFallbackService is newFallbackService without any candidate list: a failing cli
// agent whose failures are observed in place.
func noFallbackService(t *testing.T, root, codexStderr string) *Service {
	t.Helper()
	cfg := fallbackCfg(t, root, codexStderr)
	ac := cfg.Agents["codex"]
	ac.FallbackAgents = nil
	cfg.Agents["codex"] = ac
	return newServiceFromCfg(t, root, cfg)
}

// TestFailureClassPersisted: every failed job records WHY it is classified as it is —
// transient (a provider error, matched without regard to whether any continuation or
// transfer is enabled) or other — and it survives a read from the store; a job that
// succeeded carries no class at all.
func TestFailureClassPersisted(t *testing.T) {
	root := t.TempDir()
	s := noFallbackService(t, root, "stream disconnected")
	transient := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "x", Cwd: ".", TimeoutSec: 30,
	})
	if transient.FailureClass != FailureClassTransient {
		t.Fatalf("failure_class = %q, want transient (status=%s err=%s)", transient.FailureClass, transient.Status, transient.Error)
	}
	rec, ok, err := s.Meta().GetJob(transient.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob(%s): ok=%v err=%v", transient.ID, ok, err)
	}
	if rec.FailureClass != FailureClassTransient {
		t.Fatalf("persisted failure_class = %q, want transient", rec.FailureClass)
	}

	otherRoot := t.TempDir()
	otherSvc := noFallbackService(t, otherRoot, "panic: nil pointer dereference in the agent")
	other := submitAndWait(t, otherSvc, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "x", Cwd: ".", TimeoutSec: 30,
	})
	if other.FailureClass != FailureClassOther {
		t.Fatalf("failure_class = %q, want other for a non-provider failure", other.FailureClass)
	}

	done := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local",
		Prompt: "x", Cwd: ".", TimeoutSec: 30,
	})
	if done.Status != StatusDone || done.FailureClass != "" {
		t.Fatalf("job = status %s class %q, want a success carrying no failure class", done.Status, done.FailureClass)
	}
}

// TestPreDispatchSubstitutesDegradedAgent: with agent_fallback.pre_dispatch on, a
// submit whose agent is currently degraded runs on the first candidate that is not —
// while the row still records which agent the caller asked for — and the event says
// why. Off (the default), the requested agent runs as asked.
func TestPreDispatchSubstitutesDegradedAgent(t *testing.T) {
	newDegradedService := func(t *testing.T, root string) *Service {
		t.Helper()
		cfg := fallbackCfg(t, root, "at capacity")
		ac := cfg.Agents["codex"]
		ac.FallbackAgents = []string{"omp", "claude"}
		cfg.Agents["codex"] = ac
		pre := true
		cfg.Server.AgentFallback = &config.AgentFallbackConfig{PreDispatch: pre}
		return newServiceFromCfg(t, root, cfg)
	}

	t.Run("pre_dispatch off keeps the requested agent", func(t *testing.T) {
		root := t.TempDir()
		s := newFallbackService(t, root, "at capacity")
		seedAgentFailures(t, s, "codex", 3, FailureClassTransient)
		src := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "codex", Runner: "local",
			Prompt: "x", Cwd: ".", TimeoutSec: 30,
		})
		if src.RequestedAgent != "" {
			t.Fatalf("requested_agent = %q, want empty when nothing was substituted", src.RequestedAgent)
		}
	})

	t.Run("degraded agent is substituted", func(t *testing.T) {
		root := t.TempDir()
		s := newDegradedService(t, root)
		seedAgentFailures(t, s, "codex", 3, FailureClassTransient)

		res, err := s.Submit(JobRequest{
			ProjectKey: "self", Agent: "codex", Runner: "local",
			Prompt: "x", Cwd: ".", TimeoutSec: 30,
		})
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if res.Agent != "omp" {
			t.Fatalf("substituted agent = %q, want the first non-degraded candidate omp", res.Agent)
		}
		if res.RequestedAgent != "codex" {
			t.Fatalf("requested_agent = %q, want the agent the caller asked for (codex)", res.RequestedAgent)
		}
		final, _ := s.Wait(res.ID)
		if final.Status != StatusDone {
			t.Fatalf("substituted job status = %s (err=%s), want done", final.Status, final.Error)
		}
		// The chain continues from the substituted position: a later failure has the
		// remaining candidates, not the whole list again.
		if final.Fallback == nil || final.Fallback.Depth != 1 || strings.Join(final.Fallback.Candidates, ",") != "omp,claude" {
			t.Fatalf("fallback state = %+v, want the candidates kept with depth 1", final.Fallback)
		}
		types := eventTypes(t, s, res.ID)
		if !hasSubsequence(types, []string{EventJobAgentSubstituted}) {
			t.Fatalf("substitution must record %s: %v", EventJobAgentSubstituted, types)
		}
	})

	t.Run("a healthy candidate is preferred over an unknown one", func(t *testing.T) {
		root := t.TempDir()
		s := newDegradedService(t, root)
		seedAgentFailures(t, s, "codex", 3, FailureClassTransient)
		seedAgentFailures(t, s, "omp", 3, FailureClassTransient) // omp is degraded too
		res, err := s.Submit(JobRequest{
			ProjectKey: "self", Agent: "codex", Runner: "local",
			Prompt: "x", Cwd: ".", TimeoutSec: 30,
		})
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if res.Agent != "claude" {
			t.Fatalf("substituted agent = %q, want claude (the first candidate that is not degraded)", res.Agent)
		}
	})

	t.Run("all candidates degraded means no substitution", func(t *testing.T) {
		root := t.TempDir()
		s := newDegradedService(t, root)
		seedAgentFailures(t, s, "codex", 3, FailureClassTransient)
		seedAgentFailures(t, s, "omp", 3, FailureClassTransient)
		seedAgentFailures(t, s, "claude", 3, FailureClassTransient)
		res, err := s.Submit(JobRequest{
			ProjectKey: "self", Agent: "codex", Runner: "local",
			Prompt: "x", Cwd: ".", TimeoutSec: 30,
		})
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if res.Agent != "codex" || res.RequestedAgent != "" {
			t.Fatalf("job = agent %q requested %q, want the requested codex untouched", res.Agent, res.RequestedAgent)
		}
		if hasSubsequence(eventTypes(t, s, res.ID), []string{EventJobAgentSubstituted}) {
			t.Fatal("nothing was substituted, so no substitution event is expected")
		}
	})
}

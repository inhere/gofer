package job

import (
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// newAutoResumeService builds a service whose "codex" agent is a test binary
// that prints stderrText and exits 1 (a provider error stand-in), with a resume
// template that runs successfully (printf), so an automatic continuation can be
// observed end to end. autoMax is the server auto_resume_max (nil = default 1).
func newAutoResumeService(t *testing.T, root, stderrText string, autoMax *int) *Service {
	t.Helper()
	bin := testcmd.Path(t)
	cfg := &config.Config{
		Server:  config.ServerConfig{AutoResumeMax: autoMax},
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
				Type:    agent.TypeCLIAgent,
				Command: bin,
				Args:    []string{"stderr-exit", "1", stderrText, "{{prompt}}"},
				// The continuation runs the SAME binary in a mode that exits 0.
				SessionResume: []string{"printf", "resumed {{session_id}}: {{prompt}}"},
			},
		},
	}
	return newServiceFromCfg(t, root, cfg)
}

// waitAutoResumed polls until the source job records the job it was auto-resumed
// into (the trigger runs on the finish path, after the terminal status lands).
func waitAutoResumed(t *testing.T, s *Service, id string, want bool) JobResult {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, ok := s.Get(id)
		if !ok {
			t.Fatalf("job %s vanished", id)
		}
		if (got.AutoResumedBy != "") == want || time.Now().After(deadline) {
			if (got.AutoResumedBy != "") != want {
				t.Fatalf("job %s auto_resumed_by = %q, want set=%v", id, got.AutoResumedBy, want)
			}
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAutoResumeTriggersOnCapacityError(t *testing.T) {
	root := t.TempDir()
	s := newAutoResumeService(t, root, "ERROR: Selected model is at capacity. Please try a different model.", nil)

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30, SessionID: "sess-cap", PlanID: "plan-cap",
		Tags: []string{"nightly"}, Title: "big task",
	})
	if src.Status != StatusFailed {
		t.Fatalf("setup: source status = %s, want failed", src.Status)
	}
	src = waitAutoResumed(t, s, src.ID, true)

	cont, ok := s.Get(src.AutoResumedBy)
	if !ok {
		t.Fatalf("continuation %s not found", src.AutoResumedBy)
	}
	if cont.ResumedFrom != src.ID || cont.AutoResumeAttempt != 1 || cont.SourceJobID != src.ID {
		t.Fatalf("continuation lineage = resumed_from %q attempt %d source %q, want %s/1/%s", cont.ResumedFrom, cont.AutoResumeAttempt, cont.SourceJobID, src.ID, src.ID)
	}
	if cont.SessionID != "sess-cap" || cont.PlanID != "plan-cap" || strings.Join(cont.Tags, ",") != "nightly" || cont.TimeoutSec != 30 {
		t.Fatalf("continuation must inherit session/plan/tags/timeout: %+v", cont)
	}
	if !strings.Contains(cont.Title, "big task") || !strings.Contains(cont.Title, "resumed") {
		t.Fatalf("continuation title = %q, want the source title marked resumed", cont.Title)
	}
	final, _ := s.Wait(cont.ID)
	if final.Status != StatusDone {
		t.Fatalf("continuation status = %s, want done (resume template exits 0)", final.Status)
	}
	// The continuation prompt names the transient error and the resume argv carries it.
	if !strings.Contains(final.RenderedCommand, "at capacity") || !strings.Contains(final.RenderedCommand, "sess-cap") {
		t.Fatalf("continuation argv = %q, want the transient reason and the session id", final.RenderedCommand)
	}
	// Source: no terminal notification event, but an auto_resumed marker.
	types := eventTypes(t, s, src.ID)
	if hasSubsequence(types, []string{EventJobTerminal}) {
		t.Fatalf("source job must not emit %s when auto-resumed: %v", EventJobTerminal, types)
	}
	if !hasSubsequence(types, []string{"job.auto_resumed"}) {
		t.Fatalf("source job lacks job.auto_resumed: %v", types)
	}
	// The continuation's own terminal state is notified normally.
	if !hasSubsequence(eventTypes(t, s, cont.ID), []string{EventJobTerminal}) {
		t.Fatalf("continuation must emit %s", EventJobTerminal)
	}
}

func TestAutoResumeSkipsWithoutSession(t *testing.T) {
	root := t.TempDir()
	s := newAutoResumeService(t, root, "Selected model is at capacity", nil)
	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "x", Cwd: ".", TimeoutSec: 30, // no SessionID: nothing to resume into
	})
	if src.Status != StatusFailed {
		t.Fatalf("setup: status = %s", src.Status)
	}
	waitAutoResumed(t, s, src.ID, false)
	if !hasSubsequence(eventTypes(t, s, src.ID), []string{EventJobTerminal}) {
		t.Fatal("a failure that is not auto-resumed must still emit job.terminal")
	}
}

func TestAutoResumeSkipsNonTransientFailure(t *testing.T) {
	root := t.TempDir()
	s := newAutoResumeService(t, root, "panic: nil pointer dereference in the agent", nil)
	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "x", Cwd: ".", TimeoutSec: 30, SessionID: "sess-real-bug",
	})
	if src.Status != StatusFailed {
		t.Fatalf("setup: status = %s", src.Status)
	}
	waitAutoResumed(t, s, src.ID, false)
	if !hasSubsequence(eventTypes(t, s, src.ID), []string{EventJobTerminal}) {
		t.Fatal("a non-transient failure must emit job.terminal")
	}
}

func TestAutoResumeRespectsMax(t *testing.T) {
	root := t.TempDir()
	max := 1
	s := newAutoResumeService(t, root, "429 too many requests", &max)
	// Make the continuation fail the same way: point the resume template at the
	// failing mode too, so the chain would recurse without the cap.
	cfg := s.Config()
	ac := cfg.Agents["codex"]
	ac.SessionResume = []string{"stderr-exit", "1", "429 too many requests", "{{prompt}}"}
	cfg.Agents["codex"] = ac
	s = newServiceFromCfg(t, root, cfg)

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "x", Cwd: ".", TimeoutSec: 30, SessionID: "sess-max",
	})
	src = waitAutoResumed(t, s, src.ID, true)
	cont, _ := s.Wait(src.AutoResumedBy)
	if cont.Status != StatusFailed || cont.AutoResumeAttempt != 1 {
		t.Fatalf("continuation = %s attempt %d, want failed/1", cont.Status, cont.AutoResumeAttempt)
	}
	// Attempt 1 has used the budget (max=1): the failed continuation is NOT resumed again.
	waitAutoResumed(t, s, cont.ID, false)
	if !hasSubsequence(eventTypes(t, s, cont.ID), []string{EventJobTerminal}) {
		t.Fatal("the last link of the chain must emit job.terminal")
	}
}

func TestAutoResumeDisabledByZero(t *testing.T) {
	root := t.TempDir()
	zero := 0
	s := newAutoResumeService(t, root, "Selected model is at capacity", &zero)
	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "x", Cwd: ".", TimeoutSec: 30, SessionID: "sess-off",
	})
	waitAutoResumed(t, s, src.ID, false)
	if !hasSubsequence(eventTypes(t, s, src.ID), []string{EventJobTerminal}) {
		t.Fatal("with auto_resume_max=0 the failure must emit job.terminal")
	}
}

func TestAutoResumeInheritsPlanAndWorktree(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)
	s := newAutoResumeService(t, root, "service temporarily unavailable", nil)

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "x", Cwd: ".", TimeoutSec: 30, SessionID: "sess-wt", PlanID: "plan-wt", Worktree: true,
	})
	if src.Status != StatusFailed || src.WorktreePath == "" {
		t.Fatalf("setup: status=%s worktree=%q", src.Status, src.WorktreePath)
	}
	src = waitAutoResumed(t, s, src.ID, true)
	cont, _ := s.Wait(src.AutoResumedBy)
	if cont.PlanID != "plan-wt" || cont.ResumedFrom != src.ID {
		t.Fatalf("continuation plan/lineage = %q/%q", cont.PlanID, cont.ResumedFrom)
	}
	// The continuation runs INSIDE the source's worktree, not in the main checkout.
	if !strings.HasPrefix(cont.Cwd, src.WorktreePath) {
		t.Fatalf("continuation cwd = %q, want under the source worktree %q", cont.Cwd, src.WorktreePath)
	}
}

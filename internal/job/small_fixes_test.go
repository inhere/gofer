package job

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestFallbackSkippedEventAtSubmit (gofer-ehvd): a failover candidate the project does not
// admit is dropped at submit AND written on the job, so `job show` can say why the job
// never switched agent.
func TestFallbackSkippedEventAtSubmit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg := fallbackCfg(t, root, "at capacity")
	ac := cfg.Agents["codex"]
	ac.FallbackAgents = []string{"claude", "omp"} // claude is not admitted below
	cfg.Agents["codex"] = ac
	proj := cfg.Projects["self"]
	proj.AllowedAgents = []string{"codex", "omp", "exec"}
	cfg.Projects["self"] = proj
	s := newServiceFromCfg(t, root, cfg)

	src := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "codex", Runner: "local", Prompt: "x", Cwd: ".", TimeoutSec: 30})
	src = waitFellBack(t, s, src.ID)

	evs, err := s.ListJobEvents(src.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var skipped []string
	for _, e := range evs {
		if e.Type != EventJobFallbackSkipped {
			continue
		}
		var d struct{ Agent, Reason string }
		if err := json.Unmarshal([]byte(e.Detail), &d); err != nil {
			t.Fatalf("bad detail %q: %v", e.Detail, err)
		}
		if d.Reason == "" {
			t.Fatalf("job.fallback_skipped carries no reason: %s", e.Detail)
		}
		skipped = append(skipped, d.Agent)
	}
	if strings.Join(skipped, ",") != "claude" {
		t.Fatalf("job.fallback_skipped agents = %v, want exactly [claude]", skipped)
	}
	// The takeover job inherits the plan and must not repeat the root's record.
	next, _ := s.Get(src.FellBackTo)
	for _, ty := range eventTypes(t, s, next.ID) {
		if ty == EventJobFallbackSkipped {
			t.Fatalf("the takeover job %s repeated job.fallback_skipped", next.ID)
		}
	}
}

// TestSubmitMissingRunnerBeatsCeiling (gofer-r20b): a missing runner is a 400-class
// invalid request even for a caller whose ceiling would also refuse the empty name.
func TestSubmitMissingRunnerBeatsCeiling(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg := withCeilingOnCfg(fallbackCfg(t, root, "x"), "ci-bot", func(c *config.CallerCeiling) {
		c.Projects, c.Runners, c.Agents = []string{"self"}, []string{"local"}, []string{"codex"}
	})
	s := newServiceFromCfg(t, root, cfg)
	_, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "codex", Prompt: "x", Cwd: ".", TimeoutSec: 30,
		CallerID: "ci-bot", OriginCaller: "ci-bot"})
	if !errors.Is(err, ErrInvalidRequest) || errors.Is(err, ErrCeilingDenied) || !strings.Contains(err.Error(), "runner is required") {
		t.Fatalf("Submit without runner = %v, want ErrInvalidRequest \"runner is required\" (not a ceiling denial)", err)
	}
	// With a runner outside the ceiling the ceiling still speaks.
	_, err = s.Submit(JobRequest{ProjectKey: "self", Agent: "codex", Runner: "w-x", Prompt: "x", Cwd: ".", TimeoutSec: 30,
		CallerID: "ci-bot", OriginCaller: "ci-bot"})
	if !errors.Is(err, ErrCeilingDenied) {
		t.Fatalf("Submit with a foreign runner = %v, want ErrCeilingDenied", err)
	}
}

// TestRebuildContinuationRefusesGoneWorktree (gofer-5x85): re-running a continuation carries
// the same worktree again; once the worktree is gone the rerun is refused with the
// continuation guard's error (HTTP 409) instead of running in the leftover directory.
func TestRebuildContinuationRefusesGoneWorktree(t *testing.T) {
	repo, _ := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())
	first := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "status"}, Cwd: ".", TimeoutSec: 60, Worktree: true,
	})
	if first.WorktreePath == "" || first.WorktreeBranch == "" {
		t.Fatalf("first job has no worktree: %+v", first)
	}
	// A finished continuation of `first`: it ran in first's worktree (a relative cwd inside
	// it), and its row names the worktree like the real thing does.
	var req JobRequest
	if err := json.Unmarshal([]byte(first.RequestJSON), &req); err != nil {
		t.Fatal(err)
	}
	req.Worktree, req.WorktreeBase = false, ""
	req.Cwd = s.resumeCwd(first)
	raw, _ := json.Marshal(req)
	cont := first
	cont.ID, cont.RequestJSON, cont.ResumedFrom, cont.SourceJobID = "job-cont-5x85", string(raw), first.ID, first.ID
	cont.Status = StatusDone
	if err := s.persist(cont); err != nil {
		t.Fatal(err)
	}

	// Worktree present: the rerun is a continuation in it again.
	again, err := s.RebuildJob(cont.ID, RebuildOverrides{}, "caller", "127.0.0.1")
	if err != nil {
		t.Fatalf("rerun with the worktree present: %v", err)
	}
	if again.WorktreePath != first.WorktreePath {
		t.Fatalf("rerun worktree = %q, want the continuation's %q", again.WorktreePath, first.WorktreePath)
	}
	s.Wait(again.ID)

	// Worktree gone: refused with the guard's error.
	if _, err := s.RemoveWorktree(first.ID, true, true); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	if _, err := s.RebuildJob(cont.ID, RebuildOverrides{}, "caller", "127.0.0.1"); !errors.Is(err, ErrContinuationWorktreeGone) {
		t.Fatalf("rerun with the worktree gone = %v, want ErrContinuationWorktreeGone", err)
	}
}

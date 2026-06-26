package job

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// resumeArgv decodes the resumed job's persisted RequestJSON and returns its Cmd
// (the exec argv ResumeJob built). Reading the stored request is hermetic — it
// captures the rendered argv at SUBMIT time, so the assertion needs no real
// claude/codex binary and no wait for the resumed job to run.
func resumeArgv(t *testing.T, requestJSON string) []string {
	t.Helper()
	var r struct {
		Cmd []string `json:"cmd"`
	}
	if err := json.Unmarshal([]byte(requestJSON), &r); err != nil {
		t.Fatalf("resumed RequestJSON not valid JSON: %v (%q)", err, requestJSON)
	}
	return r.Cmd
}

// equalArgs reports whether two argv slices are element-wise equal.
func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestResumeJobUnknownJob: resuming a non-existent id is ErrUnknownJob (→404).
func TestResumeJobUnknownJob(t *testing.T) {
	root := t.TempDir()
	s := newClaudeInjectService(t, root)
	_, err := s.ResumeJob("no-such-job", "hi", "", "caller-1")
	if !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("ResumeJob unknown id err = %v, want ErrUnknownJob", err)
	}
}

// TestResumeJobNoSession: a job that captured no session_id cannot resume
// (ErrNoSession →400). An exec job never injects/captures, so its SessionID="".
func TestResumeJobNoSession(t *testing.T) {
	root := t.TempDir()
	s := newClaudeInjectService(t, root)
	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
	})
	if src.SessionID != "" {
		t.Fatalf("setup: exec job should have empty session_id, got %q", src.SessionID)
	}
	_, err := s.ResumeJob(src.ID, "again", "", "caller-1")
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("ResumeJob no-session err = %v, want ErrNoSession", err)
	}
}

// TestResumeJobResumeUnsupported: a job whose agent has no SessionResume template
// is rejected with ErrResumeUnsupported (→400). We build an agent with an
// explicit session_inject but an empty session_resume so the job HAS a captured
// session_id yet cannot resume.
func TestResumeJobResumeUnsupported(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"noresume", "exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			// inject yields a session_id at submit, but no resume template → cannot续接.
			"noresume": {
				Type:          agent.TypeCLIAgent,
				Command:       "echo",
				Args:          []string{"{{prompt}}"},
				SessionInject: []string{"--sid", "{{session_id}}"},
			},
		},
	}
	s := newServiceFromCfg(t, root, cfg)

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "noresume", Runner: "local",
		Prompt: "hi", Cwd: ".", TimeoutSec: 30,
	})
	if src.SessionID == "" {
		t.Fatalf("setup: inject agent should have a session_id")
	}
	_, err := s.ResumeJob(src.ID, "again", "", "caller-1")
	if !errors.Is(err, ErrResumeUnsupported) {
		t.Fatalf("ResumeJob no-resume-template err = %v, want ErrResumeUnsupported", err)
	}
}

// TestResumeJobCrossRunner: an explicit runner differing from the source job's is
// ErrCrossRunner (→400, 同 runner 约束).
func TestResumeJobCrossRunner(t *testing.T) {
	root := t.TempDir()
	s := newClaudeInjectService(t, root)
	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local",
		Prompt: "hi", Cwd: ".", TimeoutSec: 30,
	})
	if src.SessionID == "" {
		t.Fatalf("setup: claude inject should have a session_id")
	}
	_, err := s.ResumeJob(src.ID, "again", "some-other-runner", "caller-1")
	if !errors.Is(err, ErrCrossRunner) {
		t.Fatalf("ResumeJob cross-runner err = %v, want ErrCrossRunner", err)
	}
	// An explicit runner EQUAL to the source runner is allowed (no error).
	ok, err := s.ResumeJob(src.ID, "again", "local", "caller-1")
	if err != nil {
		t.Fatalf("ResumeJob same-runner should succeed, got %v", err)
	}
	s.Wait(ok.ID) // let the resumed job settle before teardown closes the jobstore.
}

// submitSourceCancel submits a source job, reads its IMMEDIATE snapshot (the
// injected/explicit session_id is set at submit, before the command runs) and
// cancels it so a real claude/codex CLI never runs to completion in the test.
func submitSourceCancel(t *testing.T, s *Service, req JobRequest) JobResult {
	t.Helper()
	res, err := s.Submit(req)
	if err != nil {
		t.Fatalf("Submit source: %v", err)
	}
	// Cancel AND wait for terminal so the job goroutine fully settles (its final
	// persist completes) before the jobstore is closed in teardown — otherwise a
	// late best-effort persist races the close and logs a benign "database is
	// closed" warning.
	t.Cleanup(func() {
		_ = s.Cancel(res.ID)
		s.Wait(res.ID)
	})
	return res
}

// TestResumeJobRendersClaudeArgv: a claude source job resumes into the exec argv
// [claude --resume <sid> -p <prompt>], on the same runner, links the same
// session_id, and carries the authenticated caller id. The resume argv is read
// from the resumed job's persisted request (captured at submit), so no real
// claude binary runs — the assertion is hermetic.
func TestResumeJobRendersClaudeArgv(t *testing.T) {
	root := t.TempDir()
	s := newResumeRunnableService(t, root, "claude")

	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local",
		Prompt: "remember 42", Cwd: "sub", TimeoutSec: 30,
	})
	sid := src.SessionID
	if sid == "" {
		t.Fatalf("setup: claude inject should have a session_id")
	}

	newJob, err := s.ResumeJob(src.ID, "what number", "", "caller-7")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(newJob.ID); s.Wait(newJob.ID) })

	if newJob.Agent != agent.ExecAgentKey {
		t.Fatalf("resumed job agent = %q, want exec", newJob.Agent)
	}
	if newJob.Runner != "local" {
		t.Fatalf("resumed job runner = %q, want local (same runner)", newJob.Runner)
	}
	if newJob.SessionID != sid {
		t.Fatalf("resumed job session_id = %q, want %q (linked)", newJob.SessionID, sid)
	}
	if newJob.CallerID != "caller-7" {
		t.Fatalf("resumed job caller_id = %q, want caller-7", newJob.CallerID)
	}

	got := resumeArgv(t, newJob.RequestJSON)
	want := []string{"claude", "--resume", sid, "-p", "what number"}
	if !equalArgs(got, want) {
		t.Fatalf("resumed claude argv = %#v, want %#v", got, want)
	}

	// The resumed job inherits the source's RELATIVE cwd ("sub"), recovered from
	// the source request_json — not the resolved absolute path (which would
	// mis-SafeJoin on re-submit).
	if cwd := cwdFromRequestJSON(newJob.RequestJSON); cwd != "sub" {
		t.Fatalf("resumed cwd = %q, want \"sub\" (source relative cwd)", cwd)
	}
}

// TestResumeJobRendersCodexArgv: a codex source job resumes into the exec argv
// [codex exec resume <sid> <prompt>] with the same linked session. The source
// session_id is supplied explicitly (resume path contract) so argv[0] stays the
// real codex Command.
func TestResumeJobRendersCodexArgv(t *testing.T) {
	root := t.TempDir()
	const sid = "abcd1234-aaaa-bbbb-cccc-001122334455"
	s := newResumeRunnableService(t, root, "codex")

	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "remember 42", Cwd: ".", TimeoutSec: 30, SessionID: sid,
	})
	if src.SessionID != sid {
		t.Fatalf("setup: explicit source session_id = %q, want %q", src.SessionID, sid)
	}

	newJob, err := s.ResumeJob(src.ID, "what number", "", "caller-9")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(newJob.ID); s.Wait(newJob.ID) })

	if newJob.SessionID != sid {
		t.Fatalf("resumed job session_id = %q, want %q", newJob.SessionID, sid)
	}

	got := resumeArgv(t, newJob.RequestJSON)
	want := []string{"codex", "exec", "resume", sid, "what number"}
	if !equalArgs(got, want) {
		t.Fatalf("resumed codex argv = %#v, want %#v", got, want)
	}
}

// newServiceFromCfg wires a Service over an explicit config (local runner + a
// fresh jobstore). Helper for resume tests that need a bespoke agent set.
func newServiceFromCfg(t *testing.T, root string, cfg *config.Config) *Service {
	t.Helper()
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	meta, err := jobstore.Open(jobstoreDBPath(root))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	return NewService(cfg, projReg, agentReg, runners, meta, nil)
}

// jobstoreDBPath returns the test jobstore path under root.
func jobstoreDBPath(root string) string { return root + "/gofer.db" }

// newResumeRunnableService builds a Service where the named agent's Command is a
// harmless `echo`-equivalent (so the resumed EXEC job — whose argv[0] is the
// agent Command — actually runs) while keeping the built-in claude session
// defaults (inject + resume template). The resumed argv is [<Command> --resume
// <sid> -p <prompt>]; we set Command="echo" so it runs to done and the rendered
// command round-trips for assertion.
func newResumeRunnableService(t *testing.T, root, agentKey string) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{agentKey, "exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true, // resume submits an exec job
			},
		},
		Agents: map[string]config.AgentConfig{
			// Command "claude" stays literal so the resumed argv asserts the real shape;
			// it never executes a real claude (the resumed exec job's argv[0] is "claude"
			// and would fail to start, but the RenderedCommand is captured before exec).
			agentKey: {Type: agent.TypeCLIAgent, Command: agentKey, Args: []string{"-p", "{{prompt}}"}},
		},
	}
	return newServiceFromCfg(t, root, cfg)
}

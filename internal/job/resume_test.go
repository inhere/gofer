package job

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/testutil/testcmd"
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

func resumeStoredRequest(t *testing.T, requestJSON string) struct {
	Cmd         []string `json:"cmd"`
	Interactive bool     `json:"interactive,omitempty"`
} {
	t.Helper()
	var r struct {
		Cmd         []string `json:"cmd"`
		Interactive bool     `json:"interactive,omitempty"`
	}
	if err := json.Unmarshal([]byte(requestJSON), &r); err != nil {
		t.Fatalf("resumed RequestJSON not valid JSON: %v (%q)", err, requestJSON)
	}
	return r
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

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
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

// submitSourceCancel submits a source job, cancels and waits it to terminal, then
// returns the terminal snapshot for resume tests (P6 requires a terminal source).
func submitSourceCancel(t *testing.T, s *Service, req JobRequest) JobResult {
	t.Helper()
	res, err := s.Submit(req)
	if err != nil {
		t.Fatalf("Submit source: %v", err)
	}
	_ = s.Cancel(res.ID)
	final, ok := s.Wait(res.ID)
	if !ok {
		t.Fatalf("Wait source: job %s not found", res.ID)
	}
	if !IsTerminal(final.Status) {
		t.Fatalf("source status=%s, want terminal", final.Status)
	}
	return final
}

func TestResumeJobRejectsRunningSourceBeforeNoSession(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "sleep", "2s"), Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForStatus(t, s, res.ID, StatusRunning, 2*time.Second)
	t.Cleanup(func() {
		_ = s.Cancel(res.ID)
		s.Wait(res.ID)
	})

	_, err = s.ResumeJob(res.ID, "again", "", "caller-1")
	if !errors.Is(err, ErrJobNotTerminal) {
		t.Fatalf("ResumeJob running source err = %v, want ErrJobNotTerminal", err)
	}
	if errors.Is(err, ErrNoSession) {
		t.Fatalf("ResumeJob running source err = %v, must not be ErrNoSession", err)
	}
}

func TestResumeJobInheritsPlanID(t *testing.T) {
	root := t.TempDir()
	const sid = "sess-plan-x"
	s := newResumeRunnableService(t, root, "codex")

	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "remember 42", Cwd: ".", TimeoutSec: 30, SessionID: sid, PlanID: "plan-x",
	})
	if src.SessionID != sid || src.PlanID != "plan-x" {
		t.Fatalf("setup source session/plan = %q/%q, want %q/plan-x", src.SessionID, src.PlanID, sid)
	}

	newJob, err := s.ResumeJob(src.ID, "what number", "", "caller-plan")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(newJob.ID); s.Wait(newJob.ID) })

	if newJob.PlanID != "plan-x" {
		t.Fatalf("resumed job plan_id = %q, want plan-x", newJob.PlanID)
	}
	got, ok := s.Get(newJob.ID)
	if !ok {
		t.Fatalf("resumed job %s not found", newJob.ID)
	}
	if got.PlanID != "plan-x" {
		t.Fatalf("stored resumed job plan_id = %q, want plan-x", got.PlanID)
	}
}

func TestResumeJobStampsSourceJobID(t *testing.T) {
	root := t.TempDir()
	const sid = "sess-source-x"
	s := newResumeRunnableService(t, root, "codex")

	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "remember 42", Cwd: ".", TimeoutSec: 30, SessionID: sid,
	})
	if src.SessionID != sid {
		t.Fatalf("setup source session = %q, want %q", src.SessionID, sid)
	}

	newJob, err := s.ResumeJob(src.ID, "what number", "", "caller-source")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(newJob.ID); s.Wait(newJob.ID) })

	if newJob.SourceJobID != src.ID {
		t.Fatalf("resumed job source_job_id = %q, want %q", newJob.SourceJobID, src.ID)
	}
	if newJob.SessionID != sid {
		t.Fatalf("resumed job session_id = %q, want %q", newJob.SessionID, sid)
	}
	got, ok := s.Get(newJob.ID)
	if !ok {
		t.Fatalf("resumed job %s not found", newJob.ID)
	}
	if got.SourceJobID != src.ID || got.SessionID != sid {
		t.Fatalf("stored resumed job source/session = %q/%q, want %q/%q", got.SourceJobID, got.SessionID, src.ID, sid)
	}
}

func TestResumeJobEmptyPlanIDStaysEmpty(t *testing.T) {
	root := t.TempDir()
	const sid = "sess-no-plan"
	s := newResumeRunnableService(t, root, "codex")

	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "remember 42", Cwd: ".", TimeoutSec: 30, SessionID: sid,
	})
	if src.SessionID != sid || src.PlanID != "" {
		t.Fatalf("setup source session/plan = %q/%q, want %q/empty", src.SessionID, src.PlanID, sid)
	}

	newJob, err := s.ResumeJob(src.ID, "what number", "", "caller-plan")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(newJob.ID); s.Wait(newJob.ID) })

	if newJob.PlanID != "" {
		t.Fatalf("resumed job plan_id = %q, want empty", newJob.PlanID)
	}
	got, ok := s.Get(newJob.ID)
	if !ok {
		t.Fatalf("resumed job %s not found", newJob.ID)
	}
	if got.PlanID != "" {
		t.Fatalf("stored resumed job plan_id = %q, want empty", got.PlanID)
	}
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
	if newJob.Interactive {
		t.Fatalf("resumed non-interactive source should stay non-interactive")
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

func TestResumeJobInteractiveSourceUsesInteractiveTemplate(t *testing.T) {
	root := t.TempDir()
	s := newInteractiveResumeService(t, root, "claude")

	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local",
		Interactive: true,
		Prompt:      "start", Cwd: ".", TimeoutSec: 30,
	})
	if !src.Interactive {
		t.Fatalf("setup source interactive = false, want true")
	}
	sid := src.SessionID
	if sid == "" {
		t.Fatalf("setup: claude inject should have a session_id")
	}

	newJob, err := s.ResumeJob(src.ID, "", "", "caller-pty")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(newJob.ID); s.Wait(newJob.ID) })

	if !newJob.Interactive {
		t.Fatalf("resumed interactive source job Interactive=false, want true")
	}
	gotReq := resumeStoredRequest(t, newJob.RequestJSON)
	if !gotReq.Interactive {
		t.Fatalf("resumed request interactive=false, want true")
	}
	want := []string{"claude", "--resume", sid}
	if !equalArgs(gotReq.Cmd, want) {
		t.Fatalf("interactive resumed argv = %#v, want %#v", gotReq.Cmd, want)
	}
	if containsArg(gotReq.Cmd, "-p") {
		t.Fatalf("interactive resumed argv must not contain -p: %#v", gotReq.Cmd)
	}
}

func TestResumeJobNonInteractiveSourceUsesSessionResumeTemplate(t *testing.T) {
	root := t.TempDir()
	s := newResumeRunnableService(t, root, "claude")

	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local",
		Prompt: "remember 42", Cwd: ".", TimeoutSec: 30,
	})
	if src.Interactive {
		t.Fatalf("setup source interactive = true, want false")
	}
	sid := src.SessionID
	if sid == "" {
		t.Fatalf("setup: claude inject should have a session_id")
	}

	newJob, err := s.ResumeJob(src.ID, "what number", "", "caller-non-pty")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(newJob.ID); s.Wait(newJob.ID) })

	if newJob.Interactive {
		t.Fatalf("resumed non-interactive source job Interactive=true, want false")
	}
	got := resumeArgv(t, newJob.RequestJSON)
	want := []string{"claude", "--resume", sid, "-p", "what number"}
	if !equalArgs(got, want) {
		t.Fatalf("non-interactive resumed argv = %#v, want %#v", got, want)
	}
}

func TestResumeJobNonInteractiveSourceRejectsEmptyPrompt(t *testing.T) {
	root := t.TempDir()
	s := newResumeRunnableService(t, root, "claude")

	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local",
		Prompt: "remember 42", Cwd: ".", TimeoutSec: 30,
	})
	if src.Interactive {
		t.Fatalf("setup source interactive = true, want false")
	}

	_, err := s.ResumeJob(src.ID, " \t", "", "caller-empty")
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("ResumeJob empty non-interactive prompt err = %v, want ErrInvalidRequest", err)
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

// TestResumeJobExemptAllowExec proves the 2026-06-26 exemption: a cli-agent
// (claude) source job resumes even when the project has allow_exec=false AND does
// NOT list "exec" in allowed_agents. The resume carrier is exec, but validate
// gates on the SOURCE agent (claude) — which is allowed and is not exec-type — so
// the broad allow_exec is not required. Without the exemption this would fail with
// "allow_exec=false".
func TestResumeJobExemptAllowExec(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"claude"}, // NOTE: no "exec"
				AllowedRunners: []string{"local"},
				AllowExec:      false, // NOTE: exec gate closed
			},
		},
		Agents: map[string]config.AgentConfig{
			// Built-in claude session defaults (inject + resume) but a harmless echo
			// Command so the resumed exec job runs to done in the test.
			"claude": {Type: agent.TypeCLIAgent, Command: "echo", Args: []string{"-p", "{{prompt}}"}},
		},
	}
	s := newServiceFromCfg(t, root, cfg)

	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local",
		Prompt: "remember 42", Cwd: ".", TimeoutSec: 30,
	})
	if src.SessionID == "" {
		t.Fatalf("setup: claude inject should have a session_id")
	}

	newJob, err := s.ResumeJob(src.ID, "what number", "", "caller-x")
	if err != nil {
		t.Fatalf("ResumeJob should be exempt from allow_exec, got %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(newJob.ID); s.Wait(newJob.ID) })

	if newJob.Agent != agent.ExecAgentKey {
		t.Fatalf("resumed job agent = %q, want exec (carrier)", newJob.Agent)
	}
	if newJob.SessionID != src.SessionID {
		t.Fatalf("resumed job session_id = %q, want %q (linked)", newJob.SessionID, src.SessionID)
	}
}

// TestResumeJobRerunStillGated proves the exemption does NOT leak to a plain
// re-submit: replaying a resume job's stored exec request through the public
// Submit path (as `job rerun` does — no ResumeSourceAgent) is still gated as a
// plain exec job. This is the anti-forge boundary: only the resume entrypoint
// carries the source-agent authorization.
func TestResumeJobRerunStillGated(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"claude"},
				AllowedRunners: []string{"local"},
				AllowExec:      false,
			},
		},
		Agents: map[string]config.AgentConfig{
			"claude": {Type: agent.TypeCLIAgent, Command: "echo", Args: []string{"-p", "{{prompt}}"}},
		},
	}
	s := newServiceFromCfg(t, root, cfg)

	// A direct exec submit (no resume marker) is rejected — the gate still applies.
	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("plain exec submit should stay gated (allow_exec=false), got %v", err)
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

func newInteractiveResumeService(t *testing.T, root, agentKey string) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:                 root,
				AllowedAgents:            []string{agentKey, "exec"},
				AllowedRunners:           []string{"local"},
				InteractiveAllowedAgents: []string{agentKey},
				AllowExec:                true,
			},
		},
		Agents: map[string]config.AgentConfig{
			agentKey: {
				Type:        agent.TypeCLIAgent,
				Command:     agentKey,
				Args:        []string{"{{prompt}}"},
				Interactive: true,
				NoRawCmd:    true,
			},
		},
	}
	s := newServiceFromCfg(t, root, cfg)
	s.runners[builtinPtyRunner] = &recordingRunner{name: builtinPtyRunner}
	return s
}

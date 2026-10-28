package job

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
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
	t.Parallel()
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
	t.Parallel()
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

// TestResumeJobResumeUnsupported: a job that HAS a session id but whose agent has no
// resume template is rejected with ErrResumeUnsupported (→400). AGT-04 now fills the
// generic `--resume` template into every cli-agent, so the only carrier left without
// one is exec — its argv is the caller's, so gofer has nothing to render.
func TestResumeJobResumeUnsupported(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	s := newServiceFromCfg(t, root, cfg)

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		SessionID: "sess-explicit-1",
	})
	if src.SessionID == "" {
		t.Fatalf("setup: an explicit session_id should be kept")
	}
	_, err := s.ResumeJob(src.ID, "again", "", "caller-1")
	if !errors.Is(err, ErrResumeUnsupported) {
		t.Fatalf("ResumeJob no-resume-template err = %v, want ErrResumeUnsupported", err)
	}
}

// TestResumeJobCrossRunner: an explicit runner differing from the source job's is
// ErrCrossRunner (→400, 同 runner 约束).
func TestResumeJobCrossRunner(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestResumeJobPlanOverride (gofer-z414): the continuation is grouped like the run it
// continues unless the caller names another plan; naming the source's own plan (or
// leaving the option nil) inherits it.
func TestResumeJobPlanOverride(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const sid = "sess-plan-move"
	s := newResumeRunnableService(t, root, "codex")
	seedPlan(t, s, "plan-from")
	seedPlan(t, s, "plan-to")

	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "remember 42", Cwd: ".", TimeoutSec: 30, SessionID: sid, PlanID: "plan-from",
	})

	want := "plan-to"
	newJob, err := s.ResumeJobWith(src.ID, "what number", "", "caller-plan", ResumeOptions{Plan: &want})
	if err != nil {
		t.Fatalf("ResumeJobWith: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(newJob.ID); s.Wait(newJob.ID) })
	if newJob.PlanID != want {
		t.Fatalf("continuation plan_id = %q, want the override %q", newJob.PlanID, want)
	}
	if got, _ := s.Get(newJob.ID); got.PlanID != want {
		t.Fatalf("stored continuation plan_id = %q, want %q", got.PlanID, want)
	}

	// The source's own plan (named explicitly) is the same decision as inheriting it.
	same := "plan-from"
	kept, err := s.ResumeJobWith(src.ID, "what number", "", "caller-plan", ResumeOptions{Plan: &same})
	if err != nil {
		t.Fatalf("ResumeJobWith(same plan): %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(kept.ID); s.Wait(kept.ID) })
	if kept.PlanID != "plan-from" {
		t.Fatalf("continuation plan_id = %q, want plan-from", kept.PlanID)
	}
}

// TestResumeJobNoPlanDetaches (gofer-z414): an explicit "no plan" drops the plan AND
// the checklist link inherited with it — a todo belongs to the plan it was created in,
// and Submit would otherwise resolve the plan straight back from the todo.
func TestResumeJobNoPlanDetaches(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const sid = "sess-plan-drop"
	s := newResumeRunnableService(t, root, "codex")
	seedPlan(t, s, "plan-drop")
	if err := s.Meta().InsertTodo(jobstore.PlanTodo{TodoID: "todo-drop", PlanID: "plan-drop", Title: "drop me", Status: jobstore.TodoReady}); err != nil {
		t.Fatalf("InsertTodo: %v", err)
	}

	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "remember 42", Cwd: ".", TimeoutSec: 30, SessionID: sid,
		PlanID: "plan-drop", TodoID: "todo-drop",
	})
	if src.PlanID != "plan-drop" || src.TodoID != "todo-drop" {
		t.Fatalf("setup: source plan/todo = %q/%q, want plan-drop/todo-drop", src.PlanID, src.TodoID)
	}

	none := ""
	newJob, err := s.ResumeJobWith(src.ID, "what number", "", "caller-plan", ResumeOptions{Plan: &none})
	if err != nil {
		t.Fatalf("ResumeJobWith: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(newJob.ID); s.Wait(newJob.ID) })
	if newJob.PlanID != "" || newJob.TodoID != "" {
		t.Fatalf("continuation plan/todo = %q/%q, want empty/empty", newJob.PlanID, newJob.TodoID)
	}
	if got, _ := s.Get(newJob.ID); got.PlanID != "" || got.TodoID != "" {
		t.Fatalf("stored continuation plan/todo = %q/%q, want empty/empty", got.PlanID, got.TodoID)
	}
}

// TestResumeJobRendersClaudeArgv: a claude source job resumes into the exec argv
// [claude --resume <sid> -p <prompt>], on the same runner, links the same
// session_id, and carries the authenticated caller id. The resume argv is read
// from the resumed job's persisted request (captured at submit), so no real
// claude binary runs — the assertion is hermetic.
func TestResumeJobRendersClaudeArgv(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestResumeCarriesAgentGlobalArgs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{"self": {
			HostPath: root, AllowedAgents: []string{"codex", "exec"},
			AllowedRunners: []string{"local"}, AllowExec: true,
		}},
		Agents: map[string]config.AgentConfig{"codex": {
			Type: agent.TypeCLIAgent, Command: bin,
			Args:          []string{"exec", "{{prompt}}"},
			GlobalArgs:    []string{"-s", "danger-full-access", "-a", "never"},
			SessionResume: []string{"exec", "resume", "{{session_id}}", "{{prompt}}"},
		}},
	}
	s := newServiceFromCfg(t, root, cfg)
	src := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "codex", Runner: "local", Prompt: "source", Cwd: ".", TimeoutSec: 30, SessionID: "sess-global"})
	newJob, err := s.ResumeJob(src.ID, "continue", "", "caller")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	got := resumeArgv(t, newJob.RequestJSON)
	want := []string{bin, "-s", "danger-full-access", "-a", "never", "exec", "resume", "sess-global", "continue"}
	if !equalArgs(got, want) {
		t.Fatalf("resume argv = %#v, want %#v", got, want)
	}
}

func TestResumeReadOnlyKeepsReadOnlyArgs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{"self": {
			HostPath: root, AllowedAgents: []string{"codex", "exec"},
			AllowedRunners: []string{"local"}, AllowExec: true,
		}},
		Agents: map[string]config.AgentConfig{"codex": {
			Type: agent.TypeCLIAgent, Command: bin,
			Args:          []string{"exec", "{{prompt}}"},
			GlobalArgs:    []string{"-s", "danger-full-access", "-a", "never"},
			ReadOnlyArgs:  []string{"-s", "read-only"},
			SessionResume: []string{"exec", "resume", "{{session_id}}", "{{prompt}}"},
		}},
	}
	s := newServiceFromCfg(t, root, cfg)
	src := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "codex", Runner: "local", Prompt: "source", Cwd: ".", TimeoutSec: 30, SessionID: "sess-readonly", ReadOnly: true})
	newJob, err := s.ResumeJob(src.ID, "continue", "", "caller")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	got := resumeArgv(t, newJob.RequestJSON)
	want := []string{bin, "-s", "danger-full-access", "-a", "never", "exec", "resume", "sess-readonly", "continue", "-s", "read-only"}
	if !equalArgs(got, want) {
		t.Fatalf("read-only resume argv = %#v, want %#v", got, want)
	}
}

// TestResumeOfResumeWalksChain pins the three-turn CLI chain used by the
// workbench: a CLI source resumes through an exec carrier, then that carrier is
// itself resumed. The second resume sources an exec carrier and must find the CLI
// agent by walking ResumedFrom.
func TestResumeOfResumeWalksChain(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newResumeRunnableService(t, root, "claude")

	first := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local",
		Prompt: "turn one", Cwd: ".", TimeoutSec: 30,
	})
	sid := first.SessionID
	if sid == "" {
		t.Fatal("setup: first turn has no session_id")
	}
	second, err := s.ResumeJob(first.ID, "turn two", "", "caller-chain")
	if err != nil {
		t.Fatalf("first ResumeJob: %v", err)
	}
	second, ok := s.Wait(second.ID)
	if !ok || !IsTerminal(second.Status) {
		t.Fatalf("second turn did not finish: ok=%v result=%+v", ok, second)
	}
	if second.Agent != agent.ExecAgentKey {
		t.Fatalf("second turn carrier = agent %q, want exec", second.Agent)
	}
	third, err := s.ResumeJob(second.ID, "turn three", "", "caller-chain")
	if err != nil {
		t.Fatalf("resume of resume: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(third.ID); s.Wait(third.ID) })
	if got, want := resumeArgv(t, third.RequestJSON), []string{"claude", "--resume", sid, "-p", "turn three"}; !equalArgs(got, want) {
		t.Fatalf("third turn argv=%#v, want %#v", got, want)
	}
}

// TestResumeCarrierRecordsResumeAgent covers the durable display identity without
// depending on a real codex process: the configured CLI command may fail, but
// Submit and persistence must still record the continuation's source agent.
func TestResumeCarrierRecordsResumeAgent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newResumeRunnableService(t, root, "codex")
	first := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "first", Cwd: ".", TimeoutSec: 30, SessionID: "sess-codex",
	})
	readResumeAgent := func(r JobResult) string {
		t.Helper()
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		value, _ := fields["resume_agent"].(string)
		return value
	}
	if got := readResumeAgent(first); got != "" {
		t.Fatalf("ordinary job resume_agent=%q, want empty", got)
	}
	second, err := s.ResumeJob(first.ID, "second", "", "caller")
	if err != nil {
		t.Fatal(err)
	}
	second, _ = s.Wait(second.ID)
	if second.Agent != "exec" || readResumeAgent(second) != "codex" {
		t.Fatalf("first carrier agent=%q resume_agent=%q", second.Agent, readResumeAgent(second))
	}
	third, err := s.ResumeJob(second.ID, "third", "", "caller")
	if err != nil {
		t.Fatal(err)
	}
	third, _ = s.Wait(third.ID)
	if third.Agent != "exec" || readResumeAgent(third) != "codex" {
		t.Fatalf("second carrier agent=%q resume_agent=%q", third.Agent, readResumeAgent(third))
	}
	for _, id := range []string{second.ID, third.ID} {
		rec, ok, err := s.meta.GetJob(id)
		if err != nil || !ok {
			t.Fatalf("read persisted %s: ok=%v err=%v", id, ok, err)
		}
		if got := readResumeAgent(fromRecord(rec)); got != "codex" {
			t.Fatalf("persisted %s resume_agent=%q, want codex", id, got)
		}
	}

	acpRoot := t.TempDir()
	acpService := newACPService(t, acpRoot, acptest.Options{})
	acpFirst := acpSubmit(t, acpService, 30)
	acpNext, err := acpService.ResumeJob(acpFirst.ID, "second", "", "caller")
	if err != nil {
		t.Fatal(err)
	}
	acpNext = endContinuousACPResume(t, acpService, acpNext.ID)
	if got := readResumeAgent(acpNext); got != "" {
		t.Fatalf("ACP continuation resume_agent=%q, want empty", got)
	}
}

func TestResumeJobNonInteractiveSourceRejectsEmptyPrompt(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	root := t.TempDir()
	const sid = "abcd1234-aaaa-bbbb-cccc-001122334455"
	s := newResumeRunnableService(t, root, "codex")

	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "remember 42", Cwd: ".", TimeoutSec: 30, SessionID: sid, ReadDirs: []string{"."},
	})
	if src.SessionID != sid {
		t.Fatalf("setup: explicit source session_id = %q, want %q", src.SessionID, sid)
	}

	newJob, err := s.ResumeJob(src.ID, "what number", "", "caller-9")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(newJob.ID); s.Wait(newJob.ID) })

	if len(newJob.ReadDirs) != 1 || newJob.ReadDirs[0] == "" {
		t.Fatalf("resumed read_dirs = %v, want inherited resolved directory", newJob.ReadDirs)
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
	t.Parallel()
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
	t.Parallel()
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
	return drainOnClose(t, NewService(cfg, projReg, agentReg, runners, meta, nil))
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
			agentKey: {Type: agent.TypeCLIAgent, Command: agentKey, Args: []string{"-p", "{{prompt}}"}, AddDirArgs: []string{"--add-dir", "{{dir}}:ro"}},
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
				HostPath:         root,
				AllowedAgents:    []string{agentKey, "exec"},
				AllowedRunners:   []string{"local"},
				AllowInteractive: boolPtr(true),
				AllowExec:        true,
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

// TestResumeJobInheritsTimeoutTagsAndTitle: the exec carrier must not fall back
// to the 5-minute exec default — a continuation is governed like the run it
// continues (timeout, tags), and its title marks it as resumed.
func TestResumeJobInheritsTimeoutTagsAndTitle(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const sid = "sess-inherit"
	s := newResumeRunnableService(t, root, "codex")

	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "long task", Cwd: ".", TimeoutSec: 2400, SessionID: sid,
		Tags: []string{"nightly", "wave2"}, Title: "big refactor",
	})
	if src.TimeoutSec != 2400 {
		t.Fatalf("setup: source timeout_sec = %d, want 2400", src.TimeoutSec)
	}

	newJob, err := s.ResumeJob(src.ID, "continue", "", "caller-inherit")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(newJob.ID); s.Wait(newJob.ID) })

	if newJob.TimeoutSec != 2400 {
		t.Fatalf("resumed timeout_sec = %d, want the source's 2400 (not the exec default)", newJob.TimeoutSec)
	}
	if got := strings.Join(newJob.Tags, ","); got != "nightly,wave2" {
		t.Fatalf("resumed tags = %q, want nightly,wave2", got)
	}
	if newJob.Title != "big refactor (resumed)" {
		t.Fatalf("resumed title = %q, want %q", newJob.Title, "big refactor (resumed)")
	}
	if resumedTitle(newJob.Title) != newJob.Title {
		t.Fatalf("a second resume must not stack the suffix: %q", resumedTitle(newJob.Title))
	}
}

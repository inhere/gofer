package job

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// verifyStderr reads one job's stderr log (the stream the verify step's banners and
// output are appended to, on the machine that ran it). ReadLogTail only offers a
// tail offset, so 0 reads the whole file.
func verifyStderr(t *testing.T, root, projectKey, jobID string) string {
	t.Helper()
	out, err := store.NewFileStore(filepath.Join(root, projectKey)).ReadLogTail(jobID, store.StreamStderr, 0)
	if err != nil {
		t.Fatalf("read stderr log of %s: %v", jobID, err)
	}
	return string(out)
}

// verifyBanner is the opening banner the verify step writes before the command.
func verifyBanner(argv []string) string {
	return "===== gofer verify: " + strings.Join(argv, " ") + " ====="
}

// newVerifyTransientService builds a service whose "codex" agent finishes with
// `code` after printing stderrText, with a resume template that would succeed — so
// the automatic continuation is LIVE for a failure of this agent (the same bait
// auto_resume_test.go uses). code==0 makes the agent finish NORMALLY, which is the
// only way a verify failure can be mistaken for a transient agent failure.
func newVerifyTransientService(t *testing.T, root, stderrText string, code int) *Service {
	t.Helper()
	bin := testcmd.Path(t)
	autoMax := 3
	cfg := &config.Config{
		Server:  config.ServerConfig{AutoResumeMax: &autoMax},
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
				Type:                   agent.TypeCLIAgent,
				Command:                bin,
				Args:                   []string{"stderr-exit", strconv.Itoa(code), stderrText, "{{prompt}}"},
				SessionResume:          []string{"printf", "resumed {{session_id}}: {{prompt}}"},
				TransientErrorPatterns: []string{"at capacity"},
			},
		},
	}
	return newServiceFromCfg(t, root, cfg)
}

// TestVerifyRunsAfterAgentAndPasses: the happy path of the verify step (SUP-01 B) —
// the agent finishes, the verify argv runs on the same machine, its result is
// structured on the job, both banners land in stderr (which is what a worker mirrors
// back to the hub), the event order is running → verify_started → verify_finished →
// terminal, and the result survives into the persisted row.
func TestVerifyRunsAfterAgentAndPasses(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	argv := testcmd.Cmd(t, "exit", "0")

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 30,
		Verify: argv,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if final.Verify == nil {
		t.Fatal("a job with a verify step must record its result")
	}
	if final.Verify.Status != VerifyPassed || final.Verify.ExitCode != 0 {
		t.Fatalf("verify = %+v, want passed with exit 0", *final.Verify)
	}
	if got := strings.Join(final.Verify.Command, " "); got != strings.Join(argv, " ") {
		t.Fatalf("verify command = %v, want the submitted argv %v", final.Verify.Command, argv)
	}
	if final.Verify.DurationMs < 0 {
		t.Fatalf("verify duration_ms = %d, want a non-negative measurement", final.Verify.DurationMs)
	}

	errText := verifyStderr(t, root, "self", final.ID)
	if !strings.Contains(errText, verifyBanner(argv)) {
		t.Fatalf("stderr is missing the opening verify banner:\n%s", errText)
	}
	if !strings.Contains(errText, "===== gofer verify: exit=0 dur=") {
		t.Fatalf("stderr is missing the closing verify banner:\n%s", errText)
	}

	types := eventTypes(t, s, final.ID)
	if !hasSubsequence(types, []string{EventJobRunning, EventJobVerifyStarted, EventJobVerifyFinished, EventJobTerminal}) {
		t.Fatalf("event order = %v, want running → verify_started → verify_finished → terminal", types)
	}

	// The terminal entry is evicted, so this read comes from jobs.verify_json — the
	// same path `job show` / the web detail use after a restart.
	stored, ok := s.Get(final.ID)
	if !ok {
		t.Fatalf("job %s not found after terminal", final.ID)
	}
	if stored.Verify == nil || stored.Verify.Status != VerifyPassed || stored.Verify.ExitCode != 0 {
		t.Fatalf("persisted verify = %+v, want passed/exit 0", stored.Verify)
	}
}

// TestVerifyFailureFailsJob: a verify step that fails fails the JOB (decision 2),
// with the verify exit code and an error naming the verify step — the agent's own
// success is not the job's outcome.
func TestVerifyFailureFailsJob(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	argv := testcmd.Cmd(t, "exit", "3")

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 30,
		Verify: argv,
	})
	if final.Status != StatusFailed {
		t.Fatalf("status = %s, want failed (a failed verify step fails the job)", final.Status)
	}
	if final.ExitCode != 3 {
		t.Fatalf("exit_code = %d, want the verify step's 3", final.ExitCode)
	}
	if !strings.Contains(final.Error, "verify failed") {
		t.Fatalf("error = %q, want it to name the verify failure", final.Error)
	}
	if final.Verify == nil || final.Verify.Status != VerifyFailed || final.Verify.ExitCode != 3 {
		t.Fatalf("verify = %+v, want failed/exit 3", final.Verify)
	}
	if errText := verifyStderr(t, root, "self", final.ID); !strings.Contains(errText, "===== gofer verify: exit=3 dur=") {
		t.Fatalf("stderr is missing the failing closing banner:\n%s", errText)
	}
	if !hasSubsequence(eventTypes(t, s, final.ID), []string{EventJobVerifyFinished, EventJobTerminal}) {
		t.Fatalf("events = %v, want verify_finished before the terminal event", eventTypes(t, s, final.ID))
	}
}

// TestVerifyTimeout: the verify step has its OWN deadline (independent of the
// agent's), and hitting it fails the job with exit -1 and a timeout status rather
// than hanging or succeeding.
func TestVerifyTimeout(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 60,
		Verify:           testcmd.Cmd(t, "sleep", "10s"),
		VerifyTimeoutSec: 1,
	})
	if final.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", final.Status)
	}
	if final.Verify == nil || final.Verify.Status != VerifyTimeout {
		t.Fatalf("verify = %+v, want status %s", final.Verify, VerifyTimeout)
	}
	if final.Verify.ExitCode != -1 {
		t.Fatalf("verify exit_code = %d, want -1 (a killed step has no exit status)", final.Verify.ExitCode)
	}
	if final.ExitCode != -1 {
		t.Fatalf("job exit_code = %d, want -1", final.ExitCode)
	}
	if !strings.Contains(final.Error, "verify") {
		t.Fatalf("error = %q, want it to explain the verify timeout", final.Error)
	}
	if errText := verifyStderr(t, root, "self", final.ID); !strings.Contains(errText, "===== gofer verify: exit=timeout dur=") {
		t.Fatalf("stderr is missing the timeout banner:\n%s", errText)
	}
}

// TestVerifySkippedWhenAgentFails: the verify step never runs when the agent did not
// finish normally — it is recorded as skipped (with the reason), the job keeps the
// AGENT's failure, and nothing is written to the log.
func TestVerifySkippedWhenAgentFails(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "exit", "2"), Cwd: ".", TimeoutSec: 30,
		Verify: testcmd.Cmd(t, "exit", "0"),
	})
	if final.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", final.Status)
	}
	if final.ExitCode != 2 {
		t.Fatalf("exit_code = %d, want the agent's 2 (the verify step never ran)", final.ExitCode)
	}
	if final.Verify == nil || final.Verify.Status != VerifySkipped {
		t.Fatalf("verify = %+v, want status %s", final.Verify, VerifySkipped)
	}
	if !strings.Contains(final.Verify.Reason, "agent failed") {
		t.Fatalf("verify reason = %q, want it to say the agent failed", final.Verify.Reason)
	}
	if errText := verifyStderr(t, root, "self", final.ID); strings.Contains(errText, "gofer verify:") {
		t.Fatalf("a skipped verify step must write nothing to the log:\n%s", errText)
	}
}

// TestVerifyFailureUnderReviewParksNeedsReview: with人工验收 on, a failed verify step
// parks the job in needs_review (the human decides whether to keep a delivery whose
// verification failed) while the failing result stays on the job for the reviewer.
func TestVerifyFailureUnderReviewParksNeedsReview(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 30,
		Verify: testcmd.Cmd(t, "exit", "1"), Review: true,
	})
	if final.Status != StatusNeedsReview {
		t.Fatalf("status = %s, want needs_review", final.Status)
	}
	if final.Verify == nil || final.Verify.Status != VerifyFailed || final.Verify.ExitCode != 1 {
		t.Fatalf("verify = %+v, want failed/exit 1 kept for the reviewer", final.Verify)
	}
	types := eventTypes(t, s, final.ID)
	if !hasSubsequence(types, []string{EventJobNeedsReview}) {
		t.Fatalf("events = %v, want %s", types, EventJobNeedsReview)
	}
	if hasSubsequence(types, []string{EventJobTerminal}) {
		t.Fatalf("a parked job must not announce a terminal event: %v", types)
	}
}

// TestVerifyFailureIsNotTransient: a verify failure is NOT a transient agent error —
// it must not trigger the automatic continuation, even when everything else about the
// job (session, budget, a provider pattern in stderr) qualifies. The control case —
// the SAME agent failing on its own with a skipped verify — proves that the
// auto-resume configuration really is live for this agent, so the guard, not a
// vacuous config, is what keeps the verify failure from being re-run.
func TestVerifyFailureIsNotTransient(t *testing.T) {
	const transient = "ERROR: Selected model is at capacity. Please try a different model."

	guarded := newVerifyTransientService(t, t.TempDir(), transient, 0)
	src := submitAndWait(t, guarded, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30, SessionID: "sess-verify",
		Verify: testcmd.Cmd(t, "exit", "3"),
	})
	if src.Status != StatusFailed {
		t.Fatalf("setup: status = %s, want failed (the verify step failed)", src.Status)
	}
	if src.Verify == nil || src.Verify.Status != VerifyFailed {
		t.Fatalf("verify = %+v, want the failed step", src.Verify)
	}
	waitAutoResumed(t, guarded, src.ID, false)
	if !hasSubsequence(eventTypes(t, guarded, src.ID), []string{EventJobTerminal}) {
		t.Fatalf("events = %v, want a plain terminal event", eventTypes(t, guarded, src.ID))
	}

	// Control: the agent itself fails (verify skipped), and the continuation DOES fire.
	live := newVerifyTransientService(t, t.TempDir(), transient, 1)
	ctrl := submitAndWait(t, live, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30, SessionID: "sess-control",
		Verify: testcmd.Cmd(t, "exit", "0"),
	})
	if ctrl.Verify == nil || ctrl.Verify.Status != VerifySkipped {
		t.Fatalf("control verify = %+v, want a skipped step", ctrl.Verify)
	}
	waitAutoResumed(t, live, ctrl.ID, true)
}

// TestVerifyRunsInsideWorktree: the verify step runs on the SAME machine, in the
// SAME directory and with the SAME env as the agent — for a worktree job that means
// inside the job's worktree (verifying the checkout the agent actually edited, not
// the shared one).
func TestVerifyRunsInsideWorktree(t *testing.T) {
	repo, _ := gitRepo(t)
	state := t.TempDir()
	s := newWorktreeService(t, repo, state)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 60,
		Worktree: true,
		Verify:   testcmd.Cmd(t, "env-cwd"),
		Env:      map[string]string{"DAB_TEST_VAR": "verify-env"},
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if final.Verify == nil || final.Verify.Status != VerifyPassed {
		t.Fatalf("verify = %+v, want passed", final.Verify)
	}
	if final.WorktreePath == "" {
		t.Fatal("setup: a --worktree job must record its worktree path")
	}

	out := verifyStderr(t, state, "repo", final.ID)
	if !strings.Contains(out, final.WorktreePath) {
		t.Fatalf("verify must run inside the job's worktree %q:\n%s", final.WorktreePath, out)
	}
	if !strings.Contains(out, "verify-env") {
		t.Fatalf("verify must run with the job's env:\n%s", out)
	}
}

// TestSubmitVerifyRequiresAllowExec: the verify argv comes from the submitter — the
// same trust surface as an exec job's argv — so a project without allow_exec rejects
// it at admission instead of running it. The --verify/--no-verify contradiction is
// rejected too, wherever it was submitted from.
func TestSubmitVerifyRequiresAllowExec(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	_, err := s.Submit(JobRequest{
		ProjectKey: "noexec", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 30,
		Verify: testcmd.Cmd(t, "exit", "0"),
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	if !strings.Contains(err.Error(), "verify requires allow_exec") {
		t.Fatalf("err = %q, want it to name the allow_exec requirement", err)
	}

	_, err = s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 30,
		Verify: testcmd.Cmd(t, "exit", "0"), NoVerify: true,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("verify+no_verify: err = %v, want ErrInvalidRequest", err)
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("verify+no_verify: err = %q, want it to say the two contradict", err)
	}
}

// TestNoVerifyDisablesProjectDefault: a project-level verify default applies to
// every job of that project, and --no-verify turns it off for exactly one job —
// including the case where the caller must not run the (failing) default.
func TestNoVerifyDisablesProjectDefault(t *testing.T) {
	root := t.TempDir()
	bin := testcmd.Path(t)
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
				Verify:         []string{bin, "exit", "3"},
			},
		},
	}
	s := newServiceFromCfg(t, root, cfg)

	// Control: the project default really is live.
	def := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 30,
	})
	if def.Status != StatusFailed || def.Verify == nil || def.Verify.Status != VerifyFailed {
		t.Fatalf("project default verify did not run: status=%s verify=%+v", def.Status, def.Verify)
	}

	off := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 30,
		NoVerify: true,
	})
	if off.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done with --no-verify", off.Status, off.Error)
	}
	if off.Verify != nil {
		t.Fatalf("--no-verify must record no verify step, got %+v", off.Verify)
	}
	if errText := verifyStderr(t, root, "self", off.ID); strings.Contains(errText, "gofer verify:") {
		t.Fatalf("--no-verify must not run the project default:\n%s", errText)
	}
}

package job

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/util"
)

// resolveVerify resolves the job's verify step and its deadline IN PLACE (SUP-01 B)
// from the SAME config snapshot the rest of Submit uses: an explicit `--verify` beats
// the project's default, `--no-verify` clears that default for this job, and a
// deadline nobody set falls back to the project's (or DefaultVerifyTimeoutSec).
// Resolving here — and not on the executing side — is what makes the Forward,
// request_json, the admission gates and the persisted row carry ONE decided pair.
func resolveVerify(cfg *config.Config, req *JobRequest) error {
	// --verify and --no-verify contradict each other (turn the step on / off for this
	// job), and the contradiction must be caught BEFORE --no-verify clears the argv —
	// otherwise the request would silently be read as "no step" and the caller's
	// explicit command would vanish.
	if len(req.Verify) > 0 && req.NoVerify {
		return fmt.Errorf("%w: verify and no_verify are mutually exclusive", ErrInvalidRequest)
	}
	if req.NoVerify {
		req.Verify = nil
	}
	if !req.NoVerify && len(req.Verify) == 0 {
		req.Verify = cfg.Projects[req.ProjectKey].Verify
	}
	if req.VerifyTimeoutSec <= 0 {
		req.VerifyTimeoutSec = cfg.EffectiveVerifyTimeoutSec(req.ProjectKey)
	}
	return nil
}

// verifyBlocked reports whether a verify step makes the job a failure: a step that
// RAN and did not pass. A skipped step says nothing about the work (the agent never
// finished), and a passed one is the acceptance we wanted.
func verifyBlocked(v *VerifyResult) bool {
	return v != nil && (v.Status == runner.VerifyFailed || v.Status == runner.VerifyTimeout)
}

// verifySkipReason returns why the step must not run for a run that ended with res
// under ctx — only a NORMAL agent finish (exit 0, no error, no cancel/timeout) leaves
// something worth verifying, and the returned text is what the skipped result
// records. "" means "run it".
func verifySkipReason(ctx context.Context, res runner.Result) string {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "job timeout"
	case errors.Is(ctx.Err(), context.Canceled):
		return "job cancelled"
	case res.Err != nil, res.ExitCode != 0:
		return "agent failed"
	}
	return ""
}

// runVerify executes one job's verify step on THIS machine (SUP-01 B) and records it
// on the job: the structured result (entry.result.Verify), the two log banners around
// the command's own output (stdout+stderr merged into the job's stderr log, which is
// what a worker mirrors back to the hub) and the job.verify_started / _finished
// events. It is BEST-EFFORT with respect to the job's own control flow: it never
// panics, never blocks past its deadline and never touches the terminal decision
// itself — foldVerify does that from the recorded result.
//
// The step is run ONLY for a locally executed job (execute calls it only when there
// is no Forward): a remote job's step runs on the worker that owns the checkout, and
// its result arrives through the outcome — running it again here would verify the
// wrong tree.
func (s *Service) runVerify(ctx context.Context, entry *jobEntry, req runner.Request, res runner.Result) {
	if len(req.Verify) == 0 {
		return
	}
	argv := req.Verify

	if reason := verifySkipReason(ctx, res); reason != "" {
		// Nothing ran, so only the outcome is recorded (no start event, no banners —
		// the log must not claim a step that never happened).
		v := &runner.VerifyResult{Command: argv, Status: runner.VerifySkipped, Reason: reason}
		s.setVerify(entry, v)
		s.recordEvent(req.JobID, EventJobVerifyFinished, map[string]any{
			"status": v.Status, "exit_code": 0, "duration_ms": 0, "reason": reason,
		})
		return
	}

	s.recordEvent(req.JobID, EventJobVerifyStarted, map[string]any{"command": strings.Join(argv, " ")})

	vctx := ctx
	if req.VerifyTimeoutSec > 0 {
		var cancel context.CancelFunc
		vctx, cancel = context.WithTimeout(ctx, time.Duration(req.VerifyTimeoutSec)*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(vctx, argv[0], argv[1:]...)
	cmd.Dir = req.WorkDir
	cmd.Env = verifyEnv(req)
	// stdout+stderr merged into the JOB's stderr log: the agent's own output stays
	// readable and a remote execution mirrors both streams in one channel (D6).
	cmd.Stdout = req.Stderr
	cmd.Stderr = req.Stderr

	writeVerifyBanner(req.Stderr, "===== gofer verify: "+strings.Join(argv, " ")+" =====")
	started := time.Now()
	runErr := cmd.Run()
	dur := time.Since(started)

	status, code, reason := verifyOutcome(vctx, runErr)
	writeVerifyBanner(req.Stderr, verifyExitBanner(status, code, dur))

	v := &runner.VerifyResult{
		Command: argv, Status: status, ExitCode: code,
		DurationMs: dur.Milliseconds(), Reason: reason,
	}
	s.setVerify(entry, v)
	s.recordEvent(req.JobID, EventJobVerifyFinished, map[string]any{
		"status": status, "exit_code": code, "duration_ms": v.DurationMs, "reason": reason,
	})
}

// verifyOutcome classifies a finished verify child: a deadline that fired (its own
// or the job's) is a timeout with no exit status, a non-zero exit is a failure, and
// anything else is the pass. vctx is the context the child ran under, so a kill by
// its own deadline is distinguishable from the command simply returning non-zero.
func verifyOutcome(vctx context.Context, runErr error) (status string, exitCode int, reason string) {
	if ctxErr := vctx.Err(); ctxErr != nil {
		switch {
		case errors.Is(ctxErr, context.DeadlineExceeded):
			return runner.VerifyTimeout, -1, "the verify step exceeded its timeout"
		default:
			return runner.VerifyTimeout, -1, "the job ended while the verify step ran"
		}
	}
	if runErr == nil {
		return runner.VerifyPassed, 0, ""
	}
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		return runner.VerifyFailed, ee.ExitCode(), ""
	}
	// The child could not be started at all (bad argv[0], permission): that is a
	// failure of the step, reported with no exit status and the reason why.
	return runner.VerifyFailed, -1, runErr.Error()
}

// verifyExitBanner renders the closing banner. A step that was killed says so
// instead of inventing an exit code.
func verifyExitBanner(status string, exitCode int, dur time.Duration) string {
	code := fmt.Sprintf("exit=%d", exitCode)
	if status == runner.VerifyTimeout {
		code = "exit=timeout"
	}
	return fmt.Sprintf("===== gofer verify: %s dur=%d =====", code, dur.Milliseconds())
}

// writeVerifyBanner writes one banner line to the job's stderr log, nil-safe (a job
// whose log writer is missing simply has no banners).
func writeVerifyBanner(w io.Writer, line string) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, "\n%s\n", line)
}

// verifyEnv layers the job's env (agent config + job env + gofer metadata, already
// merged by Submit) over this process's env INHERITED-MINUS-DENY, exactly as the
// agent's own child gets it (SEC-01: the verify step is a job-spawned process too
// and must not see the serve process's bearer token either). A nil/empty extra map
// still gets the filter applied — the point is what the step does NOT inherit.
func verifyEnv(req runner.Request) []string {
	return util.EnvironWithout(req.EnvDeny, req.EnvAllow, req.Env)
}

// setVerify records the step's result on the in-memory job entry; finish persists it
// with the rest of the terminal snapshot.
func (s *Service) setVerify(entry *jobEntry, v *runner.VerifyResult) {
	entry.mu.Lock()
	entry.result.Verify = v
	entry.mu.Unlock()
}

// foldVerify merges a recorded verify result into the job's terminal outcome — the
// step's verdict REPLACES the agent's when it did not pass (decision 2: the job is
// `failed`, not done, and carries the step's exit code and an explanatory error).
//
// It is a no-op for everything else: no step, a passed step, a skipped step (the
// agent never finished, so its own status stands) and a job the CONTEXT already
// ended (a cancel/timeout is the real cause; a step killed along with it must not
// relabel the job).
func (s *Service) foldVerify(entry *jobEntry, status string, code int, runErr error) (string, int, error) {
	entry.mu.Lock()
	v := entry.result.Verify
	entry.mu.Unlock()
	if !verifyBlocked(v) {
		return status, code, runErr
	}
	if status != StatusDone && status != StatusFailed {
		return status, code, runErr
	}
	if v.Status == runner.VerifyTimeout {
		reason := v.Reason
		if reason == "" {
			reason = "the verify step did not finish"
		}
		return StatusFailed, v.ExitCode, fmt.Errorf("verify timeout: %s", reason)
	}
	return StatusFailed, v.ExitCode, fmt.Errorf("verify failed: exit %d", v.ExitCode)
}

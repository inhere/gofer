package job

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// sweeperWorkflowScan caps how many running workflows the sweeper inspects per
// tick. A workflow只 has one active step, so this is a generous ceiling.
const sweeperWorkflowScan = 500

// StepSpec is one step of a工作流(job 链): a普通 job request expressed declaratively.
// It carries the same fields a single JobRequest needs (project/agent/runner +
// prompt/cmd/cwd/timeout/tags). P1 runs each step independently (no cross-step
// data); P2 adds ${steps.N.field} references resolved before the step starts.
//
// P1 adds per-step失败策略 (design §5.1, D17): OnFailure ∈ ""/fail (v1 fail-fast)
// | continue (skip a failed step) | retry (re-run with backoff, bounded by Retry).
// Both fields are omitempty with a zero value == v1 behaviour (OnFailure==""=fail,
// Retry==nil=no retry), so v1 specs deserialize and run unchanged (D23).
type StepSpec struct {
	Name       string   `json:"name,omitempty" yaml:"name,omitempty"`
	ProjectKey string   `json:"project_key" yaml:"project_key"`
	Agent      string   `json:"agent" yaml:"agent"`
	Runner     string   `json:"runner" yaml:"runner"`
	Prompt     string   `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	Cmd        []string `json:"cmd,omitempty" yaml:"cmd,omitempty"`
	Cwd        string   `json:"cwd,omitempty" yaml:"cwd,omitempty"`
	TimeoutSec int      `json:"timeout_sec,omitempty" yaml:"timeout_sec,omitempty"`
	Tags       []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	// OnFailure is the per-step失败策略 (P1, design §5.1): "" / "fail" keeps v1
	// fail-fast (the whole workflow fails); "continue" skips the failed step and
	// advances to the next; "retry" re-runs the step (bounded by Retry, backoff
	// per RetryPolicy.BackoffSec). Zero value ("") == v1 behaviour (D23).
	OnFailure string `json:"on_failure,omitempty" yaml:"on_failure,omitempty"`
	// Retry is the retry policy used when OnFailure=="retry" (P1, design §5.1).
	// nil == no retry (the v1 default); validated at submit (validateRetry).
	Retry *RetryPolicy `json:"retry,omitempty" yaml:"retry,omitempty"`
}

// RetryPolicy bounds per-step (and per-job, E24) retry on failure (P1, design
// §5.1, D16). MaxAttempts counts the FIRST run as attempt 1, so MaxAttempts==3
// means up to 2 retries. BackoffSec is the退避表 indexed by the just-failed
// attempt (defaults to the SR606 table when empty). OnExitCodes, when non-empty,
// restricts retry to those exit codes (empty == retry on any non-zero exit /
// timeout / failure, see retryableExit).
type RetryPolicy struct {
	MaxAttempts int   `json:"max_attempts" yaml:"max_attempts"`               // >=1 (includes the first run)
	BackoffSec  []int `json:"backoff_sec,omitempty" yaml:"backoff_sec,omitempty"` // 默认接 SR606 [30,120,300,900,3600]
	OnExitCodes []int `json:"on_exit_codes,omitempty" yaml:"on_exit_codes,omitempty"` // 空=任意非0退出重试
}

// onFailure* are the known StepSpec.OnFailure values. "" is treated as
// onFailureFail (v1 fail-fast) so a v1 spec maps to fail without change (D23).
const (
	onFailureFail     = "fail"
	onFailureContinue = "continue"
	onFailureRetry    = "retry"
)

// maxRetryAttempts caps RetryPolicy.MaxAttempts so a misconfigured workflow can
// not retry forever (defence against失控 retry storms).
const maxRetryAttempts = 10

// defaultBackoffSec is the SR606退避表 used when a RetryPolicy gives no explicit
// BackoffSec: 30s → 2min → 5min → 15min → 60min, the last entry reused past the
// end (mirrors the E14 deliveryBackoff table).
var defaultBackoffSec = []int{30, 120, 300, 900, 3600}

// WorkflowSpec is the submitted job-chain: a title + an ordered list of steps run
// strictly serially (single active step, D1/D10). It is the body of POST
// /v1/workflows and the parsed yaml workflow file (P3).
type WorkflowSpec struct {
	Title string     `json:"title,omitempty" yaml:"title,omitempty"`
	Steps []StepSpec `json:"steps" yaml:"steps"`
}

// SubmitWorkflow validates the spec, persists a running workflow header
// (current_step=1, total=len(steps)) and starts the FIRST step's job. The
// remaining steps are started one at a time by advanceWorkflow as each prior step
// reaches a terminal state (finish hook + sweeper, all幂等).
//
// Validation: at least one step, and EVERY step must pass the same single-job
// validate() (project/agent/runner allowlist + exec gate) so a workflow never
// smuggles a step past单 job 准入 (安全要点). A rejected step fails the whole submit
// before any DB row / job is created. If step-1's Submit fails, the workflow is
// marked failed and the error returned.
func (s *Service) SubmitWorkflow(spec WorkflowSpec, callerID string) (jobstore.Workflow, error) {
	if len(spec.Steps) == 0 {
		return jobstore.Workflow{}, fmt.Errorf("%w: workflow has no steps", ErrInvalidRequest)
	}

	// Static ${steps.N.field} reference check (P2): every ref must point at an
	// earlier step with a known field (no self/forward ref, step1 ref-free). Rejected
	// at submit so the chain never starts a step it cannot resolve mid-flight.
	if err := validateRefs(spec); err != nil {
		return jobstore.Workflow{}, err
	}

	// P1: per-step失败策略/retry validation. on_failure must be a known value and a
	// retry policy must be well-formed (max_attempts in [1,maxRetryAttempts]). A v1
	// spec (no on_failure / no retry) passes unchanged (D23).
	if err := validateRetry(spec); err != nil {
		return jobstore.Workflow{}, err
	}

	// Pre-validate every step against the single-job准入 before creating anything,
	// so an invalid step (e.g. step 3) is rejected at submit time, not mid-chain.
	cfg := s.config()
	for i := range spec.Steps {
		req := stepToRequest(spec.Steps[i], "", i+1, 1, callerID)
		remote := isRemoteRunner(cfg, req.Runner)
		if _, err := s.validate(cfg, req, remote); err != nil {
			return jobstore.Workflow{}, fmt.Errorf("step %d: %w", i+1, err)
		}
	}

	specJSON, err := json.Marshal(spec)
	if err != nil {
		return jobstore.Workflow{}, fmt.Errorf("marshal workflow spec: %w", err)
	}

	wfID := s.genWorkflowID()
	now := s.nowFn().Unix()
	wf := jobstore.Workflow{
		ID:          wfID,
		Title:       spec.Title,
		Status:      jobstore.WorkflowRunning,
		CurrentStep: 1,
		StepAttempt: 1, // P1: the active step's 1-based attempt (first run == 1)
		TotalSteps:  len(spec.Steps),
		SpecJSON:    string(specJSON),
		CallerID:    callerID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.meta.InsertWorkflow(wf); err != nil {
		return jobstore.Workflow{}, err
	}
	// P1: submitted event (workflow_events timeline starts here).
	s.recordWorkflowEvent(wfID, EventWorkflowSubmitted, map[string]any{
		"title":       spec.Title,
		"total_steps": len(spec.Steps),
		"caller_id":   callerID,
	})

	// Start step 1 (attempt 1). On failure mark the workflow failed (fail-fast at
	// the source) and return the error; the header row stays for inspection.
	// step.started is recorded BEFORE Submit (a fast step 1 can finish and record
	// workflow.terminal before Submit returns, inverting event order).
	req := stepToRequest(spec.Steps[0], wfID, 1, 1, callerID)
	s.recordWorkflowEvent(wfID, EventStepStarted, map[string]any{
		"step": 1, "attempt": 1, "job_id": req.RequestID,
	})
	if _, err := s.Submit(req); err != nil {
		_ = s.meta.SetWorkflowStatus(wfID, jobstore.WorkflowFailed, "submit step 1: "+err.Error())
		s.recordWorkflowEvent(wfID, EventWorkflowTerminal, map[string]any{
			"status": jobstore.WorkflowFailed,
			"error":  "submit step 1: " + err.Error(),
		})
		return jobstore.Workflow{}, err
	}

	// Return the freshly-created header snapshot.
	got, _, gerr := s.meta.GetWorkflow(wfID)
	if gerr != nil {
		return wf, nil // best-effort: the row exists; return the in-hand copy
	}
	return got, nil
}

// stepToRequest maps a StepSpec to a JobRequest, binding it to its workflow +
// 1-based step index and inheriting the workflow's caller id (D8). The internal
// WorkflowID/StepIndex/Attempt fields are how finish knows to advance the chain.
//
// ⭐ 幂等核心 (P1, plan ⭐节 1): every step-job carries a DETERMINISTIC request_id
// "<wfID>:s<step>:a<attempt>". Re-driven through the C5 unique-index idempotency
// (service.go:200), this guarantees that no matter how many concurrent callers
// (finish hook + sweeper + duplicates) try to start the SAME (step, attempt), at
// most ONE job is ever created — the root of step-start idempotency. wfID=="" (the
// submit-time pre-validation pass) leaves request_id empty (no idempotency需要).
func stepToRequest(step StepSpec, wfID string, stepIndex, attempt int, callerID string) JobRequest {
	reqID := ""
	if wfID != "" {
		reqID = fmt.Sprintf("%s:s%d:a%d", wfID, stepIndex, attempt)
	}
	return JobRequest{
		ProjectKey: step.ProjectKey,
		Agent:      step.Agent,
		Runner:     step.Runner,
		Prompt:     step.Prompt,
		Cmd:        step.Cmd,
		Cwd:        step.Cwd,
		TimeoutSec: step.TimeoutSec,
		Title:      step.Name,
		Tags:       step.Tags,
		CallerID:   callerID,
		WorkflowID: wfID,
		StepIndex:  stepIndex,
		Attempt:    attempt,
		RequestID:  reqID,
	}
}

// validateRetry checks each step's失败策略 at submit time (P1). on_failure must be
// one of the known values; a step with on_failure=="retry" MUST carry a retry
// block with max_attempts in [1,maxRetryAttempts]; a non-retry step MUST NOT carry
// a retry block (caught early so a misconfigured policy never starts). A v1 spec
// (on_failure=="" and retry==nil) passes unchanged (D23).
func validateRetry(spec WorkflowSpec) error {
	for i := range spec.Steps {
		stepNo := i + 1
		st := spec.Steps[i]
		switch st.OnFailure {
		case "", onFailureFail, onFailureContinue:
			if st.Retry != nil {
				return fmt.Errorf("%w: step %d has a retry block but on_failure=%q (retry only applies to on_failure=retry)", ErrInvalidRequest, stepNo, st.OnFailure)
			}
		case onFailureRetry:
			if st.Retry == nil {
				return fmt.Errorf("%w: step %d on_failure=retry requires a retry block", ErrInvalidRequest, stepNo)
			}
			if st.Retry.MaxAttempts < 1 {
				return fmt.Errorf("%w: step %d retry.max_attempts must be >= 1", ErrInvalidRequest, stepNo)
			}
			if st.Retry.MaxAttempts > maxRetryAttempts {
				return fmt.Errorf("%w: step %d retry.max_attempts %d exceeds the limit %d", ErrInvalidRequest, stepNo, st.Retry.MaxAttempts, maxRetryAttempts)
			}
		default:
			return fmt.Errorf("%w: step %d has unknown on_failure %q (want fail/continue/retry)", ErrInvalidRequest, stepNo, st.OnFailure)
		}
	}
	return nil
}

// maxAttempts returns the step's configured retry ceiling. Delegates to
// maxAttemptsPolicy so step-level and job-level (E24) retry share one semantics.
func maxAttempts(step StepSpec) int { return maxAttemptsPolicy(step.Retry) }

// backoffFor returns the backoff (seconds) before re-running a step whose attempt
// just failed. Delegates to backoffForPolicy (one shared semantics, E24).
func backoffFor(step StepSpec, attempt int) int { return backoffForPolicy(step.Retry, attempt) }

// retryableExit reports whether a failed step-job is retryable given the step's
// policy. Delegates to retryableExitPolicy (one shared semantics, E24).
func retryableExit(step StepSpec, exitCode int) bool {
	return retryableExitPolicy(step.Retry, exitCode)
}

// maxAttemptsPolicy returns a RetryPolicy's attempt ceiling (MaxAttempts), or 1 (no
// retry) when the policy is nil / unset. Shared by step-level and job-level retry.
func maxAttemptsPolicy(p *RetryPolicy) int {
	if p == nil || p.MaxAttempts < 1 {
		return 1
	}
	return p.MaxAttempts
}

// backoffForPolicy returns the backoff (seconds) before re-running an attempt that
// just failed. attempt is the 1-based number of the run that just failed; the
// backoff table is indexed by attempt-1 (attempt 1 → table[0]), clamped to the last
// entry past the end (SR606). An empty/absent BackoffSec falls back to the SR606
// defaultBackoffSec. Shared by step-level and job-level retry (one semantics).
func backoffForPolicy(p *RetryPolicy, attempt int) int {
	table := defaultBackoffSec
	if p != nil && len(p.BackoffSec) > 0 {
		table = p.BackoffSec
	}
	idx := attempt - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(table) {
		idx = len(table) - 1
	}
	return table[idx]
}

// retryableExitPolicy reports whether a failure with exitCode is retryable under a
// RetryPolicy.OnExitCodes. An empty/absent OnExitCodes means "retry on any non-zero
// exit" (the default). When OnExitCodes is set, only those exit codes are retried.
// Shared by step-level and job-level retry (one semantics).
func retryableExitPolicy(p *RetryPolicy, exitCode int) bool {
	if p == nil || len(p.OnExitCodes) == 0 {
		return true
	}
	for _, c := range p.OnExitCodes {
		if c == exitCode {
			return true
		}
	}
	return false
}

// genWorkflowID returns a collision-resistant workflow id, "wf-" prefixed so it
// is visually distinct from a job id (which shares the same time+random scheme).
func (s *Service) genWorkflowID() string {
	return "wf-" + s.nowFn().Format(jobIDLayout) + "-" + randomSuffix()
}

// advanceWorkflow advances a workflow when its current step's job reaches a
// terminal state. It is the串行推进引擎 and is designed to be called MANY times,
// concurrently, for the same workflow — the finish hook (one call per step-job
// terminal) AND the sweeper (crash recovery) both invoke it. 幂等 rests on TWO
// layers (plan ⭐节):
//  1. every step-job carries a DETERMINISTIC request_id (stepToRequest) so the C5
//     unique index lets at most ONE job exist per (step, attempt);
//  2. every状态转移 (推进/重试/继续) is one AdvanceStep二元组抢权 — only the
//     winner of the conditional UPDATE on (current_step, step_attempt) proceeds.
//
// step 序号/spec 下标对齐 (核心)：wf.CurrentStep is 1-based and points at the
// active/just-finished step (job step_index == CurrentStep, attempt step_attempt);
// spec.Steps is 0-based, so the active step is spec.Steps[cur-1] and the NEXT step
// is spec.Steps[cur] (== 1-based step cur+1). We read (cur,att) FIRST, then抢权,
// then act on the locally-captured values so the index is unambiguous.
//
// P1 失败分支 (design §6.1, D17): on a non-done terminal step, on_failure decides:
//   - retry:    schedule attempt+1 with backoff (set next_step_at, NO immediate
//               start — the sweeper starts it once due, request_id兜底 idempotent);
//   - continue: skip the failed step, advance to the next;
//   - fail/"":  v1 fail-fast (the whole workflow fails).
//
// best-effort: failures are logged, never panic; a missed advance is re-tried by
// the sweeper on its next tick.
func (s *Service) advanceWorkflow(wfID string) {
	wf, ok, err := s.meta.GetWorkflow(wfID)
	if err != nil || !ok || wf.Status != jobstore.WorkflowRunning {
		return // unknown or already terminal: nothing to advance
	}

	// 退避未到点：a retry scheduled next_step_at into the future — leave it for a
	// later sweeper tick. The sweeper re-reads the header each tick, so once
	// next_step_at <= now this guard passes and the attempt+1 job is started below.
	if wf.NextStepAt > s.nowFn().Unix() {
		return
	}

	jobs, err := s.meta.ListWorkflowJobs(wfID)
	if err != nil {
		return
	}
	cur, att := wf.CurrentStep, wf.StepAttempt // 1-based step + 1-based attempt
	curJob := stepJobAttempt(jobs, cur, att)
	if curJob == nil {
		// The (cur,att) job has not been started yet. This is the retry-due / crash
		// path: a retry set (cur,att+1) but did not start the job, OR a crash lost the
		// start. Start it now — the deterministic request_id makes a concurrent
		// duplicate start a no-op (C5 idempotency兜底).
		s.startStepJob(wf, cur, att, jobs)
		return
	}
	if !isTerminal(curJob.Status) {
		return // current attempt's job is still running
	}

	var spec WorkflowSpec
	if err := json.Unmarshal([]byte(wf.SpecJSON), &spec); err != nil {
		if won, _ := s.meta.AdvanceStep(wfID, cur, att, cur+1, 1, 0); won {
			s.setWorkflowFailed(wfID, "decode spec: "+err.Error())
		}
		return
	}
	if cur < 1 || cur > len(spec.Steps) {
		if won, _ := s.meta.AdvanceStep(wfID, cur, att, cur+1, 1, 0); won {
			s.setWorkflowFailed(wfID, "spec/total step mismatch")
		}
		return
	}
	step := spec.Steps[cur-1] // 0-based: the active/just-finished step

	switch curJob.Status {
	case StatusDone:
		// Win推进权 for this (step,attempt) → (cur+1, 1). Only the winner advances /
		// starts the next step, so done is committed exactly once.
		won, aerr := s.meta.AdvanceStep(wfID, cur, att, cur+1, 1, 0)
		if aerr != nil || !won {
			return
		}
		if cur >= wf.TotalSteps {
			s.setWorkflowDone(wfID) // last step done -> workflow done
			return
		}
		s.startNextStep(wf, cur, jobs, spec) // resolveRefs + start step cur+1 (attempt 1)
	default: // failed / timeout / cancelled
		switch step.OnFailure {
		case onFailureRetry:
			if att < maxAttempts(step) && retryableExit(step, curJob.ExitCode) {
				// 重试: (cur,att) → (cur,att+1) with backoff. Set next_step_at; DO NOT
				// start the attempt+1 job here — the sweeper starts it once due (so the
				// backoff is honoured), and the deterministic request_id keeps a racing
				// start idempotent. Win抢权 once so the schedule is committed exactly once.
				backoff := backoffFor(step, att)
				next := s.nowFn().Unix() + int64(backoff)
				won, aerr := s.meta.AdvanceStep(wfID, cur, att, cur, att+1, next)
				if aerr != nil || !won {
					return
				}
				s.recordWorkflowEvent(wfID, EventStepRetry, map[string]any{
					"step": cur, "attempt": att, "next_attempt": att + 1,
					"backoff_sec": backoff, "next_step_at": next,
				})
				// Promptly re-drive once the backoff elapses (an in-process timer), so the
				// attempt+1 job starts without waiting for the next sweeper tick. The
				// sweeper (AdvanceRunningWorkflows) remains the RELIABLE backstop: if this
				// timer is lost (process restart), the sweeper picks the due retry up. A
				// zero/elapsed backoff fires (near-)immediately. The re-advance re-reads the
				// header, so it is idempotent with the sweeper (request_id + AdvanceStep).
				s.scheduleRetryAdvance(wfID, backoff)
				return
			}
			// Retry exhausted (or this exit code is not retryable): fail-fast.
			won, aerr := s.meta.AdvanceStep(wfID, cur, att, cur+1, 1, 0)
			if aerr != nil || !won {
				return
			}
			s.setWorkflowFailed(wfID, fmt.Sprintf("step %d %s after %d attempt(s)", cur, curJob.Status, att))
		case onFailureContinue:
			// 继续: skip the failed step, advance to the next (or finish).
			won, aerr := s.meta.AdvanceStep(wfID, cur, att, cur+1, 1, 0)
			if aerr != nil || !won {
				return
			}
			s.recordWorkflowEvent(wfID, EventStepSkipped, map[string]any{
				"step": cur, "attempt": att, "status": curJob.Status,
			})
			if cur >= wf.TotalSteps {
				s.setWorkflowDone(wfID) // last step skipped -> workflow done
				return
			}
			s.startNextStep(wf, cur, jobs, spec)
		default: // "" / fail: v1 fail-fast (D17 default)
			won, aerr := s.meta.AdvanceStep(wfID, cur, att, cur+1, 1, 0)
			if aerr != nil || !won {
				return
			}
			s.setWorkflowFailed(wfID, fmt.Sprintf("step %d %s", cur, curJob.Status))
		}
	}
}

// startNextStep resolves the next step's refs and starts its job (attempt 1) after
// a step finished done (or was skipped via on_failure=continue). cur is the
// 1-based JUST-FINISHED step; the next step is spec.Steps[cur] (0-based) == step
// cur+1. A resolve failure fails the whole workflow (the advance已抢权, so this
// runs exactly once). priorJobs are the already-started step-jobs (for ref resolve).
func (s *Service) startNextStep(wf jobstore.Workflow, cur int, priorJobs []jobstore.JobRecord, spec WorkflowSpec) {
	next := spec.Steps[cur] // 0-based: the next step (1-based step cur+1)
	// P2 接入点：resolveRefs 把 ${steps.N.field} 替换为前序产出。替换失败 → 整条工作流
	// failed，不起此步（advance 已抢权，此处只跑一次）。
	if err := s.resolveRefs(&next, priorJobs); err != nil {
		s.setWorkflowFailed(wf.ID, fmt.Sprintf("step %d resolve refs: %s", cur+1, err.Error()))
		return
	}
	req := stepToRequest(next, wf.ID, cur+1, 1, wf.CallerID)
	// Record step.started BEFORE Submit: a fast step can finish (and its finish hook
	// record workflow.terminal) before Submit even returns, so recording after would
	// invert the event order (terminal before started). The deterministic request_id
	// is known up front, so this is safe to record pre-Submit.
	s.recordWorkflowEvent(wf.ID, EventStepStarted, map[string]any{
		"step": cur + 1, "attempt": 1, "job_id": req.RequestID,
	})
	if _, err := s.Submit(req); err != nil {
		s.setWorkflowFailed(wf.ID, "submit step: "+err.Error())
		return
	}
}

// startStepJob (re)starts the (step, attempt) job for the CURRENT pointer. It is
// the retry-due / crash-recovery entry: advanceWorkflow calls it when the active
// (cur,att) job does not yet exist (a retry set the pointer to att+1 but did not
// start the job, or a crash lost the start). It resolves the step's refs and
// Submits with the deterministic request_id — so even if finish-hook and sweeper
// race here, C5 idempotency guarantees ONE job for (step,attempt). A resolve/submit
// failure fails the whole workflow. priorJobs feed ref resolution.
//
// NOTE: this does NOT抢 AdvanceStep — the pointer is already at (step,attempt); the
// request_id IS the idempotency barrier for the start. Multiple concurrent callers
// all compute the same request_id and the unique index admits one.
func (s *Service) startStepJob(wf jobstore.Workflow, step, attempt int, priorJobs []jobstore.JobRecord) {
	var spec WorkflowSpec
	if err := json.Unmarshal([]byte(wf.SpecJSON), &spec); err != nil {
		s.setWorkflowFailed(wf.ID, "decode spec: "+err.Error())
		return
	}
	if step < 1 || step > len(spec.Steps) {
		s.setWorkflowFailed(wf.ID, "spec/total step mismatch")
		return
	}
	stepSpec := spec.Steps[step-1]
	if err := s.resolveRefs(&stepSpec, priorJobs); err != nil {
		s.setWorkflowFailed(wf.ID, fmt.Sprintf("step %d resolve refs: %s", step, err.Error()))
		return
	}
	req := stepToRequest(stepSpec, wf.ID, step, attempt, wf.CallerID)
	// Record step.started BEFORE Submit (see startNextStep): a fast step can finish
	// and record workflow.terminal before Submit returns, inverting event order.
	s.recordWorkflowEvent(wf.ID, EventStepStarted, map[string]any{
		"step": step, "attempt": attempt, "job_id": req.RequestID,
	})
	if _, err := s.Submit(req); err != nil {
		s.setWorkflowFailed(wf.ID, "submit step: "+err.Error())
		return
	}
}

// setWorkflowDone marks a workflow done and records the terminal event. The caller
// has already won the AdvanceStep for the final step, so this runs exactly once.
//
// The terminal event is recorded BEFORE the status flip (mirroring finish's job
// terminal ordering): a watcher polling for status!=running could otherwise observe
// done and read the event log BEFORE this terminal row lands, missing the terminal
// frame. Recording first reflects the already-decided outcome and closes that race.
func (s *Service) setWorkflowDone(wfID string) {
	s.recordWorkflowEvent(wfID, EventWorkflowTerminal, map[string]any{
		"status": jobstore.WorkflowDone,
	})
	_ = s.meta.SetWorkflowStatus(wfID, jobstore.WorkflowDone, "")
}

// setWorkflowFailed marks a workflow failed with a reason and records the terminal
// event. The caller has already won the AdvanceStep (or is on the submit-source
// path), so this runs once per workflow. The terminal event is recorded BEFORE the
// status flip (see setWorkflowDone — closes the watcher-races-terminal-event gap).
func (s *Service) setWorkflowFailed(wfID, reason string) {
	s.recordWorkflowEvent(wfID, EventWorkflowTerminal, map[string]any{
		"status": jobstore.WorkflowFailed, "error": reason,
	})
	_ = s.meta.SetWorkflowStatus(wfID, jobstore.WorkflowFailed, reason)
}

// GetWorkflow returns a workflow header by id (HTTP detail/cancel paths). The
// bool is false when no such workflow exists. It is a thin pass-through to the
// metadata store so httpapi never reaches into the unexported store.
func (s *Service) GetWorkflow(id string) (jobstore.Workflow, bool, error) {
	return s.meta.GetWorkflow(id)
}

// ListWorkflows returns workflow headers, optionally filtered by status, newest
// first, capped at limit (<=0 => store default). HTTP list path.
func (s *Service) ListWorkflows(status string, limit int) ([]jobstore.Workflow, error) {
	return s.meta.ListWorkflows(status, limit)
}

// WorkflowStep is one row of a workflow's step list in the detail response:
// the 1-based index, the step name (from the job's title / spec name), the
// step-job id and its current status. job_id/status are empty/"" for a step not
// yet started. Attempt is the 1-based retry attempt of THIS step-job row (P1): a
// retried step contributes one row per attempt (each its own job_id), so the
// detail view shows the full retry history in step+attempt order.
type WorkflowStep struct {
	StepIndex int    `json:"step_index"`
	Attempt   int    `json:"attempt,omitempty"`
	Name      string `json:"name,omitempty"`
	JobID     string `json:"job_id,omitempty"`
	Status    string `json:"status,omitempty"`
}

// WorkflowSteps returns the per-step summary for a workflow's detail view, in
// step order. It reads the started step-jobs (a step not yet reached has no job
// row, so the list only contains started steps — the chain is strictly serial).
// The name is recovered from the step-job's persisted request (Title == step
// name).
func (s *Service) WorkflowSteps(wfID string) ([]WorkflowStep, error) {
	jobs, err := s.meta.ListWorkflowJobs(wfID)
	if err != nil {
		return nil, err
	}
	out := make([]WorkflowStep, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, WorkflowStep{
			StepIndex: j.StepIndex,
			Attempt:   j.Attempt,
			Name:      titleFromRequestJSON(j.RequestJSON),
			JobID:     j.ID,
			Status:    j.Status,
		})
	}
	return out, nil
}

// AdvanceRunningWorkflows is the crash-recovery sweeper body (SR304): it scans
// running workflows and re-drives advanceWorkflow for each, picking up any whose
// current step finished but whose finish-hook advance never ran (process crash /
// missed goroutine). It is幂等 (advanceWorkflow抢推进权) so re-running it alongside
// the finish hook is safe — a no-op for workflows still mid-step. It returns the
// number of running workflows it inspected (for logging). ctx cancellation stops
// the scan early (serve shutdown).
func (s *Service) AdvanceRunningWorkflows(ctx context.Context) int {
	running, err := s.meta.ListWorkflows(jobstore.WorkflowRunning, sweeperWorkflowScan)
	if err != nil {
		return 0
	}
	for _, wf := range running {
		select {
		case <-ctx.Done():
			return len(running)
		default:
		}
		s.advanceWorkflow(wf.ID)
	}
	return len(running)
}

// stepJob returns the step-job whose step_index == stepIndex (1-based), or nil. It
// returns the FIRST match in step order. For a retried step (multiple attempts at
// the same step_index) prefer stepJobAttempt to disambiguate by attempt; stepJob is
// kept for ref resolution (which reads any prior step's output, attempt-agnostic).
func stepJob(jobs []jobstore.JobRecord, stepIndex int) *jobstore.JobRecord {
	for i := range jobs {
		if jobs[i].StepIndex == stepIndex {
			return &jobs[i]
		}
	}
	return nil
}

// stepJobAttempt returns the step-job matching BOTH step_index and attempt (P1), or
// nil. It is the workflow engine's lookup for "the current (step, attempt) job":
// a retried step has several jobs at the same step_index distinguished by attempt,
// so the engine must match the二元组 to find the run it is deciding on.
//
// A persisted job whose attempt is 0 (a v1/legacy step-job created before the
// attempt column existed, OR a crash-recovery row written with the field unset) is
// treated as attempt 1 — attempt is 1-based, so 0 == "unset" == the first run. This
// keeps crash recovery of a pre-P1 workflow from spuriously starting a duplicate
// first-attempt job (which would break the一个 (step,attempt) 只一 job invariant).
func stepJobAttempt(jobs []jobstore.JobRecord, stepIndex, attempt int) *jobstore.JobRecord {
	for i := range jobs {
		ja := jobs[i].Attempt
		if ja == 0 {
			ja = 1
		}
		if jobs[i].StepIndex == stepIndex && ja == attempt {
			return &jobs[i]
		}
	}
	return nil
}

// recordWorkflowEvent appends one append-only workflow lifecycle event (P1, design
// §5.4). It mirrors recordEvent (job_events): BEST-EFFORT — a marshal failure, an
// oversized detail or a write error only logs a warning and MUST NOT affect the
// workflow's推进/terminal state. detail must not carry secrets (SR403).
func (s *Service) recordWorkflowEvent(wfID, eventType string, detail any) {
	var dj string
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil && len(b) <= maxEventDetailBytes {
			dj = string(b)
		}
	}
	if _, err := s.meta.InsertWorkflowEvent(jobstore.WorkflowEvent{
		WorkflowID: wfID,
		Type:       eventType,
		Detail:     dj,
		At:         s.nowFn().Unix(),
	}); err != nil {
		log.Printf("recordWorkflowEvent: workflow %s type %s: %v", wfID, eventType, err)
	}
}

// ListWorkflowEvents returns a workflow's append-only lifecycle events in seq order
// (P1), forwarding to the metadata store. sinceSeq > 0 returns only events after
// that cursor (the HTTP ?since incremental path).
func (s *Service) ListWorkflowEvents(wfID string, sinceSeq int64) ([]jobstore.WorkflowEvent, error) {
	return s.meta.ListWorkflowEvents(wfID, sinceSeq)
}

// scheduleRetryAdvance fires advanceWorkflow once after backoffSec seconds (an
// in-process timer), so a scheduled step retry starts its attempt+1 job promptly
// rather than waiting for the next sweeper tick. The sweeper
// (AdvanceRunningWorkflows) is the RELIABLE backstop — if this timer is lost to a
// process restart, the due retry is still picked up — so the timer is purely a
// latency optimisation and never the sole driver. A backoff of 0 schedules an
// (almost) immediate re-advance. The re-advance is fully idempotent (it re-reads
// the header and goes through AdvanceStep + the deterministic request_id).
func (s *Service) scheduleRetryAdvance(wfID string, backoffSec int) {
	if backoffSec < 0 {
		backoffSec = 0
	}
	time.AfterFunc(time.Duration(backoffSec)*time.Second, func() {
		s.advanceWorkflow(wfID)
	})
}

// CancelWorkflow stops a running workflow: it marks it cancelled (so advanceWorkflow
// never starts another step) and cancels the currently-running step's job. It is
// idempotent and a no-op for an unknown or already-terminal workflow.
//
// Order matters: set cancelled FIRST so any racing advanceWorkflow (which checks
// status==running) bails, THEN cancel the active step's job. The cancelled step
// reaching terminal will fire advanceWorkflow, but the running-status guard there
// stops it from starting the next step.
func (s *Service) CancelWorkflow(wfID string) error {
	wf, ok, err := s.meta.GetWorkflow(wfID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("unknown workflow %q", wfID)
	}
	if wf.Status != jobstore.WorkflowRunning {
		return nil // already terminal: idempotent no-op
	}

	if err := s.meta.SetWorkflowStatus(wfID, jobstore.WorkflowCancelled, ""); err != nil {
		return err
	}
	s.recordWorkflowEvent(wfID, EventWorkflowCancelled, map[string]any{
		"step": wf.CurrentStep, "attempt": wf.StepAttempt,
	})

	// Cancel the active (step, attempt) job (Cancel is a stable no-op for a terminal
	// job). Match the current attempt so a retried step cancels the live run.
	jobs, err := s.meta.ListWorkflowJobs(wfID)
	if err != nil {
		return nil // status is already cancelled; job-cancel is best-effort
	}
	if cur := stepJobAttempt(jobs, wf.CurrentStep, wf.StepAttempt); cur != nil {
		_ = s.Cancel(cur.ID)
	}
	return nil
}

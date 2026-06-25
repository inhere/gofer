package job

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

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
//     start — the sweeper starts it once due, request_id兜底 idempotent);
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

	// Decode the spec FIRST (P2): the step's FanOut decides how many jobs make up the
	// (cur,att) generation, so the engine must know it before judging "started" /
	// "terminal". A decode/bounds error wins推进权 once and fails the workflow.
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

	// P3: a workflow-type step's outcome is its CHILD workflow's outcome (D19), not a
	// fan-job census. Branch the "started? terminal? verdict?" computation; the
	// done/failed handling below (on_failure 等) is shared with the job path.
	if step.Type == stepTypeWorkflow {
		s.advanceWorkflowStep(wf, step, cur, att, jobs, spec)
		return
	}

	want := fanWant(step)    // 1 for a single-job step; FanOut for a fan-out step
	join := joinPolicy(step) // all (default) / any / quorum

	// The fan jobs of THIS (step,attempt) generation. For a single-job step (want==1)
	// this is the one job (fan_index 0); for a fan-out step it is the started fan jobs
	// (fan_index 1..want). Fewer than `want` means some fan job has not been started
	// yet (crash / partial fan start) — (re)start the generation (deterministic
	// request_id makes the already-started ones C5 no-ops).
	fanJobs := stepFanJobs(jobs, cur, att)
	if len(fanJobs) < want {
		// retry-due / crash / partial-fan path: (re)start the (cur,att) generation now.
		// startStepJob fans out `want` jobs idempotently; already-started fans are no-ops.
		s.startStepJob(wf, cur, att, jobs)
		return
	}

	// join 聚合终态判定 (P2, plan T2.2): the step is only decided once ENOUGH fan jobs
	// reached terminal for the join policy (all → every fan; any → ≥1 done; quorum →
	// >half terminal). Until then, wait — a later finish-hook / sweeper re-drive lands.
	if !fanTerminal(fanJobs, want, join) {
		return
	}
	verdict := fanVerdict(fanJobs, want, join) // StatusDone or StatusFailed (aggregated)

	switch verdict {
	case StatusDone:
		// Win推进权 for this (step,attempt) → (cur+1, 1). Only the winner advances /
		// starts the next step, so done is committed exactly once.
		won, aerr := s.meta.AdvanceStep(wfID, cur, att, cur+1, 1, 0)
		if aerr != nil || !won {
			return
		}
		// any/quorum can decide done while some fans are still in-flight (the early
		// short-circuit). Best-effort cancel those leftover running fans to free the
		// executor (E17 quota): a still-running fan otherwise finishes uselessly and its
		// finish-hook re-drive is a harmless no-op (the pointer已 moved off (cur,att)).
		// Only the AdvanceStep winner runs this, so it cancels exactly once.
		s.cancelInflightFans(fanJobs)
		if cur >= wf.TotalSteps {
			s.setWorkflowDone(wfID) // last step done -> workflow done
			return
		}
		s.startNextStep(wf, cur, jobs, spec) // resolveRefs + start step cur+1 (attempt 1)
	default: // failed (aggregated: join not satisfied)
		failStatus := fanFailStatus(fanJobs) // representative failed fan status (message)
		failExit := fanFailExitCode(fanJobs) // representative failed fan exit code (retry gate)
		switch step.OnFailure {
		case onFailureRetry:
			if att < maxAttempts(step) && retryableExit(step, failExit) {
				// 重试: (cur,att) → (cur,att+1) with backoff. Set next_step_at; DO NOT
				// start the attempt+1 job here — the sweeper starts it once due (so the
				// backoff is honoured), and the deterministic request_id keeps a racing
				// start idempotent. Win抢权 once so the schedule is committed exactly once.
				// A fan-out retry re-runs the WHOLE step (all `want` fans at att+1, plan
				// 硬约束: only-failed-fan retry left for后续).
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
			s.setWorkflowFailed(wfID, fmt.Sprintf("step %d %s after %d attempt(s)", cur, failStatus, att))
		case onFailureContinue:
			// 继续: skip the failed step, advance to the next (or finish).
			won, aerr := s.meta.AdvanceStep(wfID, cur, att, cur+1, 1, 0)
			if aerr != nil || !won {
				return
			}
			s.recordWorkflowEvent(wfID, EventStepSkipped, map[string]any{
				"step": cur, "attempt": att, "status": failStatus,
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
			s.setWorkflowFailed(wfID, fmt.Sprintf("step %d %s", cur, failStatus))
		}
	}
}

// advanceWorkflowStep advances a workflow whose CURRENT step is a workflow-type step
// (P3, D19). It is the workflow-type analogue of the fan-job census in advanceWorkflow:
// the step's outcome IS its child sub-workflow's outcome.
//   - child not yet created (crash / retry-due / partial start): (re)start it via
//     startStepJob (which routes to startSubWorkflow with the deterministic child id, so
//     a racing re-drive is idempotent), then wait.
//   - child still running: wait — its terminal transition will fire the parent's
//     advanceWorkflow again (setWorkflowDone/Failed parent hook), AND the sweeper is the
//     backstop if that hook is lost.
//   - child terminal: done → advance/next-step (shared with the job path); failed/
//     cancelled → on_failure (fail/continue/retry), identical handling to the job path.
//
// The done/failed handling re-implements the SAME on_failure semantics as the job path
// (AdvanceStep抢权 + retry/continue/fail), keeping the幂等 invariant (一个 (step,attempt)
// 状态转移绝不执行两次). cur/att are the captured 1-based pointer二元组; jobs/spec are the
// already-read header projections (jobs feed startNextStep's ref resolution).
func (s *Service) advanceWorkflowStep(wf jobstore.Workflow, step StepSpec, cur, att int, jobs []jobstore.JobRecord, spec WorkflowSpec) {
	wfID := wf.ID
	child, ok, err := s.meta.FindChildWorkflow(wfID, cur)
	if err != nil {
		return // transient store error: the sweeper re-drives next tick
	}
	// Not started yet, OR a stale child from a PRIOR attempt (the current attempt's child
	// uses childWorkflowID(...,att); a retried step needs a fresh child). (Re)start the
	// current (cur,att) generation — startStepJob routes a workflow-type step to
	// startSubWorkflow with the deterministic per-attempt id (idempotent re-drive).
	wantChildID := childWorkflowID(wfID, cur, att)
	if !ok || child.ID != wantChildID {
		s.startStepJob(wf, cur, att, jobs)
		return
	}
	if child.Status == jobstore.WorkflowRunning {
		return // child still in flight: wait for its terminal transition / sweeper
	}

	// Child terminal: done → step done; failed/cancelled → step failed (then on_failure).
	if child.Status == jobstore.WorkflowDone {
		won, aerr := s.meta.AdvanceStep(wfID, cur, att, cur+1, 1, 0)
		if aerr != nil || !won {
			return
		}
		if cur >= wf.TotalSteps {
			s.setWorkflowDone(wfID)
			return
		}
		s.startNextStep(wf, cur, jobs, spec)
		return
	}

	// Child failed/cancelled → the step failed. on_failure decides (shared semantics).
	failStatus := child.Status // failed / cancelled
	switch step.OnFailure {
	case onFailureRetry:
		// A workflow-type step's retry re-runs the WHOLE sub-workflow as a fresh child
		// (childWorkflowID keys on att+1). on_exit_codes does not apply to a sub-workflow
		// (no exit code), so the retry gate is just the attempt ceiling.
		if att < maxAttempts(step) {
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
			s.scheduleRetryAdvance(wfID, backoff)
			return
		}
		won, aerr := s.meta.AdvanceStep(wfID, cur, att, cur+1, 1, 0)
		if aerr != nil || !won {
			return
		}
		s.setWorkflowFailed(wfID, fmt.Sprintf("step %d sub-workflow %s after %d attempt(s)", cur, failStatus, att))
	case onFailureContinue:
		won, aerr := s.meta.AdvanceStep(wfID, cur, att, cur+1, 1, 0)
		if aerr != nil || !won {
			return
		}
		s.recordWorkflowEvent(wfID, EventStepSkipped, map[string]any{
			"step": cur, "attempt": att, "status": failStatus,
		})
		if cur >= wf.TotalSteps {
			s.setWorkflowDone(wfID)
			return
		}
		s.startNextStep(wf, cur, jobs, spec)
	default: // "" / fail: fail-fast (D17 default)
		won, aerr := s.meta.AdvanceStep(wfID, cur, att, cur+1, 1, 0)
		if aerr != nil || !won {
			return
		}
		s.setWorkflowFailed(wfID, fmt.Sprintf("step %d sub-workflow %s", cur, failStatus))
	}
}

// startNextStep resolves the next step's refs and starts its job(s) (attempt 1) after
// a step finished done (or was skipped via on_failure=continue). cur is the
// 1-based JUST-FINISHED step; the next step is spec.Steps[cur] (0-based) == step
// cur+1. A resolve failure fails the whole workflow (the advance已抢权, so this
// runs exactly once). priorJobs are the already-started step-jobs (for ref resolve).
// submitStepFan starts a single job (FanOut<=1) or fans out N (FanOut>1, P2).
func (s *Service) startNextStep(wf jobstore.Workflow, cur int, priorJobs []jobstore.JobRecord, spec WorkflowSpec) {
	next := spec.Steps[cur] // 0-based: the next step (1-based step cur+1)
	// P2 接入点：resolveRefs 把 ${steps.N.field} 替换为前序产出（含 fan-out 聚合）。替换
	// 失败 → 整条工作流 failed，不起此步（advance 已抢权，此处只跑一次）。
	if err := s.resolveRefs(&next, priorJobs); err != nil {
		s.setWorkflowFailed(wf.ID, fmt.Sprintf("step %d resolve refs: %s", cur+1, err.Error()))
		return
	}
	if err := s.submitStepFan(wf, next, cur+1, 1); err != nil {
		s.setWorkflowFailed(wf.ID, "submit step: "+err.Error())
		return
	}
}

// startStepJob (re)starts the (step, attempt) generation for the CURRENT pointer. It
// is the retry-due / crash-recovery / partial-fan entry: advanceWorkflow calls it when
// fewer than `want` fan jobs of the active (cur,att) exist (a retry set the pointer to
// att+1 but did not start the jobs, a crash lost a start, or only some fans launched).
// It resolves the step's refs and submits the full fan with deterministic request_ids
// — so even if finish-hook and sweeper race here, C5 idempotency guarantees ONE job
// per (step,attempt,fan). A resolve/submit failure fails the whole workflow. priorJobs
// feed ref resolution.
//
// NOTE: this does NOT抢 AdvanceStep — the pointer is already at (step,attempt); the
// request_ids ARE the idempotency barrier for the start. Multiple concurrent callers
// all compute the same request_ids and the unique index admits one per fan.
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
	if err := s.submitStepFan(wf, stepSpec, step, attempt); err != nil {
		s.setWorkflowFailed(wf.ID, "submit step: "+err.Error())
		return
	}
}

// submitStepFan starts the job(s) for a RESOLVED step at (step, attempt). For a
// single-job step (FanOut<=1) it submits ONE job with fan_index 0 and the P1
// request_id "<wf>:s<step>:a<att>" (identical to P1, D23 — v1/P1 idempotency keys
// unchanged). For a fan-out step (FanOut>1, P2) it submits `want` jobs fan_index
// 1..want, each with the fan-suffixed request_id "<wf>:s<step>:a<att>:f<fan>" (C5
// 幂等延续: each (step,attempt,fan) only ever starts one job). All fan jobs inherit
// caller_id → bound by the E17 per-caller quota (the burst auto-queues, never floods).
//
// Events are recorded BEFORE Submit (a fast job can finish and record
// workflow.terminal before Submit returns, inverting order): step.started for a single
// job, step.fanout (with all fan job_ids) for a fan-out generation. The step's spec is
// assumed already ref-resolved by the caller.
func (s *Service) submitStepFan(wf jobstore.Workflow, step StepSpec, stepIndex, attempt int) error {
	// P3: a workflow-type step submits an INLINE sub-workflow (D18) instead of a job.
	// The child is bound to (wf.ID, stepIndex) with a deterministic id, so a racing
	// re-start (finish-hook + sweeper) only ever creates ONE child (idempotent). The
	// parent step's terminal == the child workflow's terminal (judged in advanceWorkflow
	// via FindChildWorkflow). A sub-workflow is a black box (no ref into its inner steps,
	// design §3), so the step's own resolved fields are unused here.
	if step.Type == stepTypeWorkflow {
		return s.startSubWorkflow(wf, step, stepIndex, attempt)
	}
	want := fanWant(step)
	if want <= 1 {
		// Single-job path (v1/P1): fan_index 0, no fan request_id segment.
		req := stepToRequest(step, wf.ID, stepIndex, attempt, 0, wf.CallerID)
		s.recordWorkflowEvent(wf.ID, EventStepStarted, map[string]any{
			"step": stepIndex, "attempt": attempt, "job_id": req.RequestID,
		})
		if _, err := s.Submit(req); err != nil {
			return err
		}
		return nil
	}

	// Fan-out path (P2): record the fanout event up front with every fan's deterministic
	// request_id, then submit fan_index 1..want. A submit failure for ANY fan returns the
	// error (advance/submit-source caller fails the workflow); already-submitted fans of
	// this generation are idempotent no-ops on a re-drive (C5 request_id).
	jobIDs := make([]string, 0, want)
	reqs := make([]JobRequest, 0, want)
	for f := 1; f <= want; f++ {
		req := stepToRequest(step, wf.ID, stepIndex, attempt, f, wf.CallerID)
		reqs = append(reqs, req)
		jobIDs = append(jobIDs, req.RequestID)
	}
	s.recordWorkflowEvent(wf.ID, EventStepFanout, map[string]any{
		"step": stepIndex, "attempt": attempt, "fan_out": want,
		"join": joinPolicy(step), "job_ids": jobIDs,
	})
	for i := range reqs {
		if _, err := s.Submit(reqs[i]); err != nil {
			return err
		}
	}
	return nil
}

// startSubWorkflow submits the inline sub-workflow of a workflow-type step (P3, D19).
// The child is bound to (parent.ID, stepIndex) via SubmitWorkflowChild, which derives a
// DETERMINISTIC child id so a concurrent re-start (finish-hook + sweeper) admits only
// ONE child (a duplicate submit returns the existing child, not an error). The child
// inherits the parent's caller_id (D8 / E17 quota continuity). On submit success it
// records subworkflow.started; on failure it returns the error so the caller fails the
// parent workflow (a sub-workflow that can not start is a step failure).
func (s *Service) startSubWorkflow(parent jobstore.Workflow, step StepSpec, stepIndex, attempt int) error {
	if step.SubWorkflow == nil || len(step.SubWorkflow.Steps) == 0 {
		// Defensive: validateSubworkflow rejects this at submit, but guard the runtime path.
		return fmt.Errorf("%w: step %d type=workflow has no sub_workflow", ErrInvalidRequest, stepIndex)
	}
	child, err := s.SubmitWorkflowChild(*step.SubWorkflow, parent.CallerID, parent.ID, stepIndex, attempt)
	if err != nil {
		return err
	}
	s.recordWorkflowEvent(parent.ID, EventSubworkflowStarted, map[string]any{
		"step": stepIndex, "attempt": attempt, "child_workflow_id": child.ID,
		"total_steps": child.TotalSteps,
	})
	return nil
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

package workflow

import (
	"encoding/json"
	"fmt"

	"github.com/inhere/gofer/internal/config"
	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// SubmitWorkflow validates the spec, persists a running workflow header
// (current_step=1, total=len(steps)) and starts the FIRST step's job. The
// remaining steps are started one at a time by Advance as each prior step
// reaches a terminal state (finish hook + sweeper, all幂等).
//
// Validation: at least one step, and EVERY step must pass the same single-job
// validate() (project/agent/runner allowlist + exec gate) so a workflow never
// smuggles a step past单 job 准入 (安全要点). A rejected step fails the whole submit
// before any DB row / job is created. If step-1's Submit fails, the workflow is
// marked failed and the error returned.
func (e *Engine) SubmitWorkflow(spec Spec, callerID string) (jobstore.Workflow, error) {
	return e.submitWorkflowImpl(spec, callerID, "", 0, 0)
}

// SubmitWorkflowChild submits an inline sub-workflow bound to a parent step (P3, D19).
// It derives a DETERMINISTIC child workflow id from (parentID, parentStep, parentAtt)
// so a racing re-submit (finish hook + sweeper both re-driving the same parent step)
// only ever creates ONE child — the InsertWorkflow on the duplicate id fails (PK
// collision) and is treated as an idempotent no-op. The attempt segment lets a retried
// workflow-type step (on_failure=retry) spawn a FRESH child per attempt without
// colliding with the prior attempt's child. The child inherits the parent's caller_id
// (D8, quota continuity) and stores parent_workflow_id/parent_step_index so its
// terminal transition triggers the parent's Advance. Returns the child header
// (or the already-existing child on a duplicate submit).
func (e *Engine) SubmitWorkflowChild(spec Spec, callerID, parentID string, parentStep, parentAtt int) (jobstore.Workflow, error) {
	return e.submitWorkflowImpl(spec, callerID, parentID, parentStep, parentAtt)
}

// childWorkflowID derives the deterministic sub-workflow id for a parent step's attempt
// (P3): "<parentID>:sub:s<step>:a<att>". A single derived id per (parent, step, attempt)
// is the idempotency barrier for child submission — like the deterministic step
// request_id (⭐节 1), it guarantees a concurrent re-drive can not start the same
// sub-workflow twice (the PK collision on the second InsertWorkflow is the屏障); the
// attempt segment isolates a retried step's children.
func childWorkflowID(parentID string, parentStep, parentAtt int) string {
	return fmt.Sprintf("%s:sub:s%d:a%d", parentID, parentStep, parentAtt)
}

// submitWorkflowImpl is the shared submit body for top-level (parentID=="") and child
// (parentID!="") workflows. parentID/parentStep/parentAtt bind a sub-workflow to its
// parent step+attempt and drive a DETERMINISTIC child id (childWorkflowID) so a
// re-submit is idempotent.
func (e *Engine) submitWorkflowImpl(spec Spec, callerID, parentID string, parentStep, parentAtt int) (jobstore.Workflow, error) {
	if len(spec.Steps) == 0 {
		return jobstore.Workflow{}, fmt.Errorf("%w: workflow has no steps", job.ErrInvalidRequest)
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

	// P2: per-step fan-out/join validation. fan_out must be in [0,maxFanOut]; join must
	// be a known value and only set when fan_out>1. A v1 spec (no fan_out / no join)
	// passes unchanged (D23).
	if err := validateFanout(spec); err != nil {
		return jobstore.Workflow{}, err
	}

	cfg := e.ops.Config()

	// P3: per-step type/sub_workflow validation + RECURSIVE sub-workflow准入. A
	// workflow-type step must carry a non-empty inline sub_workflow (validated through
	// the full chain, every leaf step过单 job 准入), fan-out × workflow is rejected, and
	// nesting depth is bounded. The parent spec is depth 1; a child submit re-validates
	// at depth 1 too (its own steps are then depth 2 relative to it — the absolute depth
	// across the whole tree is enforced at the top-level submit before any child runs).
	// A v1/P1/P2 spec (no type / no sub_workflow) passes unchanged (D23).
	if err := e.validateSubworkflow(spec, cfg, 1); err != nil {
		return jobstore.Workflow{}, err
	}

	// Pre-validate every JOB step against the single-job准入 before creating anything,
	// so an invalid step (e.g. step 3) is rejected at submit time, not mid-chain. A
	// workflow-type step has no job of its own (it submits a sub-workflow), so it is
	// admitted recursively by validateSubworkflow above, not here.
	for i := range spec.Steps {
		if spec.Steps[i].Type == stepTypeWorkflow {
			continue
		}
		req := stepToRequest(spec.Steps[i], "", i+1, 1, 0, callerID)
		remote := job.IsRemoteRunner(cfg, req.Runner)
		if _, err := e.ops.Validate(cfg, req, remote); err != nil {
			return jobstore.Workflow{}, fmt.Errorf("step %d: %w", i+1, err)
		}
	}

	specJSON, err := json.Marshal(spec)
	if err != nil {
		return jobstore.Workflow{}, fmt.Errorf("marshal workflow spec: %w", err)
	}

	// A child workflow uses the deterministic id (idempotent re-submit); a top-level
	// workflow uses a fresh collision-resistant id.
	wfID := e.genWorkflowID()
	if parentID != "" {
		wfID = childWorkflowID(parentID, parentStep, parentAtt)
	}
	now := e.now().Unix()
	wf := jobstore.Workflow{
		ID:               wfID,
		Title:            spec.Title,
		Status:           jobstore.WorkflowRunning,
		CurrentStep:      1,
		StepAttempt:      1, // P1: the active step's 1-based attempt (first run == 1)
		TotalSteps:       len(spec.Steps),
		SpecJSON:         string(specJSON),
		CallerID:         callerID,
		ParentWorkflowID: parentID,
		ParentStepIndex:  parentStep,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := e.meta.InsertWorkflow(wf); err != nil {
		// A child re-submit collides on the deterministic PK: the sub-workflow already
		// exists (a racing finish-hook / sweeper re-drive). Treat as an idempotent no-op
		// and return the existing child — NOT an error (the parent advance must not fail).
		if parentID != "" {
			if got, ok, gerr := e.meta.GetWorkflow(wfID); gerr == nil && ok {
				return got, nil
			}
		}
		return jobstore.Workflow{}, err
	}
	// P1: submitted event (workflow_events timeline starts here).
	e.recordWorkflowEvent(wfID, job.EventWorkflowSubmitted, map[string]any{
		"title":       spec.Title,
		"total_steps": len(spec.Steps),
		"caller_id":   callerID,
	})

	// Start step 1 (attempt 1). On failure mark the workflow failed (fail-fast at
	// the source) and return the error; the header row stays for inspection. Step 1
	// has no prior steps so no ref resolution is needed (validateRefs rejects step-1
	// refs at submit). submitStepFan starts a single job (FanOut<=1) or fans out N
	// (FanOut>1), recording step.started / step.fanout before Submit (a fast step can
	// finish and record workflow.terminal before Submit returns, inverting order).
	if err := e.submitStepFan(wf, spec.Steps[0], 1, 1); err != nil {
		_ = e.meta.SetWorkflowStatus(wfID, jobstore.WorkflowFailed, "submit step 1: "+err.Error())
		e.recordWorkflowEvent(wfID, job.EventWorkflowTerminal, map[string]any{
			"status": jobstore.WorkflowFailed,
			"error":  "submit step 1: " + err.Error(),
		})
		return jobstore.Workflow{}, err
	}

	// Return the freshly-created header snapshot.
	got, _, gerr := e.meta.GetWorkflow(wfID)
	if gerr != nil {
		return wf, nil // best-effort: the row exists; return the in-hand copy
	}
	return got, nil
}

// stepToRequest maps a StepSpec to a job.JobRequest, binding it to its workflow +
// 1-based step index and inheriting the workflow's caller id (D8). The internal
// WorkflowID/StepIndex/Attempt/FanIndex fields are how finish knows to advance the
// chain (and which fan job this is).
//
// ⭐ 幂等核心 (P1/P2, plan ⭐节 1): every step-job carries a DETERMINISTIC request_id.
// A single-job step uses "<wfID>:s<step>:a<attempt>" (fanIndex==0, identical to P1 so
// v1/P1 idempotency keys are unchanged, D23). A fan-out job (fanIndex>=1) appends the
// fan segment: "<wfID>:s<step>:a<attempt>:f<fan>" (P2, C5 幂等延续). Re-driven through
// the C5 unique-index idempotency, this guarantees that no matter how many concurrent
// callers (finish hook + sweeper + duplicates) try to start the SAME (step,attempt,fan),
// at most ONE job is ever created. wfID=="" (the submit-time pre-validation pass)
// leaves request_id empty (no idempotency需要).
func stepToRequest(step StepSpec, wfID string, stepIndex, attempt, fanIndex int, callerID string) job.JobRequest {
	reqID := ""
	if wfID != "" {
		reqID = fmt.Sprintf("%s:s%d:a%d", wfID, stepIndex, attempt)
		if fanIndex >= 1 {
			reqID += fmt.Sprintf(":f%d", fanIndex)
		}
	}
	return job.JobRequest{
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
		FanIndex:   fanIndex,
		RequestID:  reqID,
	}
}

// validateRetry checks each step's失败策略 at submit time (P1). on_failure must be
// one of the known values; a step with on_failure=="retry" MUST carry a retry
// block with max_attempts in [1,maxRetryAttempts]; a non-retry step MUST NOT carry
// a retry block (caught early so a misconfigured policy never starts). A v1 spec
// (on_failure=="" and retry==nil) passes unchanged (D23).
func validateRetry(spec Spec) error {
	for i := range spec.Steps {
		stepNo := i + 1
		st := spec.Steps[i]
		switch st.OnFailure {
		case "", onFailureFail, onFailureContinue:
			if st.Retry != nil {
				return fmt.Errorf("%w: step %d has a retry block but on_failure=%q (retry only applies to on_failure=retry)", job.ErrInvalidRequest, stepNo, st.OnFailure)
			}
		case onFailureRetry:
			if st.Retry == nil {
				return fmt.Errorf("%w: step %d on_failure=retry requires a retry block", job.ErrInvalidRequest, stepNo)
			}
			if st.Retry.MaxAttempts < 1 {
				return fmt.Errorf("%w: step %d retry.max_attempts must be >= 1", job.ErrInvalidRequest, stepNo)
			}
			if st.Retry.MaxAttempts > maxRetryAttempts {
				return fmt.Errorf("%w: step %d retry.max_attempts %d exceeds the limit %d", job.ErrInvalidRequest, stepNo, st.Retry.MaxAttempts, maxRetryAttempts)
			}
		default:
			return fmt.Errorf("%w: step %d has unknown on_failure %q (want fail/continue/retry)", job.ErrInvalidRequest, stepNo, st.OnFailure)
		}
	}
	return nil
}

// validateFanout checks each step's fan-out/join configuration at submit time (P2,
// design §5.1). fan_out must be in [0,maxFanOut] (negative is nonsense; over the cap
// would flood the executor). join must be a known value (all/any/quorum) and may ONLY
// be set on a fan-out step (fan_out>1) — a join on a single-job step is a misconfig
// caught early. A v1 spec (fan_out==0/1 and join=="") passes unchanged (D23).
func validateFanout(spec Spec) error {
	for i := range spec.Steps {
		stepNo := i + 1
		st := spec.Steps[i]
		if st.FanOut < 0 {
			return fmt.Errorf("%w: step %d fan_out must be >= 0", job.ErrInvalidRequest, stepNo)
		}
		if st.FanOut > maxFanOut {
			return fmt.Errorf("%w: step %d fan_out %d exceeds the limit %d", job.ErrInvalidRequest, stepNo, st.FanOut, maxFanOut)
		}
		switch st.Join {
		case "", joinAll, joinAny, joinQuorum:
			// known (or default-empty) — fall through to the fan-out coupling check.
		default:
			return fmt.Errorf("%w: step %d has unknown join %q (want all/any/quorum)", job.ErrInvalidRequest, stepNo, st.Join)
		}
		// join only applies to a real fan-out (fan_out>1). A join on a single-job step
		// is a misconfiguration (the join would never aggregate more than one job).
		if st.Join != "" && st.FanOut <= 1 {
			return fmt.Errorf("%w: step %d sets join=%q but fan_out=%d (join only applies to fan_out>1)", job.ErrInvalidRequest, stepNo, st.Join, st.FanOut)
		}
	}
	return nil
}

// validateSubworkflow checks each step's type/sub_workflow at submit time and
// RECURSIVELY validates an inline sub-workflow (P3, design §5.1, plan T3.1), so a
// nested step never smuggles past the same准入 a top-level step faces (§9 安全):
//   - type must be "" / "job" / "workflow" (unknown rejected);
//   - a job/"" step must NOT carry a sub_workflow;
//   - a workflow step MUST carry a non-empty sub_workflow (steps非空);
//   - a workflow step is mutually exclusive with fan-out (fan_out>1 rejected) — fan ×
//     workflow is unsupported (硬约束), and join makes no sense on a single sub-wf;
//   - the sub-workflow is validated recursively: validateRefs / validateRetry /
//     validateFanout / validateSubworkflow + every leaf step过单 job 准入 (cfg);
//   - nesting depth is bounded by maxWorkflowDepth (depth 1 == this top-level spec).
//
// cfg is threaded so the recursive single-job admission (e.validate) uses the same
// project/agent/runner allowlist + exec gate as the top-level pre-validation pass. A
// v1/P1/P2 spec (no type / no sub_workflow) passes unchanged at depth 1 (D23).
func (e *Engine) validateSubworkflow(spec Spec, cfg *config.Config, depth int) error {
	for i := range spec.Steps {
		stepNo := i + 1
		st := spec.Steps[i]
		switch st.Type {
		case "", stepTypeJob:
			if st.SubWorkflow != nil {
				return fmt.Errorf("%w: step %d is type=%q but carries a sub_workflow (sub_workflow only applies to type=workflow)", job.ErrInvalidRequest, stepNo, st.Type)
			}
		case stepTypeWorkflow:
			if st.SubWorkflow == nil || len(st.SubWorkflow.Steps) == 0 {
				return fmt.Errorf("%w: step %d type=workflow requires a non-empty sub_workflow (steps)", job.ErrInvalidRequest, stepNo)
			}
			// fan-out × workflow is mutually exclusive (硬约束): a workflow step is a single
			// sub-workflow, never a parallel burst, so FanOut>1 (or any join) is a misconfig.
			if st.FanOut > 1 {
				return fmt.Errorf("%w: step %d combines type=workflow with fan_out=%d (fan-out and sub-workflow are mutually exclusive)", job.ErrInvalidRequest, stepNo, st.FanOut)
			}
			// Depth guard: this top-level spec is `depth`; its sub-workflow steps are depth+1.
			if depth+1 > maxWorkflowDepth {
				return fmt.Errorf("%w: step %d sub_workflow nests to depth %d exceeding the limit %d", job.ErrInvalidRequest, stepNo, depth+1, maxWorkflowDepth)
			}
			sub := *st.SubWorkflow
			// Recursive准入: the sub-workflow faces the FULL submit validation chain so a
			// nested step can not bypass refs/retry/fanout checks OR the single-job准入.
			if err := validateRefs(sub); err != nil {
				return fmt.Errorf("step %d sub_workflow: %w", stepNo, err)
			}
			if err := validateRetry(sub); err != nil {
				return fmt.Errorf("step %d sub_workflow: %w", stepNo, err)
			}
			if err := validateFanout(sub); err != nil {
				return fmt.Errorf("step %d sub_workflow: %w", stepNo, err)
			}
			if err := e.validateSubworkflow(sub, cfg, depth+1); err != nil {
				return fmt.Errorf("step %d sub_workflow: %w", stepNo, err)
			}
			// Every LEAF (job-type) step of the sub-workflow passes the single-job准入.
			// A workflow-type sub-step is admitted by the recursive call above, not here.
			for j := range sub.Steps {
				if sub.Steps[j].Type == stepTypeWorkflow {
					continue
				}
				req := stepToRequest(sub.Steps[j], "", j+1, 1, 0, "")
				remote := job.IsRemoteRunner(cfg, req.Runner)
				if _, err := e.ops.Validate(cfg, req, remote); err != nil {
					return fmt.Errorf("step %d sub_workflow step %d: %w", stepNo, j+1, err)
				}
			}
		default:
			return fmt.Errorf("%w: step %d has unknown type %q (want job/workflow)", job.ErrInvalidRequest, stepNo, st.Type)
		}
	}
	return nil
}

// genWorkflowID returns a collision-resistant workflow id, "wf-" prefixed so it
// is visually distinct from a job id (which shares the same time+random scheme).
func (e *Engine) genWorkflowID() string {
	return "wf-" + e.now().Format(job.JobIDLayout) + "-" + job.RandomSuffix()
}

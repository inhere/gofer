package job

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"

	"github.com/inhere/gofer/internal/config"
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
	// FanOut is the per-step parallelism (P2, design §5.1, D14): when >1, the step
	// starts FanOut jobs in parallel (fan_index=1..FanOut) sharing (step_index,attempt),
	// aggregated by Join before the chain advances. 0/1 == a single job == v1 behaviour
	// (D23). Capped at maxFanOut (validateFanout) so a misconfigured spec can not flood
	// the executor; the fan jobs inherit caller_id and are E17 per-caller quota-bound.
	FanOut int `json:"fan_out,omitempty" yaml:"fan_out,omitempty"`
	// Join is the fan-out aggregation policy (P2, design §5.1, D15): "all" (default)
	// succeeds only if every fan job is done; "any" succeeds as soon as one is done;
	// "quorum" succeeds when more than half are done. Empty == "all". Only meaningful
	// when FanOut>1 (validateFanout rejects join on a non-fan step).
	Join string `json:"join,omitempty" yaml:"join,omitempty"`
	// Type is the step kind (P3, design §5.1, D18): "" / "job" (default) runs a single
	// agent/exec job; "workflow" instead submits an INLINE sub-workflow (SubWorkflow)
	// whose terminal state decides this step's outcome. Empty == "job" == v1 behaviour
	// (D23). A workflow-type step is mutually exclusive with fan-out (validateSubworkflow).
	Type string `json:"type,omitempty" yaml:"type,omitempty"`
	// SubWorkflow is the inline child-workflow definition for a Type=="workflow" step
	// (P3, design §5.1, D18). Required (non-empty steps) when Type=="workflow"; must be
	// absent for a job-type step. Each of its steps passes the SAME single-job准入
	// recursively (validateSubworkflow), so nesting never smuggles a step past the
	// allowlist/exec gate (§9 安全). nil for a v1/job step (D23).
	SubWorkflow *WorkflowSpec `json:"sub_workflow,omitempty" yaml:"sub_workflow,omitempty"`
	// File is a CLI-ONLY md-per-step reference (P4 / T4.2): when set in a workflow yaml
	// file, the CLI loads the named md file (frontmatter→step params, body→prompt) and
	// expands it INTO the other fields before submit. It is `json:"-"` so it never
	// crosses the wire to the server (the server only ever sees the expanded fields), and
	// is absent from a v1 spec (D23). Resolved relative to the workflow file's directory.
	File string `json:"-" yaml:"file,omitempty"`
}

// RetryPolicy bounds per-step (and per-job, E24) retry on failure (P1, design
// §5.1, D16). MaxAttempts counts the FIRST run as attempt 1, so MaxAttempts==3
// means up to 2 retries. BackoffSec is the退避表 indexed by the just-failed
// attempt (defaults to the SR606 table when empty). OnExitCodes, when non-empty,
// restricts retry to those exit codes (empty == retry on any non-zero exit /
// timeout / failure, see retryableExit).
type RetryPolicy struct {
	MaxAttempts int   `json:"max_attempts" yaml:"max_attempts"`                       // >=1 (includes the first run)
	BackoffSec  []int `json:"backoff_sec,omitempty" yaml:"backoff_sec,omitempty"`     // 默认接 SR606 [30,120,300,900,3600]
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

// join* are the known StepSpec.Join values (P2, design §5.1, D15). "" is treated as
// joinAll (the default) so a fan-out step without an explicit join aggregates as all.
const (
	joinAll    = "all"
	joinAny    = "any"
	joinQuorum = "quorum"
)

// maxFanOut caps StepSpec.FanOut so a misconfigured spec can not spawn an unbounded
// burst of step-jobs (defence in depth on top of the E17 per-caller quota that
// already serialises/queues a large fan). 32 is the design ceiling (plan T2.1).
const maxFanOut = 32

// stepType* are the known StepSpec.Type values (P3, design §5.1, D18). "" is treated
// as stepTypeJob (a single job, the v1 path) so a v1/P1/P2 spec maps to job without
// change (D23).
const (
	stepTypeJob      = "job"
	stepTypeWorkflow = "workflow"
)

// maxWorkflowDepth caps how deeply sub-workflows may nest (P3, plan T3.1硬约束): a
// top-level workflow is depth 1, its sub-workflow steps are depth 2, theirs depth 3.
// A spec nesting beyond this is rejected at submit (validateSubworkflow) so a
// pathological / runaway recursion can never be admitted. 3 is the plan ceiling.
const maxWorkflowDepth = 3

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
	return s.submitWorkflowImpl(spec, callerID, "", 0, 0)
}

// SubmitWorkflowChild submits an inline sub-workflow bound to a parent step (P3, D19).
// It derives a DETERMINISTIC child workflow id from (parentID, parentStep, parentAtt)
// so a racing re-submit (finish hook + sweeper both re-driving the same parent step)
// only ever creates ONE child — the InsertWorkflow on the duplicate id fails (PK
// collision) and is treated as an idempotent no-op. The attempt segment lets a retried
// workflow-type step (on_failure=retry) spawn a FRESH child per attempt without
// colliding with the prior attempt's child. The child inherits the parent's caller_id
// (D8, quota continuity) and stores parent_workflow_id/parent_step_index so its
// terminal transition triggers the parent's advanceWorkflow. Returns the child header
// (or the already-existing child on a duplicate submit).
func (s *Service) SubmitWorkflowChild(spec WorkflowSpec, callerID, parentID string, parentStep, parentAtt int) (jobstore.Workflow, error) {
	return s.submitWorkflowImpl(spec, callerID, parentID, parentStep, parentAtt)
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
func (s *Service) submitWorkflowImpl(spec WorkflowSpec, callerID, parentID string, parentStep, parentAtt int) (jobstore.Workflow, error) {
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

	// P2: per-step fan-out/join validation. fan_out must be in [0,maxFanOut]; join must
	// be a known value and only set when fan_out>1. A v1 spec (no fan_out / no join)
	// passes unchanged (D23).
	if err := validateFanout(spec); err != nil {
		return jobstore.Workflow{}, err
	}

	cfg := s.config()

	// P3: per-step type/sub_workflow validation + RECURSIVE sub-workflow准入. A
	// workflow-type step must carry a non-empty inline sub_workflow (validated through
	// the full chain, every leaf step过单 job 准入), fan-out × workflow is rejected, and
	// nesting depth is bounded. The parent spec is depth 1; a child submit re-validates
	// at depth 1 too (its own steps are then depth 2 relative to it — the absolute depth
	// across the whole tree is enforced at the top-level submit before any child runs).
	// A v1/P1/P2 spec (no type / no sub_workflow) passes unchanged (D23).
	if err := s.validateSubworkflow(spec, cfg, 1); err != nil {
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
		remote := isRemoteRunner(cfg, req.Runner)
		if _, err := s.validate(cfg, req, remote); err != nil {
			return jobstore.Workflow{}, fmt.Errorf("step %d: %w", i+1, err)
		}
	}

	specJSON, err := json.Marshal(spec)
	if err != nil {
		return jobstore.Workflow{}, fmt.Errorf("marshal workflow spec: %w", err)
	}

	// A child workflow uses the deterministic id (idempotent re-submit); a top-level
	// workflow uses a fresh collision-resistant id.
	wfID := s.genWorkflowID()
	if parentID != "" {
		wfID = childWorkflowID(parentID, parentStep, parentAtt)
	}
	now := s.nowFn().Unix()
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
	if err := s.meta.InsertWorkflow(wf); err != nil {
		// A child re-submit collides on the deterministic PK: the sub-workflow already
		// exists (a racing finish-hook / sweeper re-drive). Treat as an idempotent no-op
		// and return the existing child — NOT an error (the parent advance must not fail).
		if parentID != "" {
			if got, ok, gerr := s.meta.GetWorkflow(wfID); gerr == nil && ok {
				return got, nil
			}
		}
		return jobstore.Workflow{}, err
	}
	// P1: submitted event (workflow_events timeline starts here).
	s.recordWorkflowEvent(wfID, EventWorkflowSubmitted, map[string]any{
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
	if err := s.submitStepFan(wf, spec.Steps[0], 1, 1); err != nil {
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
func stepToRequest(step StepSpec, wfID string, stepIndex, attempt, fanIndex int, callerID string) JobRequest {
	reqID := ""
	if wfID != "" {
		reqID = fmt.Sprintf("%s:s%d:a%d", wfID, stepIndex, attempt)
		if fanIndex >= 1 {
			reqID += fmt.Sprintf(":f%d", fanIndex)
		}
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
		FanIndex:   fanIndex,
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

// validateFanout checks each step's fan-out/join configuration at submit time (P2,
// design §5.1). fan_out must be in [0,maxFanOut] (negative is nonsense; over the cap
// would flood the executor). join must be a known value (all/any/quorum) and may ONLY
// be set on a fan-out step (fan_out>1) — a join on a single-job step is a misconfig
// caught early. A v1 spec (fan_out==0/1 and join=="") passes unchanged (D23).
func validateFanout(spec WorkflowSpec) error {
	for i := range spec.Steps {
		stepNo := i + 1
		st := spec.Steps[i]
		if st.FanOut < 0 {
			return fmt.Errorf("%w: step %d fan_out must be >= 0", ErrInvalidRequest, stepNo)
		}
		if st.FanOut > maxFanOut {
			return fmt.Errorf("%w: step %d fan_out %d exceeds the limit %d", ErrInvalidRequest, stepNo, st.FanOut, maxFanOut)
		}
		switch st.Join {
		case "", joinAll, joinAny, joinQuorum:
			// known (or default-empty) — fall through to the fan-out coupling check.
		default:
			return fmt.Errorf("%w: step %d has unknown join %q (want all/any/quorum)", ErrInvalidRequest, stepNo, st.Join)
		}
		// join only applies to a real fan-out (fan_out>1). A join on a single-job step
		// is a misconfiguration (the join would never aggregate more than one job).
		if st.Join != "" && st.FanOut <= 1 {
			return fmt.Errorf("%w: step %d sets join=%q but fan_out=%d (join only applies to fan_out>1)", ErrInvalidRequest, stepNo, st.Join, st.FanOut)
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
// cfg is threaded so the recursive single-job admission (s.validate) uses the same
// project/agent/runner allowlist + exec gate as the top-level pre-validation pass. A
// v1/P1/P2 spec (no type / no sub_workflow) passes unchanged at depth 1 (D23).
func (s *Service) validateSubworkflow(spec WorkflowSpec, cfg *config.Config, depth int) error {
	for i := range spec.Steps {
		stepNo := i + 1
		st := spec.Steps[i]
		switch st.Type {
		case "", stepTypeJob:
			if st.SubWorkflow != nil {
				return fmt.Errorf("%w: step %d is type=%q but carries a sub_workflow (sub_workflow only applies to type=workflow)", ErrInvalidRequest, stepNo, st.Type)
			}
		case stepTypeWorkflow:
			if st.SubWorkflow == nil || len(st.SubWorkflow.Steps) == 0 {
				return fmt.Errorf("%w: step %d type=workflow requires a non-empty sub_workflow (steps)", ErrInvalidRequest, stepNo)
			}
			// fan-out × workflow is mutually exclusive (硬约束): a workflow step is a single
			// sub-workflow, never a parallel burst, so FanOut>1 (or any join) is a misconfig.
			if st.FanOut > 1 {
				return fmt.Errorf("%w: step %d combines type=workflow with fan_out=%d (fan-out and sub-workflow are mutually exclusive)", ErrInvalidRequest, stepNo, st.FanOut)
			}
			// Depth guard: this top-level spec is `depth`; its sub-workflow steps are depth+1.
			if depth+1 > maxWorkflowDepth {
				return fmt.Errorf("%w: step %d sub_workflow nests to depth %d exceeding the limit %d", ErrInvalidRequest, stepNo, depth+1, maxWorkflowDepth)
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
			if err := s.validateSubworkflow(sub, cfg, depth+1); err != nil {
				return fmt.Errorf("step %d sub_workflow: %w", stepNo, err)
			}
			// Every LEAF (job-type) step of the sub-workflow passes the single-job准入.
			// A workflow-type sub-step is admitted by the recursive call above, not here.
			for j := range sub.Steps {
				if sub.Steps[j].Type == stepTypeWorkflow {
					continue
				}
				req := stepToRequest(sub.Steps[j], "", j+1, 1, 0, "")
				remote := isRemoteRunner(cfg, req.Runner)
				if _, err := s.validate(cfg, req, remote); err != nil {
					return fmt.Errorf("step %d sub_workflow step %d: %w", stepNo, j+1, err)
				}
			}
		default:
			return fmt.Errorf("%w: step %d has unknown type %q (want job/workflow)", ErrInvalidRequest, stepNo, st.Type)
		}
	}
	return nil
}

// fanWant returns the effective parallelism of a step: max(1, FanOut). A fan_out of
// 0/1 is a single job (the v1 path); fan_out>1 is the configured N.
func fanWant(step StepSpec) int {
	if step.FanOut > 1 {
		return step.FanOut
	}
	return 1
}

// joinPolicy returns the step's effective join, defaulting an empty Join to joinAll
// (D15). Centralised so fanTerminal/fanVerdict share one default.
func joinPolicy(step StepSpec) string {
	if step.Join == "" {
		return joinAll
	}
	return step.Join
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
	// P4/T4.3: count the terminal + observe the whole-chain duration (nil-safe).
	s.recordWorkflowTerminalMetric(wfID, jobstore.WorkflowDone)
	// P3: if this is a sub-workflow, its terminal transition unlocks the parent step.
	s.triggerParentAdvance(wfID)
}

// recordWorkflowTerminalMetric counts one workflow terminal + observes its
// submit→terminal duration through the MetricsSink (P4/T4.3, design §9). It is
// nil-safe and BEST-EFFORT (a store read failure only skips the duration sample, never
// affects the terminal transition). Duration is now−created_at, clamped at 0 against
// clock skew. Called from setWorkflowDone/setWorkflowFailed (the AdvanceStep winner, so
// it runs once per terminal) and the cancel path.
func (s *Service) recordWorkflowTerminalMetric(wfID, status string) {
	if s.metrics == nil {
		return
	}
	dur := 0.0
	if wf, ok, err := s.meta.GetWorkflow(wfID); err == nil && ok {
		dur = float64(s.nowFn().Unix() - wf.CreatedAt)
		if dur < 0 {
			dur = 0
		}
	}
	s.metrics.WorkflowTerminal(status, dur)
}

// triggerParentAdvance fires the parent's advanceWorkflow when wfID is a sub-workflow
// (ParentWorkflowID != "") that just reached a terminal state (P3, D19). It mirrors the
// finish hook's `go advanceWorkflow`: ASYNC + 幂等 (the parent's AdvanceStep抢权 + the
// deterministic child id keep a racing trigger and the sweeper's backstop safe). A
// top-level workflow (no parent) is a no-op. Best-effort: a store read error or a
// missing parent only skips the prompt re-drive — the sweeper still re-drives the
// running parent on its next tick (子 wf 终态但父 advance 漏触发的兜底).
func (s *Service) triggerParentAdvance(wfID string) {
	wf, ok, err := s.meta.GetWorkflow(wfID)
	if err != nil || !ok || wf.ParentWorkflowID == "" {
		return
	}
	go s.advanceWorkflow(wf.ParentWorkflowID)
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
	// P4/T4.3: count the terminal + observe the whole-chain duration (nil-safe).
	s.recordWorkflowTerminalMetric(wfID, jobstore.WorkflowFailed)
	// P3: if this is a sub-workflow, its terminal transition unlocks the parent step
	// (which then sees a failed child → step failed → on_failure).
	s.triggerParentAdvance(wfID)
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
// detail view shows the full retry history in step+attempt order. FanIndex is the
// 1-based fan-out parallel index (P2): a fan-out step contributes one row per fan
// job (each its own job_id), 0 for a single-job step.
type WorkflowStep struct {
	StepIndex int    `json:"step_index"`
	Attempt   int    `json:"attempt,omitempty"`
	FanIndex  int    `json:"fan_index,omitempty"`
	Name      string `json:"name,omitempty"`
	JobID     string `json:"job_id,omitempty"`
	Status    string `json:"status,omitempty"`
	// Type/ChildWorkflowID surface a Type=="workflow" sub-workflow step (P3 UI fix):
	// such a step runs no step-job, so it is absent from the job-derived rows — these
	// fields let the chain show it and link into the child workflow's detail.
	Type            string `json:"type,omitempty"`
	ChildWorkflowID string `json:"child_workflow_id,omitempty"`
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
			FanIndex:  j.FanIndex,
			Name:      titleFromRequestJSON(j.RequestJSON),
			JobID:     j.ID,
			Status:    j.Status,
		})
	}
	// P3 UI fix: workflow-type steps run NO step-job, so they are missing from the
	// job-derived rows above (the web/CLI chain only saw job steps, hiding the whole
	// sub-workflow). Surface each such step from the spec + its child workflow so the
	// chain shows the step and can link into the child's detail.
	if wf, ok, gerr := s.meta.GetWorkflow(wfID); gerr == nil && ok {
		var spec WorkflowSpec
		if json.Unmarshal([]byte(wf.SpecJSON), &spec) == nil {
			for i := range spec.Steps {
				if spec.Steps[i].Type != stepTypeWorkflow {
					continue
				}
				row := WorkflowStep{StepIndex: i + 1, Name: spec.Steps[i].Name, Type: stepTypeWorkflow}
				// The child may not exist yet (step not reached) — then the row is a
				// pending placeholder; once started, link + status come from the child.
				if child, found, cerr := s.meta.FindChildWorkflow(wfID, i+1); cerr == nil && found {
					row.ChildWorkflowID = child.ID
					row.Status = child.Status
				}
				out = append(out, row)
			}
		}
	}
	// Merge the appended workflow rows into step order (job rows are already
	// step-ordered; stable keeps fan/attempt order within a step intact).
	sort.SliceStable(out, func(a, b int) bool { return out[a].StepIndex < out[b].StepIndex })
	return out, nil
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

// stepFanJobs returns ALL fan jobs of a (step_index, attempt) generation (P2): for a
// single-job step it is the one job (fan_index 0); for a fan-out step it is every
// started fan (fan_index 1..N). attempt is normalised like stepJobAttempt (a 0 attempt
// from a v1/legacy/unset row counts as 1) so the engine matches the二元组 unambiguously.
// The returned slice points into jobs (no copy); callers read-only.
func stepFanJobs(jobs []jobstore.JobRecord, stepIndex, attempt int) []*jobstore.JobRecord {
	out := make([]*jobstore.JobRecord, 0, 4)
	for i := range jobs {
		ja := jobs[i].Attempt
		if ja == 0 {
			ja = 1
		}
		if jobs[i].StepIndex == stepIndex && ja == attempt {
			out = append(out, &jobs[i])
		}
	}
	return out
}

// fanCounts tallies a generation's fan jobs into (done, terminal): done is fan jobs
// with StatusDone; terminal is fan jobs in ANY terminal state (done/failed/timeout/
// cancelled). Shared by fanTerminal and fanVerdict so they agree on the same census.
func fanCounts(fanJobs []*jobstore.JobRecord) (done, terminal int) {
	for _, j := range fanJobs {
		if j.Status == StatusDone {
			done++
		}
		if isTerminal(j.Status) {
			terminal++
		}
	}
	return done, terminal
}

// fanTerminal reports whether a fan-out step's (step,attempt) generation has reached a
// DECIDABLE state under its join policy (P2, design §5.1, D15), where `want` is the
// configured parallelism (max 1):
//   - all:    every fan must be terminal (then verdict = all-done?). Until then, wait.
//   - any:    decidable as soon as ONE fan is done (success short-circuit), OR every
//     fan is terminal (then it is an all-failed → failed). A still-running fan
//     with no done yet means "maybe still succeeds" → wait.
//   - quorum: decidable once a majority (> want/2) are done (success short-circuit) OR
//     enough have failed that a quorum of done is impossible (→ failed) OR all
//     terminal. Otherwise wait.
//
// In all cases, once every fan is terminal the generation is trivially decidable (the
// `terminal == want` guard), so a generation never hangs.
func fanTerminal(fanJobs []*jobstore.JobRecord, want int, join string) bool {
	done, terminal := fanCounts(fanJobs)
	if terminal >= want {
		return true // every fan terminal: always decidable (success or failure)
	}
	switch join {
	case joinAny:
		return done >= 1 // first done short-circuits success
	case joinQuorum:
		need := want/2 + 1 // strict majority of `want`
		if done >= need {
			return true // quorum of done reached: success short-circuit
		}
		failed := terminal - done
		// If too many have already failed for a done-quorum to remain possible, decide
		// now (failure) rather than wait for the rest.
		return want-failed < need
	default: // all
		return false // all needs every fan terminal (handled by the guard above)
	}
}

// fanVerdict aggregates a DECIDABLE fan-out generation to StatusDone or StatusFailed
// under its join policy (P2, design §5.1, D15): all → done iff every fan is done;
// any → done iff ≥1 fan is done; quorum → done iff a strict majority (> want/2) of
// fans are done. Anything else is failed. `want` is the configured parallelism.
// Precondition: fanTerminal(fanJobs, want, join) == true.
func fanVerdict(fanJobs []*jobstore.JobRecord, want int, join string) string {
	done, _ := fanCounts(fanJobs)
	switch join {
	case joinAny:
		if done >= 1 {
			return StatusDone
		}
	case joinQuorum:
		if done >= want/2+1 {
			return StatusDone
		}
	default: // all
		if done >= want {
			return StatusDone
		}
	}
	return StatusFailed
}

// fanFailStatus returns a representative NON-done terminal status among a generation's
// fan jobs (failed/timeout/cancelled), for the failure message / skipped event. Falls
// back to StatusFailed when none is found (defensive; the verdict was failed).
func fanFailStatus(fanJobs []*jobstore.JobRecord) string {
	for _, j := range fanJobs {
		if isTerminal(j.Status) && j.Status != StatusDone {
			return j.Status
		}
	}
	return StatusFailed
}

// fanFailExitCode returns a representative exit code of a NON-done terminal fan job,
// used to gate on_exit_codes retry (a fan-out step is retried if ANY failed fan is
// retryable). 0 when no failed fan is found (defensive).
func fanFailExitCode(fanJobs []*jobstore.JobRecord) int {
	for _, j := range fanJobs {
		if isTerminal(j.Status) && j.Status != StatusDone {
			return j.ExitCode
		}
	}
	return 0
}

// cancelInflightFans best-effort cancels every NON-terminal fan job of a generation
// (P2): used by the any/quorum success short-circuit to stop the leftover running fans
// once the step is already decided done. Cancel is a stable no-op for an already-
// terminal job, so this is safe to call on the whole generation. Errors are ignored
// (the workflow has already advanced; a stray running fan finishing later is harmless).
func (s *Service) cancelInflightFans(fanJobs []*jobstore.JobRecord) {
	for _, j := range fanJobs {
		if !isTerminal(j.Status) {
			_ = s.Cancel(j.ID)
		}
	}
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
	// P4/T4.3: count the cancelled terminal + observe the duration (nil-safe). We have
	// the header in hand (wf) so compute the duration from its created_at directly.
	if s.metrics != nil {
		dur := float64(s.nowFn().Unix() - wf.CreatedAt)
		if dur < 0 {
			dur = 0
		}
		s.metrics.WorkflowTerminal(jobstore.WorkflowCancelled, dur)
	}

	// Cancel the active (step, attempt) generation's job(s) (Cancel is a stable no-op
	// for a terminal job). Match the current attempt so a retried step cancels the live
	// run; for a fan-out step this cancels EVERY in-flight fan of the generation (P2).
	jobs, err := s.meta.ListWorkflowJobs(wfID)
	if err != nil {
		// P3: even on the best-effort job-cancel error path, a cancelled sub-workflow must
		// still unlock its parent step (parent sees cancelled → failed → on_failure).
		s.triggerParentAdvance(wfID)
		return nil // status is already cancelled; job-cancel is best-effort
	}
	for _, j := range stepFanJobs(jobs, wf.CurrentStep, wf.StepAttempt) {
		_ = s.Cancel(j.ID)
	}
	// P3: a cancelled sub-workflow unlocks its parent step (parent sees cancelled →
	// step failed → on_failure). A top-level workflow is a no-op (no parent).
	s.triggerParentAdvance(wfID)
	return nil
}

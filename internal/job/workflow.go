package job

import (
	"fmt"

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

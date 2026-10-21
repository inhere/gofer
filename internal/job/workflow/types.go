package workflow

import (
	job "github.com/inhere/gofer/internal/job"
	gotemplate "github.com/inhere/gofer/internal/template"
)

// sweeperWorkflowScan caps how many running workflows the sweeper inspects per
// tick. A workflow只 has one active step, so this is a generous ceiling.
const sweeperWorkflowScan = 500

// StepSpec is one step of a工作流(job 链): a普通 job request expressed declaratively.
// It carries the same fields a single job.JobRequest needs (project/agent/runner +
// prompt/cmd/cwd/timeout/tags). P1 runs each step independently (no cross-step
// data); P2 adds ${steps.N.field} references resolved before the step starts.
//
// P1 adds per-step失败策略 (design §5.1, D17): OnFailure ∈ ""/fail (v1 fail-fast)
// | continue (skip a failed step) | retry (re-run with backoff, bounded by Retry).
// Both fields are omitempty with a zero value == v1 behaviour (OnFailure==""=fail,
// Retry==nil=no retry), so v1 specs deserialize and run unchanged (D23).
type StepSpec struct {
	Name       string `json:"name,omitempty" yaml:"name,omitempty"`
	ProjectKey string `json:"project_key" yaml:"project_key"`
	Agent      string `json:"agent" yaml:"agent"`
	Runner     string `json:"runner" yaml:"runner"`
	// Agents fans one step out across an ordered list of agent keys. It is mutually
	// exclusive with Fan and takes precedence over legacy numeric FanOut when set.
	Agents []string `json:"agents,omitempty" yaml:"agents,omitempty"`
	// Fan provides per-fan agent/runner overrides. The slice order is the fan index.
	Fan        []FanSpec `json:"fan,omitempty" yaml:"fan,omitempty"`
	Prompt     string    `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	Cmd        []string  `json:"cmd,omitempty" yaml:"cmd,omitempty"`
	Cwd        string    `json:"cwd,omitempty" yaml:"cwd,omitempty"`
	TimeoutSec int       `json:"timeout_sec,omitempty" yaml:"timeout_sec,omitempty"`
	Tags       []string  `json:"tags,omitempty" yaml:"tags,omitempty"`
	// OnFailure is the per-step失败策略 (P1, design §5.1): "" / "fail" keeps v1
	// fail-fast (the whole workflow fails); "continue" skips the failed step and
	// advances to the next; "retry" re-runs the step (bounded by Retry, backoff
	// per job.RetryPolicy.BackoffSec). Zero value ("") == v1 behaviour (D23).
	OnFailure string `json:"on_failure,omitempty" yaml:"on_failure,omitempty"`
	// Retry is the retry policy used when OnFailure=="retry" (P1, design §5.1).
	// nil == no retry (the v1 default); validated at submit (validateRetry).
	Retry *job.RetryPolicy `json:"retry,omitempty" yaml:"retry,omitempty"`
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
	SubWorkflow *Spec `json:"sub_workflow,omitempty" yaml:"sub_workflow,omitempty"`
	// File is a CLI-ONLY md-per-step reference (P4 / T4.2): when set in a workflow yaml
	// file, the CLI loads the named md file (frontmatter→step params, body→prompt) and
	// expands it INTO the other fields before submit. It is `json:"-"` so it never
	// crosses the wire to the server (the server only ever sees the expanded fields), and
	// is absent from a v1 spec (D23). Resolved relative to the workflow file's directory.
	File string `json:"-" yaml:"file,omitempty"`
	// Env is the extra environment for this step's job (design 一·4): it rides
	// JobRequest.Env, so it applies on the server's own runner directly and reaches a
	// worker as wsproto.Dispatch.env (protocol v26; a job that carries env is refused for
	// an older worker rather than run without its variables). Values are stored with the
	// job, so they are no place for secrets.
	Env map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	// Ref is the commit a step checks out in a TEMPORARY detached worktree (design 一·3).
	// It is resolved to a full sha at submit (so every fan checks the same tree), removed
	// when the job ends, and mutually exclusive with Worktree. On a machine whose
	// checkout lacks the commit it is fetched from the default remote first.
	Ref string `json:"ref,omitempty" yaml:"ref,omitempty"`
	// Uses names a built-in step (design §一·5, `gofer/<name>`), with its arguments in
	// With: the engine submits an internal `gofer tool step <name>` exec job whose stdin
	// is the With JSON and whose last stdout line is the step result (S3).
	Uses string         `json:"uses,omitempty" yaml:"uses,omitempty"`
	With map[string]any `json:"with,omitempty" yaml:"with,omitempty"`
	// Run changes WHEN a step runs (design §一·8): "" (the default) runs it in chain
	// order, "always" runs it even after an earlier failure (fail-fast jumps to the
	// run: always steps and runs them in order) without changing the workflow's final
	// outcome — a failure before them still fails the whole workflow.
	Run string `json:"run,omitempty" yaml:"run,omitempty"`
	// Review overrides the project's require_review default for THIS step
	// (GATE-01 S3): true parks the step's job in needs_review until a human accepts
	// (the chain advances then) or rejects (the step counts as failed, so on_failure
	// applies). It is a pointer so an explicit `review: false` overrides a project
	// that requires review for everything, while nil inherits the project default.
	Review *bool `json:"review,omitempty" yaml:"review,omitempty"`
	// Z1 job options are copied to every generated step job.
	Worktree     bool              `json:"worktree,omitempty" yaml:"worktree,omitempty"`
	WorktreeBase string            `json:"worktree_base,omitempty" yaml:"worktree_base,omitempty"`
	Template     string            `json:"template,omitempty" yaml:"template,omitempty"`
	Vars         map[string]string `json:"vars,omitempty" yaml:"vars,omitempty"`
	ReadOnly     bool              `json:"read_only,omitempty" yaml:"read_only,omitempty"`
	Verify       []string          `json:"verify,omitempty" yaml:"verify,omitempty"`
}

// FanSpec overrides the agent and runner for one ordered fan. Runner may be empty
// to inherit the step runner. Agent may be empty when the fan carries its own Cmd:
// an exec fan defaults to the built-in exec agent (design §一·2), so one step can fan
// out per-check commands (`cmd` / `env` / `cwd` / `timeout_sec` each overridable).
// Name, when set, must be unique within the step and is what
// ${steps.<step>.<fan name>.<field>} refers to (the positional ${steps.<step>.fK.…}
// form keeps working).
type FanSpec struct {
	Name       string            `json:"name,omitempty" yaml:"name,omitempty"`
	Agent      string            `json:"agent,omitempty" yaml:"agent,omitempty"`
	Runner     string            `json:"runner,omitempty" yaml:"runner,omitempty"`
	Cmd        []string          `json:"cmd,omitempty" yaml:"cmd,omitempty"`
	Env        map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	Cwd        string            `json:"cwd,omitempty" yaml:"cwd,omitempty"`
	TimeoutSec int               `json:"timeout_sec,omitempty" yaml:"timeout_sec,omitempty"`
}

// onFailure* are the known StepSpec.OnFailure values. "" is treated as
// onFailureFail (v1 fail-fast) so a v1 spec maps to fail without change (D23).
const (
	onFailureFail     = "fail"
	onFailureContinue = "continue"
	onFailureRetry    = "retry"
)

// join* are the known StepSpec.Join values (P2, design §5.1, D15). "" is treated as
// joinAll (the default) so a fan-out step without an explicit join aggregates as all.
const (
	joinAll    = "all"
	joinAny    = "any"
	joinQuorum = "quorum"
	joinPick   = "pick"
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

// maxRetryAttempts caps RetryPolicy.MaxAttempts so a misconfigured workflow can
// not retry forever (defence against失控 retry storms). validateRetry enforces it.
const maxRetryAttempts = 10

// Spec is the submitted job-chain: a title + an ordered list of steps run
// strictly serially (single active step, D1/D10). It is the body of POST
// /v1/workflows and the parsed yaml workflow file (P3).
type Spec struct {
	Title string `json:"title,omitempty" yaml:"title,omitempty"`
	// Project is the workflow's default project key: every step whose project_key is
	// empty runs there (design §一·1). A template read from a project's
	// `.gofer/workflows` defaults to that project.
	Project string                    `json:"project,omitempty" yaml:"project,omitempty"`
	Steps   []StepSpec                `json:"steps" yaml:"steps"`
	Vars    map[string]gotemplate.Var `json:"vars,omitempty" yaml:"vars,omitempty"`
	Picked  map[int]int               `json:"picked,omitempty" yaml:"-"`
}

// fanWant returns the effective parallelism of a step: max(1, FanOut). A fan_out of
// 0/1 is a single job (the v1 path); fan_out>1 is the configured N.
func fanWant(step StepSpec) int {
	if len(step.Fan) > 0 {
		return len(step.Fan)
	}
	if len(step.Agents) > 0 {
		return len(step.Agents)
	}
	if step.FanOut > 1 {
		return step.FanOut
	}
	return 1
}

// fanSpecAt returns the FanSpec of a 1-based fan index, or nil when fanIndex is out of
// the fan list (a plain FanOut/Agents step, or a single-job step).
func fanSpecAt(step StepSpec, fanIndex int) *FanSpec {
	if fanIndex >= 1 && fanIndex <= len(step.Fan) {
		return &step.Fan[fanIndex-1]
	}
	return nil
}

// fanIndexByName returns the 1-based index of the fan named name in step, or 0.
func fanIndexByName(step StepSpec, name string) int {
	for i := range step.Fan {
		if step.Fan[i].Name == name {
			return i + 1
		}
	}
	return 0
}

func fanAgentRunner(step StepSpec, fanIndex int) (string, string) {
	if fanIndex >= 1 && fanIndex <= len(step.Fan) {
		f := step.Fan[fanIndex-1]
		runner := f.Runner
		if runner == "" {
			runner = step.Runner
		}
		return f.Agent, runner
	}
	if fanIndex >= 1 && fanIndex <= len(step.Agents) {
		return step.Agents[fanIndex-1], step.Runner
	}
	return step.Agent, step.Runner
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
// job.MaxAttemptsPolicy so step-level and job-level (E24) retry share one semantics.
func maxAttempts(step StepSpec) int { return job.MaxAttemptsPolicy(step.Retry) }

// backoffFor returns the backoff (seconds) before re-running a step whose attempt
// just failed. Delegates to job.BackoffForPolicy (one shared semantics, E24).
func backoffFor(step StepSpec, attempt int) int { return job.BackoffForPolicy(step.Retry, attempt) }

// retryableExit reports whether a failed step-job is retryable given the step's
// policy. Delegates to job.RetryableExitPolicy (one shared semantics, E24).
func retryableExit(step StepSpec, exitCode int) bool {
	return job.RetryableExitPolicy(step.Retry, exitCode)
}

// Step is one row of a workflow's step list in the detail response:
// the 1-based index, the step name (from the job's title / spec name), the
// step-job id and its current status. job_id/status are empty/"" for a step not
// yet started. Attempt is the 1-based retry attempt of THIS step-job row (P1): a
// retried step contributes one row per attempt (each its own job_id), so the
// detail view shows the full retry history in step+attempt order. FanIndex is the
// 1-based fan-out parallel index (P2): a fan-out step contributes one row per fan
// job (each its own job_id), 0 for a single-job step.
type Step struct {
	StepIndex int `json:"step_index"`
	Attempt   int `json:"attempt,omitempty"`
	FanIndex  int `json:"fan_index,omitempty"`
	// FanName is the `name` the spec's fan list declares for this fan (linux-race / web
	// …), so a fan row shows WHAT it ran instead of repeating the step name
	// (gofer-j5xc). Empty for a plain FanOut/Agents step and for a single-job step.
	FanName        string `json:"fan_name,omitempty"`
	Name           string `json:"name,omitempty"`
	JobID          string `json:"job_id,omitempty"`
	Status         string `json:"status,omitempty"`
	WorktreeBranch string `json:"worktree_branch,omitempty"`
	Diff           string `json:"diff,omitempty"`
	DiffSummary    string `json:"diff_summary,omitempty"`
	// Join is "pick" for a join=pick fan-out step (empty otherwise); Picked marks the
	// fan the human selected (Z4 compare view).
	Join   string `json:"join,omitempty"`
	Picked bool   `json:"picked,omitempty"`
	// Type/ChildWorkflowID surface a Type=="workflow" sub-workflow step (P3 UI fix):
	// such a step runs no step-job, so it is absent from the job-derived rows — these
	// fields let the chain show it and link into the child workflow's detail.
	Type            string `json:"type,omitempty"`
	ChildWorkflowID string `json:"child_workflow_id,omitempty"`
	// Runner/ExitCode/DurationSec/FailureExcerpt are the result-view columns (design
	// §一·6): where the row ran, how it exited, how long it took and why it failed.
	// ExitCode is a pointer so a successful exec step still shows "0" while a step
	// that never started shows none. FailureExcerpt is the failure-looking lines of
	// the job's log tail (at most 20), or the built-in step's error message.
	Runner         string `json:"runner,omitempty"`
	ExitCode       *int   `json:"exit_code,omitempty"`
	DurationSec    int64  `json:"duration_sec,omitempty"`
	FailureExcerpt string `json:"failure_excerpt,omitempty"`
	// Cwd is the directory the row actually ran in (the resolved overlay path for the
	// step's project, or the temporary ref checkout when Ref is set); Ref is the commit
	// the spec asked to check out there. StartedAt/FinishedAt are unix seconds: the
	// former is set once the job started, the latter once it reached a terminal state.
	Cwd        string `json:"cwd,omitempty"`
	Ref        string `json:"ref,omitempty"`
	StartedAt  int64  `json:"started_at,omitempty"`
	FinishedAt int64  `json:"finished_at,omitempty"`
}

package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/util"
	"github.com/inhere/gofer/internal/workflowstep"
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
	view, release, err := e.admitted()
	if err != nil {
		return jobstore.Workflow{}, err
	}
	defer release()
	return view.submitWorkflowImpl(spec, callerID, "", 0, 0)
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
	view, release, err := e.admitted()
	if err != nil {
		return jobstore.Workflow{}, err
	}
	defer release()
	return view.submitWorkflowImpl(spec, callerID, parentID, parentStep, parentAtt)
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
	// An empty step runner means "the project default" (what a plain `job run` does:
	// the built-in local runner). Resolve it now so the stored spec is explicit.
	spec, err := defaultStepRunners(spec, e.ops.Config())
	if err != nil {
		return jobstore.Workflow{}, err
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

	// S1: the spec fields whose execution lands in a later stage (uses/with → S3, run:
	// always → S4) are parsed and rendered but must not be silently ignored. Rejected
	// here, before any DB row / job exists. env and ref are implemented (S2) and pass.
	if err := validateStepFeatures(spec, cfg); err != nil {
		return jobstore.Workflow{}, err
	}

	// S2 / design 一·3: resolve every `ref:` to the commit sha the step will check out and
	// store that sha in the spec, so all fans of the step (and every retried attempt) see
	// ONE tree even if the branch or tag moves while the workflow runs.
	if spec, err = resolveStepRefs(spec, cfg); err != nil {
		return jobstore.Workflow{}, err
	}

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
		want := fanWant(spec.Steps[i])
		firstFan := 0
		if want > 1 {
			firstFan = 1
		}
		for n := 0; n < want; n++ {
			fan := firstFan + n
			req := stepToRequestForFan(spec.Steps[i], "", i+1, 1, fan, callerID)
			if err := rejectInteractiveStepRequest(i+1, req); err != nil {
				return jobstore.Workflow{}, err
			}
			remote := job.IsRemoteRunner(cfg, req.Runner)
			if _, err := e.ops.Validate(cfg, req, remote); err != nil {
				return jobstore.Workflow{}, fmt.Errorf("step %d fan %d: %w", i+1, fan, err)
			}
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
		// fail-fast 标记失败 best-effort：写失败不阻断返回 err，但记 warning，
		// 否则 workflow 头部会停在 running 与实际不符。
		if serr := e.meta.SetWorkflowStatus(wfID, jobstore.WorkflowFailed, "submit step 1: "+err.Error()); serr != nil {
			slog.Warn("set workflow failed on submit step 1", "workflow_id", wfID, "err", serr)
		}
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
	return stepToRequestForFan(step, wfID, stepIndex, attempt, fanIndex, callerID)
}

func stepToRequestForFan(step StepSpec, wfID string, stepIndex, attempt, fanIndex int, callerID string) job.JobRequest {
	reqID := ""
	if wfID != "" {
		reqID = fmt.Sprintf("%s:s%d:a%d", wfID, stepIndex, attempt)
		if fanIndex >= 1 {
			reqID += fmt.Sprintf(":f%d", fanIndex)
		}
	}
	// GATE-01 S3: a step that states review:true|false decides for its own job; an
	// explicit value is FINAL (ReviewFixed) so it also overrides a project whose
	// require_review default would otherwise re-apply in Submit (a bool alone cannot
	// tell "explicitly off" from "unset").
	review := false
	reviewFixed := step.Review != nil
	if reviewFixed {
		review = *step.Review
	}
	agent, runner := fanAgentRunner(step, fanIndex)
	// S1 (design §一·2): a fan may carry its own cmd / env / cwd / timeout_sec so one step
	// fans out the checks of a verify workflow; each override falls back to the step's
	// value. The fan's env layers over the step's (G042: extra wins).
	cmd, env, cwd, timeoutSec := step.Cmd, step.Env, step.Cwd, step.TimeoutSec
	if f := fanSpecAt(step, fanIndex); f != nil {
		if len(f.Cmd) > 0 {
			cmd = f.Cmd
		}
		if len(f.Env) > 0 {
			env = util.MergeEnv(step.Env, f.Env)
		}
		if f.Cwd != "" {
			cwd = f.Cwd
		}
		if f.TimeoutSec > 0 {
			timeoutSec = f.TimeoutSec
		}
	}
	// A step / fan that runs a command but names no agent runs the built-in exec agent
	// (design §一·2). defaultStepRunners already stores that explicitly; this also covers
	// a spec built in memory (a retried step, a nested sub-workflow).
	if agent == "" && len(cmd) > 0 {
		agent = config.BuiltinExecAgentKey
	}
	// A built-in step (design §一·5) runs gofer's own `gofer tool step <name>` on the step's
	// runner, with its `with:` block as JSON on stdin; gofer's own command is what makes it
	// an InternalExec job (the owner's exec dimension is waived, everything else applies).
	uses := step.Uses
	internalExec := ""
	if uses != "" {
		agent = config.BuiltinExecAgentKey
		cmd = []string{"gofer", "tool", "step", usesName(uses)}
		internalExec = job.InternalExecWorkflowStep
	}
	stdin := ""
	if uses != "" && len(step.With) > 0 {
		if raw, err := json.Marshal(step.With); err == nil {
			stdin = string(raw)
		}
	}
	return job.JobRequest{
		ProjectKey: step.ProjectKey,
		Agent:      agent,
		Runner:     runner,
		// gofer-3db4: a workflow step is gofer's own job; an end-notification it cannot
		// address is dropped with an event rather than failing the chain.
		NotifyDegradable: true,
		Prompt:           step.Prompt,
		Cmd:              cmd,
		Stdin:            stdin,
		InternalExec:     internalExec,
		Env:              env,
		Cwd:              cwd,
		TimeoutSec:       timeoutSec,
		Title:            step.Name,
		Tags:             stepTags(step, internalExec),
		// `ref:` (design 一·3): the spec carries the resolved sha by now (resolveStepRefs),
		// so every fan of the step checks out the same commit in a temporary worktree.
		Ref:          step.Ref,
		Worktree:     step.Worktree,
		WorktreeBase: step.WorktreeBase,
		Template:     step.Template,
		TemplateVars: step.Vars,
		ReadOnly:     step.ReadOnly,
		Verify:       step.Verify,
		CallerID:     callerID,
		OriginCaller: callerID, // a step is bounded by the ceiling of the caller that created the workflow
		WorkflowID:   wfID,
		StepIndex:    stepIndex,
		Attempt:      attempt,
		FanIndex:     fanIndex,
		RequestID:    reqID,
		// GATE-01 S3: the step's review decision (see above) rides the job request.
		Review:      review,
		ReviewFixed: reviewFixed,
	}
}

// usesName returns the registered step name of a `uses:` value. The name was validated at
// submit (validateUsesStep), so an unparseable value yields the raw spelling and the job
// fails with the step's own "unknown built-in step" message rather than silently running
// the wrong command.
func usesName(uses string) string {
	if name, err := workflowstep.ParseUses(uses); err == nil {
		return name
	}
	return uses
}

// stepTags is the step's tags, with gofer's own workflow-step tag added to an internal
// step job so it is recognisable in the job list (job.WorkflowStepJobTag).
func stepTags(step StepSpec, internalExec string) []string {
	if internalExec == "" {
		return step.Tags
	}
	return append(append([]string{}, step.Tags...), job.WorkflowStepJobTag)
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
		case "", joinAll, joinAny, joinQuorum, joinPick:
			// known (or default-empty) — fall through to the fan-out coupling check.
		default:
			return fmt.Errorf("%w: step %d has unknown join %q (want all/any/quorum)", job.ErrInvalidRequest, stepNo, st.Join)
		}
		// join only applies to a real fan-out (fan_out>1). A join on a single-job step
		// is a misconfiguration (the join would never aggregate more than one job).
		if st.Join != "" && fanWant(st) <= 1 {
			return fmt.Errorf("%w: step %d sets join=%q but fan_out=%d (join only applies to fan_out>1)", job.ErrInvalidRequest, stepNo, st.Join, st.FanOut)
		}
		if len(st.Agents) > 0 && len(st.Fan) > 0 {
			return fmt.Errorf("%w: step %d sets agents and fan together", job.ErrInvalidRequest, stepNo)
		}
		if len(st.Agents) > maxFanOut || len(st.Fan) > maxFanOut {
			return fmt.Errorf("%w: step %d agent fan count exceeds the limit %d", job.ErrInvalidRequest, stepNo, maxFanOut)
		}
		if len(st.Agents) > 0 && st.FanOut > 1 {
			return fmt.Errorf("%w: step %d sets agents and fan_out together", job.ErrInvalidRequest, stepNo)
		}
		for j, a := range st.Agents {
			if a == "" {
				return fmt.Errorf("%w: step %d agents[%d] is empty", job.ErrInvalidRequest, stepNo, j)
			}
		}
		names := make(map[string]bool, len(st.Fan))
		for j, f := range st.Fan {
			// S1: an exec fan may omit its agent as long as it carries its own command
			// (design §一·2: the verify workflow's checks are per-fan cmd lines).
			if f.Agent == "" && len(f.Cmd) == 0 {
				return fmt.Errorf("%w: step %d fan[%d] needs agent or cmd", job.ErrInvalidRequest, stepNo, j)
			}
			if err := validateFanName(stepNo, j, f.Name, names); err != nil {
				return err
			}
		}
	}
	return nil
}

// fanNameRe is the shape of a fan name: an identifier, because it appears as the
// selector of a ${steps.<step>.<fan name>.<field>} reference.
var fanNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// reservedFanNameRe matches the positional / aggregate selectors that already mean
// something else in a reference (f1, all, picked), so a fan may not take one as name.
var reservedFanNameRe = regexp.MustCompile(`^(f\d+|all|picked)$`)

// validateFanName checks one fan's optional name: a plain identifier, not one of the
// reserved selector spellings, and unique within its step (S1: the name is how
// ${steps.<step>.<fan name>.<field>} addresses that fan).
func validateFanName(stepNo, fanIdx int, name string, seen map[string]bool) error {
	if name == "" {
		return nil
	}
	if !fanNameRe.MatchString(name) {
		return fmt.Errorf("%w: step %d fan[%d] name %q is not a valid identifier", job.ErrInvalidRequest, stepNo, fanIdx, name)
	}
	if reservedFanNameRe.MatchString(name) {
		return fmt.Errorf("%w: step %d fan[%d] name %q is reserved (fN/all/picked select a fan by position or aggregate)", job.ErrInvalidRequest, stepNo, fanIdx, name)
	}
	if seen[name] {
		return fmt.Errorf("%w: step %d has duplicate fan name %q", job.ErrInvalidRequest, stepNo, name)
	}
	seen[name] = true
	return nil
}

// runAlways is the only known StepSpec.Run value (design §一·8).
const runAlways = "always"

// unsupportedStepField is the shape of the error a spec field whose execution lands in
// a later stage returns: an explicit "not supported yet" instead of a silent ignore
// (a step that quietly drops ref / uses / with would run the WRONG command).
func unsupportedStepField(stepNo int, field string) error {
	return fmt.Errorf("%w: step %d: 暂不支持：%s（后续版本提供）", job.ErrInvalidRequest, stepNo, field)
}

// validateStepFeatures validates the S1-parsed spec fields whose execution lands in a
// later stage (run: always → S4, still refused) and the built-in steps that are
// implemented (S3: `uses` / `with`). `ref` and `env` are implemented since S2 (a
// temporary detached checkout, and the v26 dispatch field), so they pass.
func validateStepFeatures(spec Spec, cfg *config.Config) error {
	for i := range spec.Steps {
		stepNo := i + 1
		st := spec.Steps[i]
		if st.Uses != "" || len(st.With) > 0 {
			if err := validateUsesStep(cfg, st); err != nil {
				return fmt.Errorf("step %d: %w", stepNo, err)
			}
		}
		if st.Run != "" {
			if st.Run != runAlways {
				return fmt.Errorf("%w: step %d has unknown run %q (want always)", job.ErrInvalidRequest, stepNo, st.Run)
			}
			return unsupportedStepField(stepNo, "run: always")
		}
		if st.SubWorkflow != nil {
			if err := validateStepFeatures(*st.SubWorkflow, cfg); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateUsesStep checks one built-in step at submit (design §一·5): the `uses:` name is
// a registered step, the `with:` block decodes strictly into that step's arguments and
// passes its own checks, the step does not also carry a command / prompt / fan (it runs
// gofer's code, not a job of its own), and a `gofer/push` step's project allows pushing
// (delivery.allow_push — the trust model of the design's §五: pushing main / tags is
// never implicit).
func validateUsesStep(cfg *config.Config, st StepSpec) error {
	if st.Uses == "" {
		return fmt.Errorf("%w: with: is only allowed together with uses: gofer/<name>", job.ErrInvalidRequest)
	}
	if err := workflowstep.Validate(st.Uses, st.With); err != nil {
		return fmt.Errorf("%w: %v", job.ErrInvalidRequest, err)
	}
	switch {
	case len(st.Cmd) > 0:
		return fmt.Errorf("%w: a uses: step may not also set cmd (it runs gofer's own command)", job.ErrInvalidRequest)
	case st.Prompt != "":
		return fmt.Errorf("%w: a uses: step may not also set prompt", job.ErrInvalidRequest)
	case len(st.Fan) > 0 || len(st.Agents) > 0 || st.FanOut > 1:
		return fmt.Errorf("%w: a uses: step may not fan out", job.ErrInvalidRequest)
	}
	if name, _ := workflowstep.ParseUses(st.Uses); name == workflowstep.NamePush && cfg != nil {
		if d := cfg.Projects[st.ProjectKey].Delivery; d == nil || !d.AllowPush {
			return fmt.Errorf("%w: %s: project %q does not allow pushing (set projects.%s.delivery.allow_push)",
				job.ErrInvalidRequest, st.Uses, st.ProjectKey, st.ProjectKey)
		}
	}
	return nil
}

// SpecUsesPush reports whether a spec (or any nested sub-workflow) runs a `gofer/push`
// step. The HTTP entry point uses it to require the `repo.push` permission before a
// workflow that would push anything is submitted.
func SpecUsesPush(spec Spec) bool {
	for i := range spec.Steps {
		st := spec.Steps[i]
		if name, err := workflowstep.ParseUses(st.Uses); st.Uses != "" && err == nil && name == workflowstep.NamePush {
			return true
		}
		if st.SubWorkflow != nil && SpecUsesPush(*st.SubWorkflow) {
			return true
		}
	}
	return false
}

// resolveStepRefs replaces every step's `ref:` with the full commit sha it names, resolved
// against the project's checkout on THIS machine (design 一·3). The stored spec then pins
// one commit for the whole workflow, which is what makes a fan-out of the step a check of
// the SAME tree on every runner.
//
// A ref and worktree on one step are refused: ref is a detached checkout thrown away when
// the job ends, worktree is a branch meant to be delivered.
func resolveStepRefs(spec Spec, cfg *config.Config) (Spec, error) {
	for i := range spec.Steps {
		st := &spec.Steps[i]
		if st.SubWorkflow != nil {
			sub, err := resolveStepRefs(*st.SubWorkflow, cfg)
			if err != nil {
				return spec, fmt.Errorf("step %d sub_workflow: %w", i+1, err)
			}
			st.SubWorkflow = &sub
		}
		if strings.TrimSpace(st.Ref) == "" {
			continue
		}
		if st.Worktree {
			return spec, fmt.Errorf("%w: step %d sets both ref and worktree (mutually exclusive: ref checks out one commit in a temporary worktree)", job.ErrInvalidRequest, i+1)
		}
		if cfg == nil {
			return spec, fmt.Errorf("%w: step %d ref: no config to resolve the project", job.ErrInvalidRequest, i+1)
		}
		proj, ok := cfg.Projects[st.ProjectKey]
		if !ok {
			return spec, fmt.Errorf("%w: step %d ref: unknown project %q", job.ErrInvalidRequest, i+1, st.ProjectKey)
		}
		sha, err := job.ResolveCommitRef(context.Background(), cfg.ExecPath(proj), st.Ref)
		if err != nil {
			return spec, fmt.Errorf("step %d ref: %w", i+1, err)
		}
		st.Ref = sha
	}
	return spec, nil
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
				if err := rejectInteractiveStepRequest(j+1, req); err != nil {
					return fmt.Errorf("step %d sub_workflow: %w", stepNo, err)
				}
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

func rejectInteractiveStepRequest(stepIndex int, req job.JobRequest) error {
	if req.Interactive {
		return fmt.Errorf("%w: workflow step %d cannot be interactive", job.ErrInvalidRequest, stepIndex)
	}
	return nil
}

// genWorkflowID returns a collision-resistant workflow id, "wf-" prefixed so it
// is visually distinct from a job id (which shares the same time+random scheme).
func (e *Engine) genWorkflowID() string {
	return "wf-" + e.now().Format(job.JobIDLayout) + "-" + job.RandomSuffix()
}

// defaultStepRunners fills an empty StepSpec.Runner from the step's project: the
// built-in local runner when the project allows it (or lists no runners), else the
// project's single allowed runner. It never guesses between several runners. The
// input spec is not mutated.
//
// It also applies the other submit-time defaults that come from the spec instead of
// the config: a step without project_key runs in Spec.Project (S1, design §一·1) and an
// exec step / fan without agent but with its own cmd defaults to the built-in exec
// agent (design §一·2).
func defaultStepRunners(spec Spec, cfg *config.Config) (Spec, error) {
	steps := append([]StepSpec(nil), spec.Steps...)
	for i := range steps {
		st := &steps[i]
		if st.SubWorkflow != nil {
			sub := *st.SubWorkflow
			if sub.Project == "" {
				sub.Project = spec.Project
			}
			rendered, err := defaultStepRunners(sub, cfg)
			if err != nil {
				return spec, err
			}
			st.SubWorkflow = &rendered
		}
		if st.ProjectKey == "" {
			st.ProjectKey = spec.Project
		}
		if st.Agent == "" && len(st.Cmd) > 0 && st.Type != stepTypeWorkflow && st.Uses == "" {
			st.Agent = agent.ExecAgentKey
		}
		// A built-in step (`uses:`) runs gofer's own exec command on the step's runner.
		if st.Uses != "" {
			st.Agent = agent.ExecAgentKey
		}
		if len(st.Fan) > 0 {
			fan := append([]FanSpec(nil), st.Fan...)
			for j := range fan {
				fan[j].Runner = config.NormalizeRunnerName(fan[j].Runner)
				if fan[j].Agent == "" && len(fan[j].Cmd) > 0 {
					fan[j].Agent = agent.ExecAgentKey
				}
			}
			st.Fan = fan
		}
		if st.Runner != "" && st.Type != stepTypeWorkflow {
			// G043: store the canonical spelling so a spec written with `server`
			// and one written with `local` are the same stored workflow.
			st.Runner = config.NormalizeRunnerName(st.Runner)
		}
		if st.Runner != "" || st.Type == stepTypeWorkflow || cfg == nil {
			continue
		}
		proj, ok := cfg.Projects[st.ProjectKey]
		if !ok {
			continue // unknown project: the normal admission reports it (worker projects etc.)
		}
		allowed := proj.AllowedRunners
		switch {
		case len(allowed) == 0:
			st.Runner = config.BuiltinLocalRunner
		case slices.ContainsFunc(allowed, config.IsBuiltinLocalRunnerName):
			st.Runner = config.BuiltinLocalRunner
		case len(allowed) == 1:
			st.Runner = allowed[0]
		default:
			return spec, fmt.Errorf("%w: step %d has no runner and project %q allows several (%s): set runner", job.ErrInvalidRequest, i+1, st.ProjectKey, strings.Join(allowed, ", "))
		}
	}
	spec.Steps = steps
	return spec, nil
}

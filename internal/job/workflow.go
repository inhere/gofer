package job

import (
	"context"
	"encoding/json"
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
}

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

	// Pre-validate every step against the single-job准入 before creating anything,
	// so an invalid step (e.g. step 3) is rejected at submit time, not mid-chain.
	cfg := s.config()
	for i := range spec.Steps {
		req := stepToRequest(spec.Steps[i], "", i+1, callerID)
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
		TotalSteps:  len(spec.Steps),
		SpecJSON:    string(specJSON),
		CallerID:    callerID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.meta.InsertWorkflow(wf); err != nil {
		return jobstore.Workflow{}, err
	}

	// Start step 1. On failure mark the workflow failed (fail-fast at the source)
	// and return the error; the header row stays for inspection.
	req := stepToRequest(spec.Steps[0], wfID, 1, callerID)
	if _, err := s.Submit(req); err != nil {
		_ = s.meta.SetWorkflowStatus(wfID, jobstore.WorkflowFailed, "submit step 1: "+err.Error())
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
// WorkflowID/StepIndex fields are how finish knows to advance the chain.
func stepToRequest(step StepSpec, wfID string, stepIndex int, callerID string) JobRequest {
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
	}
}

// genWorkflowID returns a collision-resistant workflow id, "wf-" prefixed so it
// is visually distinct from a job id (which shares the same time+random scheme).
func (s *Service) genWorkflowID() string {
	return "wf-" + s.nowFn().Format(jobIDLayout) + "-" + randomSuffix()
}

// advanceWorkflow advances a workflow when its current step's job reaches a
// terminal state. It is the串行推进引擎 and is designed to be called MANY times,
// concurrently, for the same workflow — the finish hook (one call per step-job
// terminal) AND the sweeper (crash recovery) both invoke it. 幂等 is guaranteed by
// AdvanceCurrentStep's conditional UPDATE: only ONE caller wins the推进权, so the
// next step is started exactly once (一个 step 绝不起两次).
//
// step 序号/spec 下标对齐 (核心，务必看清)：
//   - wf.CurrentStep is 1-based and points at the JUST-FINISHED step (the active
//     one). The matching job has step_index == wf.CurrentStep.
//   - spec.Steps is 0-based, so the just-finished step is spec.Steps[CurrentStep-1].
//   - the NEXT step is spec.Steps[CurrentStep] (0-based) == 1-based step CurrentStep+1.
//   - we read cur := wf.CurrentStep FIRST, then AdvanceCurrentStep(cur -> cur+1) to
//     win推进权, then start spec.Steps[cur] (0-based) as step cur+1. Using the
//     locally captured `cur` (not the post-advance DB value) keeps the index
//     unambiguous.
//
// best-effort: failures are logged, never panic; a missed advance is re-tried by
// the sweeper on its next tick.
func (s *Service) advanceWorkflow(wfID string) {
	wf, ok, err := s.meta.GetWorkflow(wfID)
	if err != nil || !ok || wf.Status != jobstore.WorkflowRunning {
		return // unknown or already terminal: nothing to advance
	}

	jobs, err := s.meta.ListWorkflowJobs(wfID)
	if err != nil {
		return
	}
	cur := wf.CurrentStep // 1-based, == the active/just-finished step
	curJob := stepJob(jobs, cur)
	if curJob == nil || !isTerminal(curJob.Status) {
		return // current step has not started yet, or is still running
	}

	// 抢推进权 (幂等屏障)：only the winner of this conditional UPDATE proceeds. A
	// concurrent/duplicate call (finish hook + sweeper for the same finished step)
	// loses here and returns — so the next step is started by exactly one caller.
	won, err := s.meta.AdvanceCurrentStep(wfID, cur, cur+1)
	if err != nil || !won {
		return
	}

	switch curJob.Status {
	case StatusFailed, StatusTimeout, StatusCancelled:
		// fail-fast (D4): any non-done terminal step fails the whole workflow; no
		// further steps are started.
		_ = s.meta.SetWorkflowStatus(wfID, jobstore.WorkflowFailed,
			fmt.Sprintf("step %d %s", cur, curJob.Status))
	case StatusDone:
		if cur >= wf.TotalSteps {
			// Last step done -> workflow done.
			_ = s.meta.SetWorkflowStatus(wfID, jobstore.WorkflowDone, "")
			return
		}
		var spec WorkflowSpec
		if err := json.Unmarshal([]byte(wf.SpecJSON), &spec); err != nil {
			_ = s.meta.SetWorkflowStatus(wfID, jobstore.WorkflowFailed, "decode spec: "+err.Error())
			return
		}
		if cur >= len(spec.Steps) {
			// Defensive: total_steps disagrees with spec length (should not happen).
			_ = s.meta.SetWorkflowStatus(wfID, jobstore.WorkflowFailed, "spec/total step mismatch")
			return
		}
		next := spec.Steps[cur] // 0-based: spec.Steps[cur] is the next (1-based step cur+1)
		// P2 接入点：此处在起下一步前用 resolveRefs(&next, jobs) 把 ${steps.N.field} 替换为
		// 前序产出（result_dir/stdout/exit_code/...）。P1 不实现引用，各 step 独立跑。
		req := stepToRequest(next, wfID, cur+1, wf.CallerID)
		if _, err := s.Submit(req); err != nil {
			_ = s.meta.SetWorkflowStatus(wfID, jobstore.WorkflowFailed, "submit step: "+err.Error())
		}
	}
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
// yet started.
type WorkflowStep struct {
	StepIndex int    `json:"step_index"`
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

// stepJob returns the step-job whose step_index == stepIndex (1-based), or nil.
func stepJob(jobs []jobstore.JobRecord, stepIndex int) *jobstore.JobRecord {
	for i := range jobs {
		if jobs[i].StepIndex == stepIndex {
			return &jobs[i]
		}
	}
	return nil
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

	// Cancel the active step's job (Cancel is a stable no-op for a terminal job).
	jobs, err := s.meta.ListWorkflowJobs(wfID)
	if err != nil {
		return nil // status is already cancelled; job-cancel is best-effort
	}
	if cur := stepJob(jobs, wf.CurrentStep); cur != nil {
		_ = s.Cancel(cur.ID)
	}
	return nil
}

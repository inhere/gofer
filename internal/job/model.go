// Package job is the async job state machine: it accepts a JobRequest, resolves
// the agent+cwd, runs it on a runner in a background goroutine, streams logs to
// the store and tracks status/timeout/cancel. See plan §6.2 and §9 (P4).
package job

// JobRequest is the create-job payload. JSON tags are snake_case (plan §6.2).
// yaml tags mirror the json names so the md+yaml frontmatter submit path
// (design §6.2 / P1-b) reuses the same struct via goccy/go-yaml.
type JobRequest struct {
	ProjectKey string   `json:"project_key" yaml:"project_key"`
	Agent      string   `json:"agent" yaml:"agent"`
	Runner     string   `json:"runner" yaml:"runner"`
	Prompt     string   `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	Cmd        []string `json:"cmd,omitempty" yaml:"cmd,omitempty"`
	Cwd        string   `json:"cwd,omitempty" yaml:"cwd,omitempty"`
	TimeoutSec int      `json:"timeout_sec,omitempty" yaml:"timeout_sec,omitempty"`
	Title      string   `json:"title,omitempty" yaml:"title,omitempty"`
	// WorkerID selects which registered worker a runner=worker job dispatches to
	// (ws-worker §8). When set it must be a known server.workers entry (explicit
	// routing wins); ignored for local/peer-http runners. When empty for a worker
	// runner, WorkerLabels (if any) drives auto-selection (D3), else the runner's
	// configured default worker is used (D4 fallback).
	WorkerID string `json:"worker_id,omitempty" yaml:"worker_id,omitempty"`
	// WorkerLabels auto-selects a worker by labels when runner=worker and WorkerID
	// is empty (D3): a candidate worker must advertise ALL these labels. Ignored
	// when WorkerID is set (explicit routing wins).
	WorkerLabels []string `json:"worker_labels,omitempty" yaml:"worker_labels,omitempty"`
	// Sync requests synchronous submit: the HTTP handler blocks until the job is
	// terminal (capped server-side) and returns the final JobResult. Can also be
	// set via ?wait=1. WaitTimeoutSec overrides the default wait cap (clamped).
	Sync           bool `json:"sync,omitempty" yaml:"sync,omitempty"`
	WaitTimeoutSec int  `json:"wait_timeout_sec,omitempty" yaml:"wait_timeout_sec,omitempty"`
	// CallerID is the authenticated submitter id (C2). It is set server-side by
	// the HTTP layer from the auth context (any client-supplied value is
	// overwritten); it is not part of the client-facing contract. yaml:"-" keeps
	// md frontmatter from forging the caller id (design §9).
	CallerID string `json:"caller_id,omitempty" yaml:"-"`
	// RequestID is the optional client-supplied idempotency key (C5, e.g. a
	// UUID). When set, re-submitting the same RequestID returns the existing job
	// instead of creating a new one (deduped by the jobs.request_id unique index).
	RequestID string `json:"request_id,omitempty" yaml:"request_id,omitempty"`
	// Tags are free-form labels for the job (E5). They are persisted (jobs.tags_json)
	// and queryable via ?tag= (exact element match). Unlike WorkerLabels (routing,
	// not stored), Tags are索引/检索维度。
	Tags []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	// Retry is the OPTIONAL per-job retry policy (E24 unified retry, P1, design
	// §6.2). nil == no retry (the v1 default,向后兼容). When set on a non-workflow
	// job, finish re-runs the job on a retryable failure (attempt+1, same request_json)
	// up to RetryPolicy.MaxAttempts, with backoff per the policy. It shares the SAME
	// RetryPolicy / backoffFor / retryableExit as the step-level retry (one semantics,
	// roadmap横切). P1 uses an in-process delay (time.AfterFunc): a process restart
	// loses a pending job-level retry — the可靠版 (sweeper-driven next_retry_at) is
	// left for后续; the reliable path today is a single-step workflow + step retry.
	Retry *RetryPolicy `json:"retry,omitempty" yaml:"retry,omitempty"`
	// WorkflowID / StepIndex are INTERNAL fields set ONLY by the workflow engine
	// (SubmitWorkflow / advanceWorkflow) when starting a step-job; they bind the job
	// to its workflow + 1-based step. json/yaml tag "-" keeps clients (HTTP body / md
	// frontmatter) from forging them — a plain POST /v1/jobs never sets a workflow.
	WorkflowID string `json:"-" yaml:"-"`
	StepIndex  int    `json:"-" yaml:"-"`
	// Attempt is the 1-based retry attempt of a step-job (P1, design §5.3). It is
	// set by the workflow engine (stepToRequest) and persisted to jobs.attempt so a
	// retried step's distinct runs are distinguishable. A non-workflow job (or a v1
	// step) leaves it 0; the persist path COALESCEs that to 1. json/yaml "-" keeps
	// clients from forging it.
	Attempt int `json:"-" yaml:"-"`
	// FanIndex is the 1-based parallel index of a fan-out step-job (P2, design §5.3):
	// a FanOut>1 step starts N jobs sharing (step_index, attempt), distinguished by
	// FanIndex=1..N. Set ONLY by the workflow engine (stepToRequest); a non-fan-out
	// job (single-job path, v1/P1) leaves it 0. Persisted to jobs.fan_index and forms
	// the f<fanIndex> segment of the deterministic request_id. json/yaml "-" keeps
	// clients from forging it.
	FanIndex int `json:"-" yaml:"-"`
}

// JobResult is the persisted/queryable job state (plan §6.2).
type JobResult struct {
	ID         string `json:"id"`
	ProjectKey string `json:"project_key"`
	Agent      string `json:"agent"`
	Runner     string `json:"runner"`
	// Title is the optional human-readable job name from the original JobRequest.
	// The jobs table has no title column; it persists inside request_json and is
	// recovered on the DB read path (fromRecord) so it round-trips, not just on
	// the live in-memory path.
	Title     string `json:"title,omitempty"`
	Status    string `json:"status"`
	ExitCode  int    `json:"exit_code"`
	Cwd       string `json:"cwd"`
	ResultDir string `json:"result_dir"`
	// WorkerID is the worker that executed a runner=worker job (ws-worker §8),
	// persisted to jobs.worker_id and echoed for audit / filtering. Empty for
	// local/peer-http jobs.
	WorkerID  string `json:"worker_id,omitempty"`
	StartedAt int64  `json:"started_at"`
	EndedAt   int64  `json:"ended_at,omitempty"`
	// UpdatedAt is the unix time of the last persisted snapshot. It is stamped by
	// the metadata store write path (Service.persist) so listing/retention always
	// have a monotonic ordering value; it is not set by the runner state machine.
	UpdatedAt int64  `json:"updated_at,omitempty"`
	Error     string `json:"error,omitempty"`
	// CallerID is the authenticated submitter id (C2), persisted to
	// jobs.caller_id and echoed in responses for audit / per-caller filtering.
	CallerID string `json:"caller_id,omitempty"`
	// RequestID is the idempotency key (C5) this job was created with; it is
	// persisted (jobs.request_id) and echoed so the idempotent-reuse path returns
	// it and it round-trips through persist.
	RequestID string `json:"request_id,omitempty"`
	// RequestJSON is the original JobRequest marshalled to JSON, kept for audit /
	// re-submit. It is persisted to the jobs.request_json column (SP5 replaces the
	// on-disk request.json file). json:"-" keeps it out of API responses — it is an
	// internal/audit field, not part of the queryable job state.
	RequestJSON string `json:"-"`
	// 产出与审计（job-outcomes-audit）：job 终态时 captureOutcomes 采集的产出字段。
	// 全部 best-effort（采集失败为空），omitempty 保证旧 job/未捕获时不出现在响应里。
	// RenderedCommand 是 {command,args,env_keys} 的 JSON 字符串（E15），前端 JSON.parse。
	RenderedCommand string `json:"rendered_command,omitempty"`
	// ResultJSON 是 <result_dir>/result.json 原文（已是合法 JSON 字符串），前端 JSON.parse（E6）。
	ResultJSON string `json:"result_json,omitempty"`
	// ArtifactsJSON 产物清单（E1）：不进 get_job（清单走专门端点，P2），仅入库 + 透传。
	ArtifactsJSON string `json:"-"`
	// DiffSummary git diff --stat 截断摘要（E12，P3）。
	DiffSummary string `json:"diff_summary,omitempty"`
	// Source 标记 job 实际执行位置（P4）：""(local) / worker:<id> / peer:<name>。
	// 远端 runner 回传时填充并入库，详情据此标注执行来源（P4-c）。
	Source string `json:"source,omitempty"`
	// Tags 是 job 的自由标签（E5），持久化到 jobs.tags_json，支持 ?tag= 检索。
	// omitempty 保证无标签的 job 响应里不出现该字段。
	Tags []string `json:"tags,omitempty"`
	// WorkflowID / StepIndex 标记此 job 属于哪个工作流的第几步（1-based）。普通 job 为
	// ""/0（omitempty 不出现在响应里）。持久化到 jobs.workflow_id/step_index，
	// finish 钩子据 WorkflowID 决定是否异步推进所属工作流。
	WorkflowID string `json:"workflow_id,omitempty"`
	StepIndex  int    `json:"step_index,omitempty"`
	// Attempt 是此 step-job 的 1-based 重试尝试号（P1）。首次运行=1；重试起的新 job
	// attempt+1。持久化到 jobs.attempt（旧库 COALESCE 成 1）。普通 job 为 0（omitempty）。
	Attempt int `json:"attempt,omitempty"`
	// FanIndex 是 fan-out step 内此并行 job 的 1-based 序号（P2）。FanOut>1 的 step 起
	// N 个 job，以 FanIndex=1..N 区分；非 fan-out（单 job 路径）为 0（omitempty）。
	// 持久化到 jobs.fan_index。
	FanIndex int `json:"fan_index,omitempty"`
}

// Job status values (plan §6.2).
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusDone      = "done"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
	StatusTimeout   = "timeout"
	// StatusPendingInteraction is reserved for P9 (running-agent two-way
	// interaction). Declared here so the status set is documented in one place;
	// P4 never sets it.
	StatusPendingInteraction = "pending_interaction"
)

// Job lifecycle event types (E13, design §5.2). Each is recorded append-only via
// recordEvent at the corresponding state transition's persist-success point. The
// detail payload per type is documented at each insertion site.
const (
	EventJobSubmitted        = "job.submitted"        // {project,agent,runner,caller_id,tags}
	EventJobDispatched       = "job.dispatched"       // {runner,worker_id} (remote only)
	EventJobRunning          = "job.running"          // nil
	EventJobTerminal         = "job.terminal"         // {status,exit_code,error}
	EventJobCancelled        = "job.cancelled"        // {was_terminal}
	EventInteractionCreated  = "interaction.created"  // {interaction_id,type,prompt}
	EventInteractionAnswered = "interaction.answered" // {interaction_id,answer}
)

// Workflow lifecycle event types (P1, design §5.4). Recorded append-only via
// recordWorkflowEvent at the corresponding workflow-engine transition. The detail
// payload per type is documented at each insertion site.
const (
	EventWorkflowSubmitted  = "workflow.submitted"  // {title,total_steps,caller_id}
	EventStepStarted        = "step.started"        // {step,attempt,job_id} (single-job step)
	EventStepFanout         = "step.fanout"         // {step,attempt,fan_out,join,job_ids} (P2 fan-out step)
	EventStepRetry          = "step.retry"          // {step,attempt,next_attempt,backoff_sec,next_step_at}
	EventStepSkipped        = "step.skipped"        // {step,attempt,status} (on_failure=continue)
	EventSubworkflowStarted = "subworkflow.started" // {step,child_workflow_id,total_steps} (P3, type=workflow step)
	EventWorkflowTerminal   = "workflow.terminal"   // {status,error}
	EventWorkflowCancelled  = "workflow.cancelled"  // {was_terminal}
)

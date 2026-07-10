// Package job is the async job state machine: it accepts a JobRequest, resolves
// the agent+cwd, runs it on a runner in a background goroutine, streams logs to
// the store and tracks status/timeout/cancel. See plan §6.2 and §9 (P4).
package job

// JobRequest is the create-job payload. JSON tags are snake_case (plan §6.2).
// yaml tags mirror the json names so the md+yaml frontmatter submit path
// (design §6.2 / P1-b) reuses the same struct via goccy/go-yaml.
type JobRequest struct {
	ProjectKey string `json:"project_key" yaml:"project_key"`
	Agent      string `json:"agent" yaml:"agent"`
	Runner     string `json:"runner" yaml:"runner"`
	Prompt     string `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	// AgentArgs are extra CLI flags appended to a cli-agent's argv at build time.
	// Ignored for exec agents (§14). Persisted in request_json for rerun/replay.
	AgentArgs  []string `json:"agent_args,omitempty" yaml:"agent_args,omitempty"`
	Cmd        []string `json:"cmd,omitempty" yaml:"cmd,omitempty"`
	Cwd        string   `json:"cwd,omitempty" yaml:"cwd,omitempty"`
	TimeoutSec int      `json:"timeout_sec,omitempty" yaml:"timeout_sec,omitempty"`
	Title      string   `json:"title,omitempty" yaml:"title,omitempty"`
	// Interactive requests a pty-attached run (WEB-03, design §5/§8): the job
	// service routes an interactive job to the pty runner variant (when a pty
	// backend is registered) instead of req.Runner, so its stdin/stdout is a raw
	// terminal a browser can attach to. Non-interactive (the default false) is
	// byte-for-byte the existing path (G023). Cols/Rows are the INITIAL terminal
	// size (default 80x24). NOTE (spike): admission gating (interactive白名单 /
	// no-raw-cmd / reject exec+workflow+schedule) and threading Cols/Rows through
	// runner.Request land in P1 — P0 only wires the runner-selection seam.
	Interactive bool `json:"interactive,omitempty" yaml:"interactive,omitempty"`
	Cols        int  `json:"cols,omitempty" yaml:"cols,omitempty"`
	Rows        int  `json:"rows,omitempty" yaml:"rows,omitempty"`
	// RecordPty requests asciinema recording for this interactive pty session.
	// It is a per-job opt-in layered under the serve-wide storage.cast.enabled
	// capability; false means "track session metadata only, do not write pty.cast".
	RecordPty bool `json:"record_pty,omitempty" yaml:"record_pty,omitempty"`
	// Role is an OPTIONAL E35 role-preset reference (design §8.5). When set, submit
	// resolves it from cfg.Roles to fill empty Agent/SystemPrompt/ProjectKey/Tags
	// (explicit request fields win). An unknown role is rejected (ErrUnknownRole).
	Role string `json:"role,omitempty" yaml:"role,omitempty"`
	// SystemPrompt is the resident system prompt injected via the agent's
	// SystemInject template (E35). Set directly, or filled from the role preset; on
	// submit it is rendered into argv (e.g. claude --append-system-prompt). It is
	// persisted in request_json so resume can re-apply it (review #5).
	SystemPrompt string `json:"system_prompt,omitempty" yaml:"system_prompt,omitempty"`
	// Env is OPTIONAL per-job env layered onto the agent process env: it sits ON TOP
	// of the agent-config env (AgentConfig.Env) but UNDER the gofer-owned job
	// metadata (GOFER_JOB_ID/CWD/RESULT_DIR), so a job can pass extra vars to its
	// agent process (local runner; see Submit). A role preset (RoleConfig.Env) fills
	// it as a DEFAULT — an explicit per-job key wins (resolveRole). The main use is
	// `--role supervisor` injecting GOFER_AGENT_ROLE=supervisor for the MCP
	// self-register (P3). 勿放 secret：本字段随 request_json 落库（SR403/SR805），secret
	// 应改走 agent.env / K8s secret 注入（不入 request_json）。
	Env map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	// EnvFiles declares non-sensitive dotenv file paths whose loaded values are
	// injected only into the execution env. The file list itself may be persisted
	// in request_json; loaded values must never be written back to Env/request_json.
	EnvFiles []string `json:"env_files,omitempty" yaml:"env_files,omitempty"`
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
	// PlanID is the client-settable grouping key (plan-orchestration P1). Unlike
	// WorkflowID/StepIndex, it is not engine-private: clients may attach a job to a
	// plan at submit time via JSON, YAML, CLI, or future MCP inputs.
	PlanID string `json:"plan_id,omitempty" yaml:"plan_id,omitempty"`
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
	// SessionID, when non-empty, is the底层 agent CLI 会话标识 to bind this job to
	// (session-capture, resume path P2): it wins over auto-injection (the job uses
	// this exact id instead of generating a new uuid) and suppresses capture. It is
	// persisted onto the JobResult so the session链 round-trips. Empty == let submit
	// inject (claude) or capture (codex) decide.
	SessionID string `json:"session_id,omitempty" yaml:"session_id,omitempty"`
	// Channel is the submission CHANNEL — which interface the job was submitted
	// through: "cli" / "web" / "mcp" / "im" (future). Client-declared (CLI stamps
	// "cli", the web console "web", the MCP server "mcp"); informational provenance,
	// not an auth identity (that is CallerID). yaml tag so an md+yaml task file may
	// set it too. Empty for legacy / raw-API submits.
	Channel string `json:"channel,omitempty" yaml:"channel,omitempty"`
	// Client is the ORIGINATING client of the submission: the CLI stamps its
	// os.Hostname(); for an HTTP submit with no client given, the server stamps the
	// remote IP. Together with Channel and CallerID it answers "who/where submitted
	// this". Informational (forgeable) — not used for access control.
	Client string `json:"client,omitempty" yaml:"client,omitempty"`
	// OriginAgent is the agent_id of the主 agent (owner) that launched this job —
	// the orchestrator holding the full plan/design context (supervisor-routing
	// P1.1, design §8.1). Persisted to jobs.origin_agent so a pending interaction's
	// escalation can be routed back to its owner first (L1). For an MCP submit the
	// server auto-injects the registered session agent_id (P1.0); an explicit value
	// wins. Empty for non-MCP entrypoints (CLI/web) — those escalate straight to L2.
	OriginAgent string `json:"origin_agent,omitempty" yaml:"origin_agent,omitempty"`
	// EscalateTo is an OPTIONAL job-level override of the escalation recipient (a
	// to-spec like "role-one:supervisor" / "agent:<id>"), tried after the owner and
	// before the global policy default (supervisor-routing P1.1). Empty == use the
	// global supervisor policy. Persisted to jobs.escalate_to. The routing改写 that
	// consumes it lands in P1.2; P1.1 only carries it through.
	EscalateTo string `json:"escalate_to,omitempty" yaml:"escalate_to,omitempty"`
	// ResumeSourceAgent is an INTERNAL marker set ONLY by ResumeJob (session-capture
	// P2, 2026-06-26 decision). A resume mechanically carries Agent="exec" (the
	// resume argv runs as the built-in exec carrier), but its REAL identity for
	// access control is the SOURCE agent whose session is being续接. When set,
	// validate gates BOTH the allowed_agents check and the exec security gate on
	// THIS source agent instead of the "exec" carrier: resume only re-runs the
	// source agent's CLI in a constrained, templated form (argv = [agent.Command] +
	// SessionResume + prompt), so it must NOT demand the broad allow_exec that an
	// arbitrary exec job would. json/yaml "-": this exemption is a property of the
	// `resume` entrypoint, NOT of the persisted job — it is never written to
	// request_json and clients cannot forge it via the public submit API (a forged
	// value would otherwise bypass allow_exec to run arbitrary exec). A `job rerun`
	// of a resume job replays the stored exec request through the public path and is
	// therefore (correctly) gated as a plain exec job — re-resume via the `resume`
	// command instead.
	ResumeSourceAgent string `json:"-" yaml:"-"`
}

// JobResult is the persisted/queryable job state (plan §6.2).
type JobResult struct {
	ID          string `json:"id"`
	ProjectKey  string `json:"project_key"`
	Agent       string `json:"agent"`
	Runner      string `json:"runner"`
	Interactive bool   `json:"interactive,omitempty"`
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
	// Channel / Client are the submission provenance (mirrors JobRequest): which
	// interface (cli/web/mcp/im) and which originating host/addr the job came from.
	// Persisted to jobs.channel / jobs.client; surfaced in show/list so DB records
	// answer "who/where/how submitted" alongside CallerID.
	Channel string `json:"channel,omitempty"`
	Client  string `json:"client,omitempty"`
	// OriginAgent / EscalateTo are the supervisor-routing owner columns (P1.1,
	// mirrors JobRequest): OriginAgent=发起该 job 的 owner agent_id（escalation 优先
	// 回投它，L1），EscalateTo=可选 job 级 escalate 覆盖。持久化到 jobs.origin_agent /
	// jobs.escalate_to；surfaced in show/get so callers can see the owner routing.
	OriginAgent string `json:"origin_agent,omitempty"`
	EscalateTo  string `json:"escalate_to,omitempty"`
	// Role is the E35 role-preset name this job was launched with (mirrors
	// JobRequest.Role). It is persisted to jobs.role so the supervisor router can
	// identify a job that is ITSELF a supervisor (Role=="supervisor") and refuse to
	// auto-answer or re-escalate its interactions (套娃防护, supervisor-routing P2.2,
	// design §8.4) — those go straight to a human (L3). Empty for a roleless job.
	Role string `json:"role,omitempty"`
	// PlanID is the client-settable plan grouping key persisted to jobs.plan_id.
	// Empty means this job is not grouped under a plan.
	PlanID string `json:"plan_id,omitempty"`
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
	// SessionID 底层 agent CLI 会话标识(claude/codex)。注入(提交时 gofer 生成)或捕获(终态从输出)。
	// 空=无/未捕获。持久化 jobs.session_id，供 show/list/resume。
	SessionID string `json:"session_id,omitempty"`
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
	EventInteractionPunted   = "interaction.punted"   // {interaction_id,caller_id}
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

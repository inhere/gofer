// Package job is the async job state machine: it accepts a JobRequest, resolves
// the agent+cwd, runs it on a runner in a background goroutine, streams logs to
// the store and tracks status/timeout/cancel. See plan §6.2 and §9 (P4).
package job

import "github.com/inhere/gofer/internal/runner"

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
	AgentArgs []string `json:"agent_args,omitempty" yaml:"agent_args,omitempty"`
	Cmd       []string `json:"cmd,omitempty" yaml:"cmd,omitempty"`
	// Cwd is the job's working directory, relative to the project root. Under
	// --worktree it is mapped into the job's worktree (see Worktree).
	Cwd string `json:"cwd,omitempty" yaml:"cwd,omitempty"`
	// Worktree (WT-01) runs the job in a MANAGED git worktree of the project
	// checkout instead of the checkout itself: gofer creates
	// <repo top>/tmp/gofer/wt/<job-id> on a fresh branch gofer/<job-id> at
	// WorktreeBase, runs the whole job there (cwd mapped by the same relative
	// sub-path, env GOFER_WORKTREE/_BRANCH/_BASE exported) and KEEPS it afterwards
	// (the branch is the deliverable). It is what lets several agent jobs edit and
	// commit in one checkout in parallel without fighting over .git/index.lock.
	// Rejected when the resolved cwd is not inside a git checkout. A project may
	// turn it on for every job with worktree_default: true.
	Worktree bool `json:"worktree,omitempty" yaml:"worktree,omitempty"`
	// WorktreeBase is the ref the worktree branch starts at (WT-01). Empty = the
	// checkout's current HEAD. Only meaningful together with Worktree.
	WorktreeBase string `json:"worktree_base,omitempty" yaml:"worktree_base,omitempty"`
	TimeoutSec   int    `json:"timeout_sec,omitempty" yaml:"timeout_sec,omitempty"`
	Title        string `json:"title,omitempty" yaml:"title,omitempty"`
	// Interactive requests a pty-attached run (WEB-03, design §5/§8): the job
	// service routes an interactive job to the pty runner variant (when a pty
	// backend is registered) instead of req.Runner, so its stdin/stdout is a raw
	// terminal a browser can attach to. Non-interactive (the default false) is
	// byte-for-byte the existing path (G023). Cols/Rows are the INITIAL terminal
	// size (default 80x24). NOTE (spike): admission gating (interactive白名单 /
	// no-raw-cmd / reject exec+workflow+schedule) and threading Cols/Rows through
	// runner.Request land in P1 — P0 only wires the runner-selection seam.
	Interactive bool `json:"interactive,omitempty" yaml:"interactive,omitempty"`
	// ReadOnly (bd h-aii-0ql3) asks for a job that cannot write: a cli-agent gets its
	// read-only argv suffix (config read_only_args, built-in for codex/claude), an
	// acp-agent gets session/set_mode with the id mapped by acp.modes.read_only, and
	// an exec agent is refused (its argv is whatever the caller wrote). It is
	// persisted (jobs.read_only) and inherited by a resume — a continuation cannot be
	// upgraded to writable, only a new job can.
	ReadOnly bool `json:"read_only,omitempty" yaml:"read_only,omitempty"`
	// Review (GATE-01 S3) asks for人工验收: when the agent finishes NORMALLY (done),
	// the job parks in `needs_review` instead of done until a HUMAN accepts (→done)
	// or rejects (→rejected) it — an agent never signs off its own work. Set by
	// `job run --review` / the HTTP body's review field, or resolved from the
	// project's require_review default at submit (see ReviewFixed). A failure
	// (failed/cancelled/timeout) never enters review.
	Review bool `json:"review,omitempty" yaml:"review,omitempty"`
	// ReviewFixed marks Review as FINAL: it was set by an explicit workflow-step
	// override (StepSpec.Review), which must beat the project's require_review default
	// for a `review: false` step too — a plain bool cannot tell "explicitly off" from
	// "unset". Internal: json/yaml "-" keeps it off the wire and out of request_json,
	// mirroring WorkflowID/StepIndex.
	ReviewFixed bool `json:"-" yaml:"-"`
	// Verify is the job's验证步骤 (SUP-01 B): an argv run AFTER the agent finishes
	// normally (exit 0), on the same machine, in the job's cwd/env — the check that
	// turns "the agent says it worked" into evidence. It is resolved at submit from
	// --verify or the project's `verify` default, so the Forward, request_json and
	// the persisted row all carry ONE decided value (an empty slice = no step, which
	// is what --no-verify leaves behind). The argv is executed verbatim, never through
	// a shell (write `bash -lc '…'` if you need one, exactly like an exec job), and
	// its exit status decides the job's when it fails.
	Verify []string `json:"verify,omitempty" yaml:"verify,omitempty"`
	// VerifyTimeoutSec bounds that step, independently of the job's own deadline
	// (0 = resolved at submit: the project's verify_timeout_sec, else 600s).
	VerifyTimeoutSec int `json:"verify_timeout_sec,omitempty" yaml:"verify_timeout_sec,omitempty"`
	// NoVerify turns the PROJECT's verify default off for this job (SUP-01 B).
	// It only means something when the project declares one; combined with an
	// explicit Verify it is a contradiction and the submit is rejected.
	NoVerify bool `json:"no_verify,omitempty" yaml:"no_verify,omitempty"`
	Cols     int  `json:"cols,omitempty" yaml:"cols,omitempty"`
	Rows     int  `json:"rows,omitempty" yaml:"rows,omitempty"`
	// InitialInput is text the pty runner types into an INTERACTIVE job's stdin
	// once its terminal has settled (session relay §9.1 B: the first message of a
	// `--resume` takeover). Internal: json/yaml "-" keeps it off the wire and out
	// of request_json (it is one-shot text, not a reusable request property); the
	// worker path carries it in wsproto.Dispatch.InitialInput instead. Renaming a
	// request property would be the caller's fault, so the value is never rendered
	// into argv — it goes to the child as terminal INPUT.
	InitialInput string `json:"-" yaml:"-"`
	// InitialInputQuietMs is the quiet window that decides when that text is
	// written (see runner.Request.InitialInputQuietMs). Internal for the same
	// reason; a dispatched worker receives the hub's resolved value.
	InitialInputQuietMs int `json:"-" yaml:"-"`
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
	// TodoID is the client-settable checklist key (SUP-01 C): the plan todo this job
	// is run for. Submit resolves the plan FROM the todo when PlanID is empty (and
	// refuses a contradiction), marks the todo `doing`, and the terminal hooks write
	// the outcome back into its note. Empty == not attached to a checklist item.
	TodoID string `json:"todo_id,omitempty" yaml:"todo_id,omitempty"`
	// TodoForeign marks a todo that belongs to the FORWARDING hub rather than this
	// process's store (set only by the worker's dispatch re-entry): plan todos are
	// hub-managed, so this side displays the id and links nothing — the hub applies
	// the outcome to its own row.
	TodoForeign bool `json:"-" yaml:"-"`
	// SourceJobID is the lineage key set ONLY by ResumeJob / RebuildJob (P5): it points
	// the new job back to its SOURCE job id. Like ResumeSourceAgent (model.go:163) it is
	// json/yaml "-": it is NEVER written from a client body (c.BindJSON can't set it) nor
	// from md frontmatter, and it does NOT round-trip through request_json. UNLIKE
	// ResumeSourceAgent (whose "-" is a SECURITY requirement — it exempts allow_exec),
	// here "-" is chosen to ELIMINATE the forge surface: the URL /jobs/{id}/rebuild
	// already carries the authoritative source id, so the server stamps it internally
	// (submit copies it into the JobResult → jobs.source_job_id via a plain Go assignment,
	// which the json tag does not affect). Empty == not derived.
	SourceJobID string `json:"-" yaml:"-"`
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
	ResumedFrom       string `json:"-" yaml:"-"`
	AutoResumeAttempt int    `json:"-" yaml:"-"`
	// FallbackAgents is the per-JOB candidate list (SUP-01 P3, `job run --fallback`):
	// it wins over the project's agent_fallbacks and over the agent's own
	// fallback_agents. Submit resolves the effective list from it and persists the
	// RESULT (jobs.fallback_json), so a config change while the job runs cannot make
	// the chain drift.
	FallbackAgents []string `json:"fallback_agents,omitempty" yaml:"fallback_agents,omitempty"`
	// NoFallback turns the transfer off for THIS job whatever the config says
	// (SUP-01 P3, `job run --no-fallback`).
	NoFallback bool `json:"no_fallback,omitempty" yaml:"no_fallback,omitempty"`
	// FellBackFrom is the INTERNAL marker of a transfer (SUP-01 P3): the id of the
	// job this one took over from. json/yaml "-" like ResumedFrom — a client cannot
	// claim lineage, and it does not round-trip through request_json.
	FellBackFrom string `json:"-" yaml:"-"`
	// Fallback is the transfer plan this job INHERITS (SUP-01 P3): the candidate list
	// resolved for the chain's root plus how many links are already used, carried
	// verbatim so the whole chain follows ONE frozen plan. Internal (json:"-"); a
	// plain submit has it resolved by Submit from the config.
	Fallback *FallbackState `json:"-" yaml:"-"`
	// RequestedAgent is the agent the CALLER asked for, kept when pre-dispatch
	// substituted a degraded one (SUP-01 P3). Internal: the server stamps it from the
	// request it received, so a client body cannot claim one.
	RequestedAgent string `json:"-" yaml:"-"`
}

// FallbackState is a job's resolved failover plan (SUP-01 P3), persisted as
// jobs.fallback_json at submit time: the ordered candidate agents and how many links
// of the chain are already used. The NEXT candidate is Candidates[Depth]; a Depth of
// len(Candidates) means the chain is exhausted and the failure is simply terminal.
type FallbackState struct {
	Candidates []string `json:"candidates,omitempty"`
	Depth      int      `json:"depth,omitempty"`
}

// next returns the agent that should take the job over, or "" when the chain is
// exhausted (or no candidate was resolved at all).
func (f *FallbackState) next() string {
	if f == nil || f.Depth < 0 || f.Depth >= len(f.Candidates) {
		return ""
	}
	return f.Candidates[f.Depth]
}

// Failure classes (SUP-01 P3). Every FAILED job is classified from the same pattern
// match that decides continuation/transfer, INDEPENDENTLY of whether either is
// enabled — health must reflect the provider, not the job's policy.
const (
	// FailureClassTransient marks a provider-side error (see the agent's
	// transient_error_patterns): a retry in a fresh process may well succeed.
	FailureClassTransient = "transient"
	// FailureClassOther marks every other failure — a real bug, a bad command, a
	// verify step that did not pass. Another agent would not fix it.
	FailureClassOther = "other"
)

// JobResult is the persisted/queryable job state (plan §6.2).
type JobResult struct {
	ID          string `json:"id"`
	ProjectKey  string `json:"project_key"`
	Agent       string `json:"agent"`
	Runner      string `json:"runner"`
	Interactive bool   `json:"interactive,omitempty"`
	// ReadOnly mirrors JobRequest.ReadOnly and is persisted to jobs.read_only (bd
	// h-aii-0ql3): whether THIS job ran under a read-only sandbox, inheritable by a
	// resume and visible in `job show` / the web console after the fact.
	ReadOnly bool `json:"read_only,omitempty"`
	// RequireReview / ReviewedBy / ReviewedAt / ReviewNote are the人工验收 (GATE-01
	// S3) audit fields. RequireReview mirrors JobRequest.Review (resolved: --review or
	// the project's require_review) and stays true after a review, so a finished job
	// still answers "was this delivery gated on a human?". ReviewedBy/At/Note record
	// the DECISION (who accepted/rejected, when, why) and are empty until one is made;
	// ReviewedBy is the caller id ("anonymous" for an empty/allow_empty_token caller,
	// "mcp:<agent>" for the MCP tool). Persisted to jobs.require_review/reviewed_by/
	// reviewed_at/review_note.
	RequireReview bool   `json:"require_review,omitempty"`
	ReviewedBy    string `json:"reviewed_by,omitempty"`
	ReviewedAt    int64  `json:"reviewed_at,omitempty"`
	ReviewNote    string `json:"review_note,omitempty"`
	// TimeoutSec is the EFFECTIVE job deadline in seconds AFTER the configured
	// ceiling clamp (bd h-aii-s9ck), persisted to jobs.timeout_sec so a post-mortem
	// answers "why did my 2h request die at 1h?" without replaying config history.
	// 0 = no deadline (an interactive session submitted without an explicit timeout).
	TimeoutSec int `json:"timeout_sec,omitempty"`
	// RequestedTimeoutSec is the timeout_sec the caller actually asked for (0 =
	// unset, the server default applies). It differs from TimeoutSec only when the
	// ceiling truncated it.
	RequestedTimeoutSec int `json:"requested_timeout_sec,omitempty"`
	// TimeoutClamped reports that the REQUEST was truncated to the ceiling, i.e. the
	// job will be killed earlier than asked. It exists so the clamp is never silent:
	// the CLI warns on submit, and an API/Web caller can see why. A server-side
	// default that merely happens to exceed the ceiling is not a clamp (nothing was
	// requested), so it stays false.
	TimeoutClamped bool `json:"timeout_clamped,omitempty"`
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
	WorkerID string `json:"worker_id,omitempty"`
	// WorkerInstanceID is the PROCESS nonce of the worker connection this job was
	// dispatched to (RECOV-01 R4, wsproto.Register.InstanceID), persisted to
	// jobs.worker_instance_id. It is what makes adoption decidable after a serve
	// restart: a re-registering worker may only take a store-held `recovering` job
	// over when its instance matches this value (the very process that ran it).
	// Empty for local/peer jobs and for jobs dispatched before R4.
	WorkerInstanceID string `json:"worker_instance_id,omitempty"`
	StartedAt        int64  `json:"started_at"`
	EndedAt          int64  `json:"ended_at,omitempty"`
	// RecoveringSince is the unix time the job entered `recovering` (RECOV-01); 0
	// when the job is not (or no longer) recovering. It is persisted so the window
	// a job spent waiting for its worker is visible after the fact in `job show` /
	// the web detail, not just in the live event log.
	RecoveringSince int64 `json:"recovering_since,omitempty"`
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
	// SourceJobID is the lineage key (P5): the source job this one was resumed/rebuilt
	// from. Persisted to jobs.source_job_id (single source of truth — it is NOT in
	// request_json, see JobRequest.SourceJobID). Surfaced in show/list and via
	// ?source_job= for bidirectional lineage nav. Empty == not derived (omitempty).
	SourceJobID string `json:"source_job_id,omitempty"`
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
	// NDJSONKept / NDJSONDropped / NDJSONTruncated 是采集期 NDJSON 投影器的行数审计
	// （bd h-aii-rpky / bd h-aii-525u）：该 job 的 stderr.log 写入了多少行紧凑事件、丢掉了
	// 多少行逐 token 增量事件、有多少行因超过单行上限被截断（标 `…(truncated)`）。文本
	// agent、远端执行（执行机自己计数）或过滤未启用的 job 恒为 0（omitempty 不出现在响应里）。
	NDJSONKept      int `json:"ndjson_kept,omitempty"`
	NDJSONDropped   int `json:"ndjson_dropped,omitempty"`
	NDJSONTruncated int `json:"ndjson_truncated,omitempty"`
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
	// StopReason 是 acp-agent job 的 session/prompt stopReason（ACP-01 S0）：
	// end_turn / max_tokens / max_turn_requests / refusal / cancelled。其余 agent 类型恒为空
	// （omitempty 不出现在响应里）。它解释"agent 为何停下"——尤其是 max_tokens/max_turn_requests
	// 这类 status 仍为 done 的提前结束。持久化 jobs.stop_reason。
	StopReason        string `json:"stop_reason,omitempty"`
	ResumedFrom       string `json:"resumed_from,omitempty"`
	AutoResumeAttempt int    `json:"auto_resume_attempt,omitempty"`
	AutoResumedBy     string `json:"auto_resumed_by,omitempty"`
	// WT-01 受管 worktree：该 job 在独立 worktree 中执行时，记录交付物位置与分支状态。
	// WorktreePath=<repo top>/tmp/gofer/wt/<job-id>（默认保留，分支上的提交即交付物），
	// WorktreeBranch=gofer/<job-id>，WorktreeBaseSHA=基线提交，WorktreeHeadSHA=终态时该分支
	// HEAD（git rev-parse HEAD），CommitsAhead=领先基线的提交数（git rev-list --count
	// base..HEAD）。非 worktree job 全为空/0（omitempty 不出现）；一并入库 jobs 表，供
	// `job show` / web 详情 / `job worktree ls|rm` 与 retention 使用。
	WorktreePath    string `json:"worktree_path,omitempty"`
	WorktreeBranch  string `json:"worktree_branch,omitempty"`
	WorktreeBaseSHA string `json:"worktree_base_sha,omitempty"`
	WorktreeHeadSHA string `json:"worktree_head_sha,omitempty"`
	CommitsAhead    int    `json:"commits_ahead,omitempty"`
	// TodoID is the plan todo this job was submitted for (SUP-01 C); the terminal
	// hooks write the outcome back into that todo. Empty = not a checklist job.
	TodoID string `json:"todo_id,omitempty"`
	// TodoForeign mirrors JobRequest.TodoForeign for a continuation: the todo belongs
	// to the store that submitted the job, not to this one (a worker's local copy),
	// so the continuation must not try to resolve or link it either.
	TodoForeign bool `json:"-"`
	// BaseSHA / Commits are the提交采集 (SUP-01 C): BaseSHA is the commit the job
	// STARTED from — captured on the executing machine when it turns `running`, and
	// for a worktree job simply its baseline — and Commits lists what it produced
	// between that base and HEAD at its terminal, newest first (capped). Both stay
	// empty outside a git checkout / when git is unavailable. Persisted as
	// jobs.base_sha / jobs.commits_json; `job show`, the web detail and the todo
	// note read them.
	BaseSHA string   `json:"base_sha,omitempty"`
	Commits []Commit `json:"commits,omitempty"`
	// Verify is the outcome of the job's verification step (SUP-01 B), nil when the
	// job had none. A failed/timed-out step is why such a job is failed (or parked in
	// needs_review when a human reviews it), so this is the field that explains the
	// status; it is persisted as jobs.verify_json. The type is runner's — the step's
	// result also travels on the remote Outcome channel, and one definition keeps the
	// two in step.
	Verify *VerifyResult `json:"verify,omitempty"`
	// FailureClass is why a FAILED job is classified the way it is (SUP-01 P3):
	// "transient" (a provider error — the health aggregation counts it) or "other".
	// Empty for anything that is not a failure. Persisted as jobs.failure_class.
	FailureClass string `json:"failure_class,omitempty"`
	// FellBackFrom / FellBackTo are the two ends of a failover link (SUP-01 P3):
	// FellBackFrom names the job this one took over from, FellBackTo the job that took
	// THIS one over after it failed. Both empty outside a transfer chain (a resubmitted
	// job never invents one). Persisted as jobs.fell_back_from / fell_back_to.
	FellBackFrom string `json:"fell_back_from,omitempty"`
	FellBackTo   string `json:"fell_back_to,omitempty"`
	// RequestedAgent is the agent the CALLER asked for, kept when Submit substituted a
	// degraded one (SUP-01 P3) and inherited by every takeover on the chain. Empty
	// when the job ran exactly the agent it was submitted with. Persisted as
	// jobs.requested_agent.
	RequestedAgent string `json:"requested_agent,omitempty"`
	// Fallback is the frozen failover plan (candidates + used depth) this job runs
	// under (SUP-01 P3), resolved once at submit and carried by every takeover.
	// Persisted as jobs.fallback_json; nil for a job with no candidates.
	Fallback *FallbackState `json:"fallback,omitempty"`
}

// VerifyResult / the verify statuses are the runner package's types, aliased here:
// the same value describes a local run and a worker's回传 result, and job already
// imports runner. See runner.VerifyResult for the field meanings.
type VerifyResult = runner.VerifyResult

// Verify step statuses (mirrored from runner so callers spell job.VerifyPassed).
const (
	VerifyPassed  = runner.VerifyPassed
	VerifyFailed  = runner.VerifyFailed
	VerifyTimeout = runner.VerifyTimeout
	VerifySkipped = runner.VerifySkipped
)

// Commit is one commit a job produced (SUP-01 C): the abbreviated sha git prints
// and its subject line. It is what the todo note quotes and the JobDetail block
// lists.
type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
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
	// StatusRecovering (RECOV-01) is a NON-terminal holding state: a worker job
	// whose worker connection dropped but whose worker process may still be running
	// it. It is entered when the hub suspends the job's sink (the connection died
	// with the job in flight and recovery is enabled) and left either back to
	// StatusRunning (the same worker process reconnected and proved it still has the
	// job) or to StatusFailed / the job's own terminal state (the recovery window
	// expired → worker_lost, or the worker replayed the real Result). A recovering
	// job keeps its timeout running: a job that hangs in recovering still ends by
	// timeout. Appended to the END of the enum so existing values never shift.
	StatusRecovering = "recovering"
	// StatusNeedsReview (GATE-01 S3) is the人工验收 holding state: the agent finished
	// NORMALLY (a `done` run of a job that asked for review) but the delivery is not
	// accepted yet. It is NON-terminal (IsTerminal false): retention must not evict
	// the job a human still has to rule on, and `job resume` demands accept/reject
	// first. The PROCESS is over, though, so the finished-vs-live question is answered
	// by IsFinished (true here) — that is what closes the log stream/SSE, evicts the
	// in-memory entry and keeps crash recovery from treating it as a running job.
	StatusNeedsReview = "needs_review"
	// StatusRejected (GATE-01 S3) is the terminal outcome of a human REJECTING a
	// needs_review job: the work was delivered but not accepted. It is terminal
	// (retention collects it; a workflow step aggregates it as a failure, like failed)
	// — but it never triggers a job-level retry or an automatic continuation, because
	// only a human's `reject --resume` decides whether the work is continued.
	// Appended to the END of the enum so existing values never shift.
	StatusRejected = "rejected"
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
	// EventJobToolCall is an acp-agent's tool call reaching a new status
	// (ACP-01 S0): {tool_call_id,title,kind,status}. Recorded by the acp runner
	// through runner.Request.OnJobEvent; content-only refreshes are not events.
	EventJobToolCall = "job.tool_call"
	// EventJobNeedsReview is a reviewed job finishing NORMALLY and parking for人工
	// 验收 (GATE-01 S3): {job_id, exit_code}. It REPLACES job.terminal for that
	// transition (the job is not终态 yet), and it is a notification DEFAULT trigger
	// (it is a "a human must act" signal, exactly like interaction.created).
	EventJobNeedsReview = "job.needs_review"
	// EventJobReviewed is a human's accept/reject decision on a needs_review job:
	// {verdict, by, note, resume_job_id?}. It is followed by the job.terminal event of
	// the state the decision produced (done / rejected).
	EventJobReviewed = "job.reviewed"
	// EventJobInputInjected is the pty runner writing priming text into an
	// interactive child's stdin once its terminal settled (session relay §9.1 B):
	// {bytes, written, quiet_ms, error?}. The literal lives in the runner package
	// (which emits it and cannot import this one — G022); it is aliased here so the
	// job event vocabulary has ONE definition and a caller may read it by name.
	EventJobInputInjected = runner.EventInputInjected
	// EventJobVerifyStarted is the verify step starting on the executing machine:
	// {command} (the argv, space-joined for reading). Recorded just before the child
	// is spawned, so a step that hangs is visible as a started-without-finished pair.
	EventJobVerifyStarted = "job.verify_started"
	// EventJobVerifyFinished is that step ending: {status, exit_code, duration_ms}
	// (status is passed|failed|timeout|skipped) — the evidence behind the job's own
	// terminal status, and the event a notification subscriber watches for a red build.
	EventJobVerifyFinished = "job.verify_finished"
	// The permission events are emitted by the acp runner (internal/runner/acp), whose
	// gated calls cannot reach this package (G022). Their literals live in the runner
	// package — the same single-definition rule as EventJobInputInjected — and are
	// aliased here for the mirror whitelist and for callers that read them by name.
	EventJobPermissionRequested = runner.EventPermissionRequested
	EventJobPermissionAnswered  = runner.EventPermissionAnswered
	EventJobPermissionTimedOut  = runner.EventPermissionTimedOut
	// EventJobFellBack is a transient failure taken over by the next candidate agent
	// (SUP-01 P3): {to_job, agent, reason} where reason is the matched provider-error
	// text. It REPLACES job.terminal for that transition (the failure is about to be
	// continued by another agent, so an IM subscriber must not be told it is over) —
	// exactly like job.auto_resumed; when the takeover cannot be submitted, the
	// job.terminal event is recorded late instead.
	EventJobFellBack = "job.fell_back"
	// EventJobAgentSubstituted is a degraded agent replaced BEFORE dispatch
	// (SUP-01 P3, server.agent_fallback.pre_dispatch): {from, to, reason}. The job row
	// keeps the requested agent in requested_agent, so "why did this run on another
	// agent?" is answered from the job itself.
	EventJobAgentSubstituted = "job.agent_substituted"
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

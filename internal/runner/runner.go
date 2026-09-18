// Package runner abstracts where a job's process executes. The MVP ships the
// local runner (internal/runner/local); the peer-http runner
// (internal/runner/peerhttp), added in P7, forwards a job to a peer bridge.
// See plan §6.3, §9 (P4) and §11.1 (P7).
package runner

import (
	"context"
	"encoding/json"
	"io"

	"github.com/inhere/gofer/internal/config"
)

// EventInputInjected is the job event a runner records when it wrote priming
// input into an interactive child's stdin (session relay §9.1 B). It lives here —
// not in the job package — because the runner emits it and the runner package
// cannot import job (G022); job.EventJobInputInjected aliases it, so the literal
// has exactly one definition.
const EventInputInjected = "job.input_injected"

// The approval-gate job events (GATE-01 §1): recorded by the acp runner when a
// session/request_permission is raised, answered (by a human or automatically) or
// timed out. They live here for the same reason as EventInputInjected — the acp
// runner cannot import job — and, since SUP-01 G, also because the job package's
// worker→hub mirror whitelist must name them without a second literal that could
// drift. job.EventJobPermission* alias these.
const (
	EventPermissionRequested = "job.permission_requested"
	EventPermissionAnswered  = "job.permission_answered"
	EventPermissionTimedOut  = "job.permission_timed_out"
)

// Runner executes one resolved command and reports how it ended.
type Runner interface {
	// Name returns the runner's stable identifier (e.g. "local").
	Name() string
	// Run executes req under ctx and returns the exit code and any error.
	Run(ctx context.Context, req Request) Result
}

// Request is a ready-to-execute command for a runner.
//
// Stdout/Stderr meaning differs by runner:
//   - The LOCAL runner wires these io.Writers directly to the child process so
//     the job service can stream output straight into stdout.log / stderr.log as
//     it is produced.
//   - REMOTE runners (peer-http, P7) do NOT resolve a local command. They
//     re-submit the original request (carried in Forward) to a peer bridge and
//     MIRROR the peer's log stream back into these same writers, so the local
//     job's stdout.log / stderr.log (and thus /logs, /stream and list) stay
//     transparently usable for the proxied job. Command/Args/WorkDir are unset
//     for remote runners (the peer resolves them with its own config). A remote
//     runner additionally uses Interactions (an InteractionSink) to bridge the
//     peer's running-job interactions (P9) onto the HOST job.
//
// Forward is nil for local jobs (the local runner ignores it) and set by the
// job service for remote-runner jobs.
type Request struct {
	JobID   string
	WorkDir string // absolute host dir; the job service supplies a SafeJoin'd path
	Command string
	Args    []string
	Env     map[string]string // agent-config env, layered over the process env
	Stdout  io.Writer         // child stdout sink / mirrored remote stdout (see type doc)
	Stderr  io.Writer         // child stderr sink / mirrored remote stderr (see type doc)

	// Interactive requests a pty-backed run. Cols/Rows are the initial terminal
	// size in character cells; zero values let the runner apply its defaults.
	Interactive bool
	Cols        int
	Rows        int

	// InitialInput is text the pty runner writes to the interactive child's stdin
	// once its terminal is ready — after the FIRST output and then a quiet window,
	// so a TUI has drawn its prompt before the text arrives (session relay §9.1 B:
	// the takeover job's first message). "" = nothing is written. Only the pty
	// runner reads it; every other runner ignores it.
	InitialInput string
	// InitialInputQuietMs is that quiet window in milliseconds (0 = the runner's
	// default, resolved from session.takeover_input_delay_ms by the job service).
	InitialInputQuietMs int

	// Forward carries the original (pre-resolution) request a remote runner
	// re-submits to a peer bridge. Nil for local jobs.
	Forward *Forward

	// Verify / VerifyTimeoutSec are the job's验证步骤 (SUP-01 B), already resolved by
	// the job service (project default + --no-verify). The LOCAL runner does not read
	// them: the job service runs the step itself right after Run returns, on this
	// machine and in this WorkDir/Env — which is why they are on the request the job
	// service built rather than on the Forward (a remote job's step runs on the
	// EXECUTING machine and travels in Forward).
	Verify           []string
	VerifyTimeoutSec int

	// Interactions bridges a peer's running-job interactions onto the host job.
	// Nil for local jobs; set by the job service for remote runners so a peer's
	// interactions surface on the host job (see InteractionSink).
	Interactions InteractionSink

	// Approvals lets a runner RAISE an approval request of its own — an interaction of
	// type permission — and block until it is answered (GATE-01 §1). The acp runner
	// uses it for a tool call the project's approval policy will not auto-approve; nil
	// means the executing side has no interaction surface, and a runner that needs one
	// must refuse to run the gated action rather than approve it silently.
	Approvals ApprovalSink

	// OnRendered (nil-safe) is invoked by a remote runner with the rendered command
	// as soon as the executing machine reports it, so the host can show WHAT is
	// running immediately (G1). The host cannot render a remote agent's argv itself
	// (it lacks that worker's agent config), so a worker/peer reports it out-of-band.
	// Local runs set the rendered command inline and leave this nil.
	OnRendered func(rendered string)

	// OnSuspend (nil-safe) is invoked by a remote runner when the executing machine's
	// connection dropped but the job is being HELD for a possible reconnect (RECOV-01)
	// instead of failed: the host job moves to `recovering` and keeps its timeout
	// running. Local runs leave it nil.
	OnSuspend func(reason string)
	// OnResume (nil-safe) is called when that connection came back and the executing
	// machine still runs the job: the host job returns to `running` (RECOV-01). It
	// must be safe to call after a terminal signal (it is a no-op then). Local runs
	// leave it nil.
	OnResume func()

	// OnDispatchedWorker (nil-safe) is invoked by a remote runner the moment it has
	// committed the job to a resolved target — after the sink is registered and the
	// worker proven online, before the dispatch frame is written (RECOV-01 R4). It
	// carries the RESOLVED worker_id (explicit, label-selected, or the runner's
	// configured default — the D4 fallback the request itself never names) plus that
	// connection's process instance id, so the host row records which worker PROCESS
	// owns the job. Only the same instance may later ADOPT the job after a serve
	// restart. Local runs leave it nil.
	OnDispatchedWorker func(workerID, instanceID string)

	// ACP carries the inputs of an acp-agent job (prompt, event-stream directory,
	// MCP servers, permission policy). It is set by the job service when the
	// resolved agent is type acp-agent, and the acp runner is the only runner that
	// reads it. Nil for every other job.
	ACP *ACPRequest

	// OnJobEvent (nil-safe) lets a runner record a job lifecycle event (e.g.
	// job.tool_call from an acp-agent's tool-call status change) on the job. The job
	// service wires it to its event log; the runner never touches the store itself.
	OnJobEvent func(eventType string, detail map[string]any)
}

// ACPRequest is the acp-agent payload of a runner.Request: everything the acp
// runner needs beyond the process argv (Command/Args/WorkDir/Env) and the log
// writers.
type ACPRequest struct {
	// Prompt is the turn's prompt text (sent as one text content block).
	Prompt string
	// ResultDir is the job's result directory; the runner writes its structured
	// event stream to <ResultDir>/artifacts/acp.jsonl.
	ResultDir string
	// Approval is the RESOLVED approval policy of this job (GATE-01 §1): the project's
	// approval block tightened by the agent's acp.permission_policy
	// (config.Config.EffectiveApproval), defaults applied. It is the runner's ONLY
	// input for session/request_permission — the agent's raw permission_policy is
	// folded in by the resolver, so a stricter policy can never be lost in transit —
	// and Mode off reproduces the S0 auto-allow.
	Approval config.ApprovalConfig
	// MCPServers are advertised to the agent in session/new.
	MCPServers []ACPMCPServer
	// LoadSessionID, when non-empty, is the agent session this job CONTINUES: the
	// runner calls session/load{sessionId} instead of session/new (ACP-01 S2). It is
	// filled only for a resume (JobRequest.ResumedFrom set) — a plain job that carries
	// a session_id binds to it without replaying the agent's history. An agent that
	// does not advertise agentCapabilities.loadSession fails the job rather than
	// silently running the prompt in a fresh, context-free session.
	LoadSessionID string
	// ReadOnlyModeID, when non-empty, switches the session into this agent mode
	// (session/set_mode) BEFORE the prompt turn (bd h-aii-0ql3): it is the operator's
	// acp.modes.read_only mapping, and admission has already refused the job when the
	// agent maps none. When the session reports availableModes and this id is not
	// among them, the run fails instead of prompting in a writable mode.
	ReadOnlyModeID string
}

// ACPMCPServer is one MCP server advertised to an acp-agent through session/new.
type ACPMCPServer struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

// ApprovalOption is one choice offered for an approval request, mirroring an ACP
// permission option (session/request_permission.options).
type ApprovalOption struct {
	// ID is the ACP optionId — what an answer selects.
	ID string
	// Label is the agent's human-facing name for the option.
	Label string
	// Kind is the ACP option kind: allow_once|allow_always|reject_once|reject_always.
	Kind string
}

// ApprovalToolCall is the tool call an approval request is about (ACP toolCall,
// reduced to what an approver needs).
type ApprovalToolCall struct {
	ID              string
	Title           string
	Kind            string
	Locations       []string
	RawInputSummary string
}

// ApprovalRequest is one approval gate request (GATE-01 §1): the tool call an agent
// wants to run, the options the agent offered for it, and why the gate stopped it.
type ApprovalRequest struct {
	// Prompt is the question shown to the approver.
	Prompt string
	// Options are the agent's own options, verbatim and in its order.
	Options []ApprovalOption
	// ToolCall is the gated tool call (nil when the agent sent none).
	ToolCall *ApprovalToolCall
	// PolicyHint is the human-readable rationale, e.g. "ask: kind=edit".
	PolicyHint string
	// TimeoutSec is how long this request may wait for an answer (0 = no deadline).
	// The sink stamps it onto the raised interaction so an answerer can show a
	// countdown: the deadline is the one the runner will actually honour.
	TimeoutSec int
	// OnRaised (nil-safe) is invoked EXACTLY ONCE, synchronously, with the interaction
	// id as soon as the request is visible to approvers — before the call starts
	// waiting. That is where a caller records its "a human is needed" event or
	// notification; emitting it after the wait would announce an approval nobody can
	// act on any more.
	OnRaised func(interactionID string)
}

// ApprovalOutcome is the approver's decision.
type ApprovalOutcome struct {
	// Answer is the chosen optionId ("" when the request was cancelled instead of
	// answered — the caller decides what a cancellation means).
	Answer string
	// By is the interaction's answered_by attribution (human / agent:<id> /
	// auto:<policy>); "" for an unattributed or cancelled answer.
	By string
}

// ApprovalSink raises an approval request on a job and blocks until it is answered or
// ctx ends. The implementation is the job service (an interaction of type permission);
// the interface lives here so runner implementations need not import job. A ctx that
// ends (the gate's own deadline or the job's) MUST leave no pending interaction behind.
type ApprovalSink interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) (ApprovalOutcome, error)
}

// RemoteInteractionOption mirrors a peer interaction option without importing the
// job package (runner must stay cycle-free: job imports runner).
type RemoteInteractionOption struct {
	Value string
	Label string
	// ID / Kind carry a permission interaction's ACP option identity and kind
	// (GATE-01 §1); "" for every other interaction.
	ID   string
	Kind string
}

// RemoteInteraction is a peer-raised interaction a remote runner surfaces to the
// host job via an InteractionSink. Fields mirror job.Interaction's wire shape.
type RemoteInteraction struct {
	ID      string
	Type    string
	Prompt  string
	Options []RemoteInteractionOption
	// ToolCall / PolicyHint carry a type=permission interaction's approval detail
	// (GATE-01 §1); nil/"" for every other interaction, as does ExpiresAt (the
	// approval deadline, unix seconds, 0 = none).
	ToolCall   *RemoteInteractionToolCall
	PolicyHint string
	ExpiresAt  int64
}

// RemoteInteractionToolCall mirrors job.InteractionToolCall for the same
// cycle-free reason as RemoteInteractionOption.
type RemoteInteractionToolCall struct {
	ID              string
	Title           string
	Kind            string
	Locations       []string
	RawInputSummary string
}

// InteractionSink lets a remote runner bridge a peer's running-job interactions
// into the HOST job: Open records the interaction on the host job (host ->
// pending_interaction) and returns a channel delivering the host-side answer once
// the user answers it; the runner then forwards that answer to the peer. The
// channel is closed WITHOUT a value if the host job ends / ctx is cancelled before
// an answer. Open must be idempotent-safe for a repeated interaction id.
type InteractionSink interface {
	Open(ctx context.Context, it RemoteInteraction) (<-chan string, error)
}

// Forward carries the original (pre-resolution) request a remote runner needs to
// re-submit to a peer bridge (peer resolves agent/cwd/command with its own
// config). Nil for local jobs; the local runner ignores it.
type Forward struct {
	ProjectKey   string
	Agent        string
	PeerRunner   string // runner to use on the peer; default "local"
	Prompt       string
	AgentArgs    []string
	SystemPrompt string
	Cmd          []string
	Cwd          string // ORIGINAL relative cwd; peer SafeJoins against ITS project
	// Worktree (WT-01) asks the EXECUTING machine to run the job in a managed git
	// worktree of ITS project checkout (the peer SafeJoins Cwd against its own
	// project root and then maps it into the worktree). WorktreeBase is the base ref
	// (empty = the executing checkout's HEAD). The project-level worktree_default is
	// already resolved into Worktree by the submitting service, so the executor never
	// re-derives a default from its own config.
	Worktree     bool
	WorktreeBase string
	TimeoutSec   int
	Interactive  bool
	Cols         int
	Rows         int
	// InitialInput / InitialInputQuietMs carry path B's priming text and quiet
	// window to the EXECUTING machine (session relay §9.1 B): the pty is started
	// there, so the text and the window the hub resolved must travel with the
	// dispatch instead of being re-derived from the worker's own config.
	InitialInput        string
	InitialInputQuietMs int
	// ResumeSourceAgent is an internal resume marker carried over trusted worker
	// dispatch so the worker validates the exec resume carrier against the source
	// agent. It is not part of the public HTTP job contract.
	ResumeSourceAgent string
	// SessionID / ResumedFrom (ACP-01 S2) carry a continuation to a remote executor:
	// ResumedFrom is the source job id (set only by ResumeJob) and SessionID the agent
	// session to LOAD. A remote runner sends them over Dispatch only when the request
	// really is a resume — a plain job that merely carries a session_id is not one.
	SessionID   string
	ResumedFrom string
	// ReadOnly (bd h-aii-0ql3) asks the remote executor to run the job in its own
	// read-only mode (cli-agent argv sandbox / acp-agent session/set_mode). The
	// executor validates it against ITS agent config, so an agent without a read-only
	// mode fails there with the explanation the host could not have produced.
	ReadOnly bool
	// WorkerID is the resolved target worker for a runner=worker job (P2 dynamic
	// routing): the explicit req.WorkerID or the one auto-selected from labels.
	// Empty for peer-http forwards and for worker jobs that rely on the runner's
	// configured default worker (D4 fallback).
	WorkerID string
	// TodoID (SUP-01 C) is the hub's checklist item for this job, carried so the
	// execution machine's job row can DISPLAY it. The todo itself lives in the
	// hub's store, so the linkage is the hub's alone (JobRequest.TodoForeign) — a
	// peer re-submits without it and links its own nothing.
	TodoID string
	// Verify / VerifyTimeoutSec are the job's验证步骤 (SUP-01 B): the argv to run
	// AFTER the agent finishes normally, and its own deadline in seconds. The
	// EXECUTING machine runs it in its checkout with the job's env, because that is
	// where the work happened — the hub has no checkout to verify. Empty = no step
	// (an old hub never sets them, and an old worker ignores them; the hub refuses
	// the dispatch instead, see wsproto.VerifyMinProtocolVersion).
	Verify           []string
	VerifyTimeoutSec int
}

// Result is the outcome of a single Run. ExitCode is the process exit status
// (or a synthetic non-zero when the process could not start / was killed); Err
// carries the underlying error when execution failed or the context ended. The
// job service maps ExitCode/Err plus the context reason to a job status.
//
// Outcome is the P4 remote-capture channel: a LOCAL runner leaves it nil (the
// job service then captures产出 from its own result dir / DB — P1–P3). A REMOTE
// runner (worker / peer-http) executed the job on another machine, so it carries
// the产出 captured there back to the host job here; the job service detects a
// non-nil Outcome and applies it directly instead of scanning a (远端) result dir
// the host does not own (design §6.6 / D6).
type Result struct {
	ExitCode int
	Err      error
	// Outcome, when non-nil, carries产出 captured on a remote execution machine
	// (worker / peer). Nil for local jobs. See Outcome doc.
	Outcome *Outcome
	// SessionID is a session identifier an execution-local runner learned out of
	// band from the process it ran — today the acp runner's sessionId, which the job
	// row records so `job resume` has a uniform entry point. Empty when the runner
	// has none (local, remote: a remote's session id travels in Outcome).
	SessionID string
	// StopReason is the acp runner's session/prompt stopReason. It is empty for
	// runners with no such notion; the job row records it when set.
	StopReason string
	// Usage is the token/cost accounting an execution-local runner learned from the
	// process it ran — today the acp runner's accumulated usage_update tally
	// (SUP-01 E). Nil when the runner has none (local exec, and the ndjson/codex
	// captures, which run inside the job service rather than in a runner).
	Usage *Usage
}

// Outcome is the产出与审计 payload a REMOTE runner回传 from the execution machine
// to the host job (P4, design §6.6). v1 carries only清单+小结果: the rendered
// command, the structured result.json, the diff摘要 and the artifacts清单
// METADATA — the大产物文件本身留 worker 侧/共享盘, NOT inlined here (D6).
//
// Artifacts is carried as raw JSON (the `[]ArtifactItem` manifest the execution
// machine already serialised) so the runner package stays a cycle-free leaf: the
// job package owns ArtifactItem and imports runner, never the reverse. The job
// service applies it verbatim into the jobs.artifacts_json column.
type Outcome struct {
	RenderedCommand string          `json:"rendered_command,omitempty"`
	ResultJSON      string          `json:"result_json,omitempty"`
	DiffSummary     string          `json:"diff_summary,omitempty"`
	Artifacts       json.RawMessage `json:"artifacts,omitempty"` // []ArtifactItem 清单元数据(JSON)
	// Source marks WHERE the job actually ran: "worker:<id>" or "peer:<name>"
	// (empty for local). It is persisted (jobs.source) and surfaced so the详情
	// can标注 "在 worker w-xxx / peer X 执行" (P4-c).
	Source string `json:"source,omitempty"`
	// SessionID is the底层 agent CLI 会话标识 the EXECUTION machine captured/injected
	// for this job (P3). The remote runner (worker/peer) ran its own P1
	// captureOutcomes against its local JobResult, so this carries that machine's
	// SessionID back to the host (applyOutcome → entry.result.SessionID), enabling
	// `job resume` / `list --session` for remotely-executed jobs. Empty = the
	// remote produced no session id (unsupported agent / not captured).
	SessionID string `json:"session_id,omitempty"`
	// WT-01: the managed worktree the job ran in — recorded on the EXECUTION machine
	// (it owns that path), so a host row for a runner=worker/peer job still shows
	// where the deliverable branch lives. All empty/0 for a non-worktree job and for
	// an old worker that never sends them (host fields stay empty).
	WorktreePath    string `json:"worktree_path,omitempty"`
	WorktreeBranch  string `json:"worktree_branch,omitempty"`
	WorktreeBaseSHA string `json:"worktree_base_sha,omitempty"`
	WorktreeHeadSHA string `json:"worktree_head_sha,omitempty"`
	CommitsAhead    int    `json:"commits_ahead,omitempty"`
	// BaseSHA / Commits are the SUP-01 C提交采集: the commit the job started from
	// and the commits it produced (newest first), captured on the execution machine
	// — the host has no checkout there, so both travel with the outcome.
	BaseSHA string   `json:"base_sha,omitempty"`
	Commits []Commit `json:"commits,omitempty"`
	// Verify is the job's验证步骤 result (SUP-01 B). The step runs on the EXECUTION
	// machine (it owns the checkout being verified), so its structured result travels
	// back here; nil = the job had no verify step (or the worker predates it — the
	// hub refuses such a dispatch rather than running a step it can no longer trust).
	Verify *VerifyResult `json:"verify,omitempty"`
	// Usage is the token/cost accounting the EXECUTION machine captured for this job
	// (SUP-01 E): an agent's own numbers live in the log stream / stderr tail over
	// there, so they travel with the outcome like the commits and the verify verdict.
	// Nil = that machine captured none.
	Usage *Usage `json:"usage,omitempty"`
}

// VerifyResult is the outcome of one job's verify step (SUP-01 B): the argv that
// ran, how it ended and how long it took. It lives in the runner package — not in
// job — because it is carried across the wire by Outcome (job imports runner, so the
// job package aliases this type: job.VerifyResult).
type VerifyResult struct {
	// Command is the argv as submitted (element-wise, never a shell join).
	Command []string `json:"command,omitempty"`
	// Status is passed|failed|timeout|skipped.
	Status string `json:"status"`
	// ExitCode is the step's exit status (-1 when it did not end on its own: a
	// timeout, or a job cancelled while the step ran). 0 for skipped/passed.
	ExitCode int `json:"exit_code"`
	// DurationMs is how long the step ran, in milliseconds (0 for skipped).
	DurationMs int64 `json:"duration_ms"`
	// Reason explains a non-obvious status, e.g. why the step was skipped
	// ("agent failed") or why it timed out.
	Reason string `json:"reason,omitempty"`
}

// Verify step statuses (VerifyResult.Status).
const (
	// VerifyPassed: the step ran and exited 0 — the job keeps its status.
	VerifyPassed = "passed"
	// VerifyFailed: the step ran and exited non-zero — the job fails (or parks in
	// needs_review when a human is asked to rule on the delivery).
	VerifyFailed = "failed"
	// VerifyTimeout: the step exceeded its own deadline and was killed.
	VerifyTimeout = "timeout"
	// VerifySkipped: the step never ran because the agent did not finish normally
	// (failed / cancelled / timed out) — the agent's outcome is the job's outcome.
	VerifySkipped = "skipped"
)

// Commit is one commit the execution machine captured for a job (SUP-01 C):
// abbreviated sha + subject line, as `git log --oneline` prints them.
type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

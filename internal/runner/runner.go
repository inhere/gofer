// Package runner abstracts where a job's process executes. The MVP ships the
// local runner (internal/runner/local); the peer-http runner
// (internal/runner/peerhttp), added in P7, forwards a job to a peer bridge.
// See plan §6.3, §9 (P4) and §11.1 (P7).
package runner

import (
	"context"
	"encoding/json"
	"io"
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

	// Forward carries the original (pre-resolution) request a remote runner
	// re-submits to a peer bridge. Nil for local jobs.
	Forward *Forward

	// Interactions bridges a peer's running-job interactions onto the host job.
	// Nil for local jobs; set by the job service for remote runners so a peer's
	// interactions surface on the host job (see InteractionSink).
	Interactions InteractionSink
}

// RemoteInteractionOption mirrors a peer interaction option without importing the
// job package (runner must stay cycle-free: job imports runner).
type RemoteInteractionOption struct {
	Value string
	Label string
}

// RemoteInteraction is a peer-raised interaction a remote runner surfaces to the
// host job via an InteractionSink. Fields mirror job.Interaction's wire shape.
type RemoteInteraction struct {
	ID      string
	Type    string
	Prompt  string
	Options []RemoteInteractionOption
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
	ProjectKey  string
	Agent       string
	PeerRunner  string // runner to use on the peer; default "local"
	Prompt      string
	Cmd         []string
	Cwd         string // ORIGINAL relative cwd; peer SafeJoins against ITS project
	TimeoutSec  int
	Interactive bool
	Cols        int
	Rows        int
	// WorkerID is the resolved target worker for a runner=worker job (P2 dynamic
	// routing): the explicit req.WorkerID or the one auto-selected from labels.
	// Empty for peer-http forwards and for worker jobs that rely on the runner's
	// configured default worker (D4 fallback).
	WorkerID string
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
}

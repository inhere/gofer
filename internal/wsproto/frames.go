package wsproto

import "encoding/json"

// Protocol versioning. The wire version is NOT the gofer release version; it only
// describes worker↔hub capability-frame compatibility.
//
// The floor and the implemented version are deliberately TWO constants. Collapsing
// them into one makes a rolling upgrade impossible: bumping the single constant to
// ship a new frame instantly turns every already-deployed worker into "too old", so
// the next reconnect (one network blip is enough) evicts a fleet that was working
// fine. Splitting them lets a hub say "I implement N, but I still register anyone at
// or above M": shipping a new frame only raises Current, and dropping support for an
// old fleet is then a separate, deliberate decision that raises Min.
const (
	// MinProtocolVersion is the lowest version the hub still accepts at registration
	// (the compatibility floor). v2 is the floor because it is where the worker's
	// capability report (AgentCaps) became authoritative for validation and routing:
	// a worker below it (pre-federation workers report 0 — field absent) cannot be
	// trusted for that, so it is rejected with an upgrade prompt.
	MinProtocolVersion = 2

	// CurrentProtocolVersion is the version THIS build implements; a worker reports it
	// on register and the hub uses the reported value to negotiate OPTIONAL features
	// per peer (see ReloadMinProtocolVersion). It must never be used as the
	// registration gate — that is MinProtocolVersion's job.
	CurrentProtocolVersion = 3
)

// ReloadMinProtocolVersion is the first protocol version that carries the config
// hot-reload frames. A worker registered below it stays fully usable for everything
// else; it simply cannot be asked to reload, so the caller must check SupportsReload
// with the version the peer reported and surface an explicit "worker too old" error
// instead of sending a frame the peer will ignore.
const ReloadMinProtocolVersion = 3

// SupportsReload reports whether a peer that registered with protocol version proto
// implements the hot-reload frames. It is the single place that knows which version
// gained the capability — callers must not compare version numbers themselves.
func SupportsReload(proto int) bool { return proto >= ReloadMinProtocolVersion }

// AgentBrief is a worker-reported agent capability with the detail the UI cascade
// needs (type/interactive) beyond a bare key. Federation: the worker is the
// authority for ITS agents' type/interactive (the server may not have them in its
// own config).
type AgentBrief struct {
	Key         string `json:"key"`
	Type        string `json:"type,omitempty"`
	Interactive bool   `json:"interactive,omitempty"`
}

// Register (w→s, P1): the worker announces its identity + capability snapshot on
// connect. The hub validates worker_id against the token binding (review #1) AND
// rejects a worker whose ProtocolVersion < wsproto.MinProtocolVersion (hard
// incompatibility + upgrade prompt). AgentCaps/Projects are now AUTHORITATIVE
// for validation+routing (was display-only); the worker still re-validates locally
// on dispatch (review #8).
type Register struct {
	WorkerID string `json:"worker_id"`
	// InstanceID is a per-PROCESS nonce minted once at worker start and reused
	// across reconnects. It lets the hub tell a transient network reconnect (same
	// instance → in-flight jobs survive, supersede exemption applies) from a worker
	// RESTART (new instance under the same worker_id → the old process's in-flight
	// jobs died with it and must be failed, not exempted). Empty on old workers →
	// the hub falls back to the legacy supersede-always behaviour (z8ow).
	InstanceID string `json:"instance_id,omitempty"`
	// ProtocolVersion is the worker's capability-frame version (0 = pre-federation
	// worker → below the floor, rejected by the hub gate). A worker sets it to
	// wsproto.CurrentProtocolVersion — the version IT implements, which the hub keeps
	// per connection to negotiate optional features (it may be older than the hub's).
	ProtocolVersion int      `json:"protocol_version,omitempty"`
	PtyCapable      bool     `json:"pty_capable,omitempty"`
	OS              string   `json:"os,omitempty"`
	Arch            string   `json:"arch,omitempty"`          // runtime.GOARCH
	GoferVersion    string   `json:"gofer_version,omitempty"` // buildinfo.DisplayVersion
	StartedAt       int64    `json:"started_at,omitempty"`    // worker process start, unix sec
	Labels          []string `json:"labels,omitempty"`
	Projects        []string `json:"projects,omitempty"`
	// Agents stays the bare key list (validation / selector, back-compat); AgentCaps
	// carries the typed detail (type/interactive) the UI cascade needs. A new worker
	// sends BOTH (the key redundancy is accepted; Agents is dropped once every
	// consumer reads AgentCaps).
	Agents        []string     `json:"agents,omitempty"`
	AgentCaps     []AgentBrief `json:"agent_caps,omitempty"`
	MaxConcurrent int          `json:"max_concurrent,omitempty"`
}

// Registered (s→w, P1): handshake ack. ServerTime is in milliseconds (SR102, in
// line with the /v1 envelope convention).
type Registered struct {
	Accepted   bool   `json:"accepted"`
	Reason     string `json:"reason,omitempty"`
	ServerTime int64  `json:"server_time"`
}

// Dispatch (s→w, P1): a job assignment = JobRequest projection. Runner is always
// "local" (the worker executes locally with its own config). worker_id is NOT
// carried — the worker already knows it is itself.
type Dispatch struct {
	JobID             string   `json:"job_id"`
	ProjectKey        string   `json:"project_key"`
	Agent             string   `json:"agent"`
	Runner            string   `json:"runner"`
	Prompt            string   `json:"prompt,omitempty"`
	AgentArgs         []string `json:"agent_args,omitempty"`
	SystemPrompt      string   `json:"system_prompt,omitempty"`
	Cmd               []string `json:"cmd,omitempty"`
	Cwd               string   `json:"cwd,omitempty"`
	TimeoutSec        int      `json:"timeout_sec,omitempty"`
	Interactive       bool     `json:"interactive,omitempty"`
	Cols              int      `json:"cols,omitempty"`
	Rows              int      `json:"rows,omitempty"`
	ResumeSourceAgent string   `json:"resume_source_agent,omitempty"`
	RelayNonce        string   `json:"relay_nonce,omitempty"`
	// PtySessionID is the host-minted relay session id the worker echoes back in
	// its pty-connect hello so the serve endpoint can strong-check it against the
	// binding (httpapi/pty_connect_handler; D-P2-4). Empty on non-interactive.
	PtySessionID string `json:"pty_session_id,omitempty"`
}

// Log (w→s, P1): an incremental log frame. Seq is monotonic per job (the same
// notion as the C4 SSE seq), giving the hub an ordering baseline.
type Log struct {
	JobID  string `json:"job_id"`
	Stream string `json:"stream"` // "stdout" | "stderr"
	Seq    int    `json:"seq"`
	Text   string `json:"text"`
}

// Status (w→s, P1): an optional status hint. result is the authoritative
// terminal state; the hub records status but does not drive the terminal flip
// from it (WP1).
type Status struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

// Result (w→s, P1): the authoritative terminal outcome for a job.
type Result struct {
	JobID    string `json:"job_id"`
	Status   string `json:"status"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

// Outcome (w→s, P4): the产出与审计 payload the worker captured locally for a job,
// sent JUST BEFORE the terminal Result frame so the host can apply it before
// finishing the job (design §6.6 / D6). v1 carries only清单+小结果: rendered
// command / structured result.json / diff摘要 / artifacts清单 METADATA — the大
// 产物文件本身留 worker 侧（不进帧）. Artifacts stays raw JSON so wsproto need not
// import job (job owns ArtifactItem). The frame is OPTIONAL: an old worker that
// never sends it leaves the host job outcome empty (回归红线, the hub's read loop
// safely ignores an unknown opcode regardless).
type Outcome struct {
	JobID           string          `json:"job_id"`
	RenderedCommand string          `json:"rendered_command,omitempty"`
	ResultJSON      string          `json:"result_json,omitempty"`
	DiffSummary     string          `json:"diff_summary,omitempty"`
	Artifacts       json.RawMessage `json:"artifacts,omitempty"`
	// SessionID 是 worker 侧本地 JobResult 捕获/注入得到的 agent 会话标识(P3)，随
	// Outcome 帧回传 host，host 端 applyOutcome 落到 entry.result.SessionID。空=未捕获。
	SessionID string `json:"session_id,omitempty"`
}

// --- P2/P3 placeholders: declared so the protocol is complete (review #6); the
// hub/worker do not act on these in WP1. ---

// Cancel (s→w, P2): cancel a running job on the worker.
type Cancel struct {
	JobID string `json:"job_id"`
}

// Interaction (w→s, P2): a worker-raised running-job interaction bridged onto the
// host job. The interaction body stays raw JSON so wsproto need not import job;
// P2 decodes it into job.Interaction on the hub side.
type Interaction struct {
	JobID       string          `json:"job_id"`
	Action      string          `json:"action"` // open|answered|cancelled
	Interaction json.RawMessage `json:"interaction"`
}

// Answer (s→w, P2): the host-side answer to a worker interaction.
type Answer struct {
	JobID         string `json:"job_id"`
	InteractionID string `json:"interaction_id"`
	Answer        string `json:"answer"`
}

// Ping/Pong (both, P3): heartbeat / half-open detection.
type Ping struct {
	TS int64 `json:"ts"`
}
type Pong struct {
	TS int64 `json:"ts"`
}

// --- Config hot-reload frames (protocol v3; gate with SupportsReload). ---

// Reload (s→w): ask the worker to re-read its local config and re-report what it
// can do. It is an RPC REQUEST, not a fire-and-forget signal: RequestID is the
// only thing that ties the worker's ReloadResult back to this call, so the caller
// (which is blocking a synchronous HTTP request on the answer) can tell ITS reply
// apart from any other reload happening on the same connection. Reason is free
// text for logs/audit only.
type Reload struct {
	RequestID string `json:"request_id"`
	Reason    string `json:"reason,omitempty"`
}

// ReloadResult (w→s): the reply to exactly one Reload, echoing its RequestID.
//
// OK=false means the worker REFUSED the new config (bad YAML, invalid agent, …)
// and is still running the OLD one unchanged — the reload failed, but the worker
// did not degrade; Err carries the reason so the caller can surface it instead of
// answering "accepted" and losing the error. OK=true carries the resulting Caps,
// so a successful reload updates the hub's view of the worker in the same frame
// (no separate broadcast needed, no window where the hub routes on stale caps).
//
// ok has NO omitempty on purpose: false is the meaningful value here (a dropped
// "ok":false would decode as the zero value anyway, but the explicit key keeps the
// wire self-describing for logs and for any non-Go peer).
type ReloadResult struct {
	RequestID string `json:"request_id"`
	OK        bool   `json:"ok"`
	Err       string `json:"err,omitempty"`
	Caps      *Caps  `json:"caps,omitempty"`
}

// Caps (w→s): an UNSOLICITED re-report of the worker's capabilities, sent when
// they changed with no Reload request to answer — a SIGHUP-triggered reload is the
// case that forces this frame to exist (it originates on the worker, so there is
// no RequestID and nowhere to send a receipt).
//
// It is a SEPARATE type from ReloadResult, never a substitute for it. Collapsing
// the two would let an unrelated broadcast (SIGHUP, a concurrent reload, a
// re-report after reconnect) be mistaken for the answer to a pending Reload — the
// caller would resolve its RPC against caps it never asked for, and the real reply
// would then look unsolicited. A Caps frame therefore MUST NOT complete a pending
// reload request; it only refreshes the hub's capability view.
//
// Register cannot be reused for this: it is accepted only as the FIRST frame of a
// connection (the hub has no run-time branch for it), so re-reporting capabilities
// on a live connection is what this frame is for. It is a re-report, not a
// re-register: identity (worker_id/instance_id) and the immutable process facts
// (os/arch/gofer_version/started_at/protocol_version/pty_capable) are NOT resent —
// they cannot change without a restart, which brings a fresh Register anyway.
//
// The payload is a FULL SNAPSHOT of every capability field that a config reload can
// change, i.e. exactly the config-derived subset of Register. No omitempty: a
// reload that empties a capability (all projects removed, say) must travel as an
// explicit empty list, not as an absent field indistinguishable from "unchanged".
type Caps struct {
	Labels    []string     `json:"labels"`
	Projects  []string     `json:"projects"`
	Agents    []string     `json:"agents"`
	AgentCaps []AgentBrief `json:"agent_caps"`
	MaxConc   int          `json:"max_concurrent"`
}

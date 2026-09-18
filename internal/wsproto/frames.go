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
	//
	// v4 adds the server-authoritative policy push frames (policy/applied). Bumping
	// Current (not Min) is the whole point of the two-constant split: a v3 worker
	// stays registered, it just cannot be sent a policy frame (negotiated per peer via
	// SupportsPolicy), so no already-deployed worker is evicted by shipping this frame.
	// v5 adds the TCP tunnel frames; v6 adds the resume/read-only dispatch fields
	// (session_id/resumed_from/read_only — see SessionLoadMinProtocolVersion); v7 adds
	// the priming dispatch fields (initial_input/initial_input_quiet_ms — see
	// InitialInputMinProtocolVersion); v8 adds the verify dispatch fields
	// (verify/verify_timeout_sec — see VerifyMinProtocolVersion) and the job_event
	// frame.
	CurrentProtocolVersion = 8
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

// PolicyMinProtocolVersion is the first protocol version that carries the
// server-authoritative policy push frames (policy/applied). Same negotiation rule as
// ReloadMinProtocolVersion: a worker registered below it stays fully usable — it just
// never receives a policy frame, so a v3 worker keeps sourcing its projects from its
// own local config (LEGACY) and is never evicted for lacking the capability.
const PolicyMinProtocolVersion = 4

// TunnelMinProtocolVersion is the first protocol version carrying TCP tunnel frames.
const TunnelMinProtocolVersion = 5

// SupportsTunnel reports whether a peer supports TCP tunnel frames.
func SupportsTunnel(proto int) bool { return proto >= TunnelMinProtocolVersion }

// SessionLoadMinProtocolVersion is the first protocol version whose Dispatch carries
// session_id/resumed_from (an acp-agent continuation) and read_only. Since P2 the hub
// does NOT tolerate a peer below it for a job that needs those fields: the additive
// fields would be silently ignored, which costs exactly the S2 semantics (a resume
// opens a NEW session instead of loading the source one; a read-only job runs
// writable) — a job that quietly does the wrong thing. The dispatch is REFUSED
// instead (see runner/worker's capability gate), with the missing capability named so
// the operator upgrades that worker. G032: this replaced the earlier warn-only
// tolerance, which no longer exists.
const SessionLoadMinProtocolVersion = 6

// SupportsSessionLoad reports whether a peer that registered with protocol version
// proto understands the resume dispatch fields (session_id/resumed_from).
func SupportsSessionLoad(proto int) bool { return proto >= SessionLoadMinProtocolVersion }

// InitialInputMinProtocolVersion is the first protocol version whose Dispatch carries
// initial_input/initial_input_quiet_ms — path B's priming text and quiet window
// (session relay §9.1 B). Same refusal rule as SessionLoadMinProtocolVersion: a peer
// below it would silently ignore the fields, so a dispatch that needs priming is
// refused with the missing capability named instead of handing a human a takeover
// session whose first message never arrives.
const InitialInputMinProtocolVersion = 7

// SupportsInitialInput reports whether a peer that registered with protocol version
// proto understands the priming dispatch fields.
func SupportsInitialInput(proto int) bool { return proto >= InitialInputMinProtocolVersion }

// VerifyMinProtocolVersion is the first protocol version whose Dispatch carries
// verify/verify_timeout_sec (SUP-01 B) and which can send the job_event frame
// (SUP-01 G). It is the version where a worker can be TRUSTED to run a job's verify
// step: a peer below it would ignore the argv and report a job as done with no
// verification performed, so the hub refuses such a dispatch up front (G032: no
// silent degradation) rather than shipping a step that will not run.
const VerifyMinProtocolVersion = 8

// SupportsVerify reports whether a peer that registered with protocol version proto
// understands the verify dispatch fields and sends job_event frames.
func SupportsVerify(proto int) bool { return proto >= VerifyMinProtocolVersion }

// TunnelOpen requests a worker to open a TCP tunnel (protocol v5).
type TunnelOpen struct {
	TunnelID   string `json:"tunnel_id"`
	Target     string `json:"target"`
	RelayNonce string `json:"relay_nonce"`
	Network    string `json:"network,omitempty"`
}

// SupportsPolicy reports whether a peer that registered with protocol version proto
// implements the policy push frames. Like SupportsReload it is the single place that
// knows which version gained the capability — callers must not compare versions.
func SupportsPolicy(proto int) bool { return proto >= PolicyMinProtocolVersion }

// AgentBrief is a worker-reported agent capability with the detail the UI cascade
// needs (type/interactive) beyond a bare key. Federation: the worker is the
// authority for ITS agents' type/interactive (the server may not have them in its
// own config).
//
// Adding Available/Version does NOT bump the protocol: As[T] (envelope.go) is a
// plain json.Unmarshal with no DisallowUnknownFields, so an old peer silently
// ignores the new keys and a new peer decodes their absence as the zero value.
type AgentBrief struct {
	Key         string `json:"key"`
	Type        string `json:"type,omitempty"`
	Interactive bool   `json:"interactive,omitempty"`
	Batch       bool   `json:"batch"`
	// Available is DISPLAY-ONLY. Never gate admission on it: a worker that predates
	// this field reports nothing (nil), and an operator-declared agent whose probe
	// failed reports false — BOTH OF THEM RUN FINE.
	//
	// That is also why it is a *bool and not a bool: a plain bool would decode the
	// absent field of a pre-P2 worker as false, making "never reported" (unknown)
	// indistinguishable from "reported unusable". Any consumer that filters or greys
	// out on it therefore hits both of those false negatives at once — every agent of
	// every old worker, plus every escape-hatch agent whose CLI the probe could not
	// see. nil = unknown; the agent list itself is the authority on what can run.
	Available *bool `json:"available,omitempty"`
	// Version is the best-effort CLI version string (empty = not probed, probe failed
	// or unavailable). Display-only, same rule as Available.
	Version string `json:"version,omitempty"`
}

// UnmarshalJSON derives batch capability for legacy workers that omitted it.
func (a *AgentBrief) UnmarshalJSON(data []byte) error {
	type alias AgentBrief
	var raw struct {
		*alias
		Batch *bool `json:"batch"`
	}
	raw.alias = (*alias)(a)
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.Batch == nil {
		a.Batch = !a.Interactive
	} else {
		a.Batch = *raw.Batch
	}
	return nil
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
	Hostname        string   `json:"hostname,omitempty"`      // os.Hostname() — identifies the machine (NAT-safe, unlike the conn's remote addr)
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
	// Inflight (w→s, RECOV-01) is the worker's view of the jobs it currently
	// tracks for this hub: the remote job_id, its local status and the byte
	// offsets/seq it has ALREADY pushed on the wire. The hub uses it on a
	// same-instance reconnect to decide, per recovering job, whether the worker
	// still has the job (→ resume + replay from the server's offsets), has it but
	// already finished (→ wait for the replayed Result) or no longer has it
	// (→ fail at once, "worker no longer tracks job").
	//
	// NO omitempty on purpose: a nil slice means "a pre-RECOV-01 worker that cannot
	// prove anything" (the hub then falls back to the recovery-window timer), while
	// an EMPTY slice means "a new worker that tracks nothing" — and those two must
	// not be conflated, or a worker that dropped all its jobs would silently keep
	// the server waiting instead of failing them. Same absent≠empty reasoning as
	// AgentBrief.Available. The addition does NOT bump protocol_version: an old
	// server ignores the unknown key (As is a plain json.Unmarshal).
	Inflight []InflightJob `json:"inflight"`
}

// InflightJob is one entry of Register.Inflight: a job the worker still tracks on
// behalf of the hub (RECOV-01). Status is the WORKER-side local job status; the
// hub only needs to know whether it is terminal (job.IsTerminal lives in job, so
// the hub compares against the wire vocabulary below).
type InflightJob struct {
	JobID string `json:"job_id"`
	// Status is the worker's local job status ("queued"/"running"/"pending_interaction"/
	// "done"/"failed"/"cancelled"/"timeout"/"recovering").
	Status string `json:"status,omitempty"`
	// StdoutOff/StderrOff are the byte offsets the worker has successfully SENT for
	// this job's stdout/stderr log files (a frame that failed to write does NOT
	// advance them), so the hub can rewind the worker to what it actually persisted.
	StdoutOff int64 `json:"stdout_off,omitempty"`
	StderrOff int64 `json:"stderr_off,omitempty"`
	// Seq is the highest log-frame seq the worker has sent for this job.
	Seq int64 `json:"seq,omitempty"`
}

// ResumeJob is one entry of Registered.Resume: a job the hub is holding in
// `recovering` and the worker must resume (RECOV-01). The offsets are the
// SERVER-side durably written byte counts — the authoritative source — so the
// worker rewinds its local read offsets to them and re-sends whatever was lost
// while the connection was down (no gaps, no duplicates beyond the rewind point).
type ResumeJob struct {
	JobID     string `json:"job_id"`
	StdoutOff int64  `json:"stdout_off,omitempty"`
	StderrOff int64  `json:"stderr_off,omitempty"`
}

// Registered (s→w, P1): handshake ack. ServerTime is in milliseconds (SR102, in
// line with the /v1 envelope convention).
type Registered struct {
	Accepted   bool   `json:"accepted"`
	Reason     string `json:"reason,omitempty"`
	ServerTime int64  `json:"server_time"`
	// ProtocolVersion is the protocol version the SERVER implements (Q7-b). A worker
	// reads it to negotiate optional server-side features. It is additive: an OLD
	// server never sets it, so As[Registered] decodes the absent key to 0 — a worker
	// must treat 0 as "server predates this field", never as a real version.
	ProtocolVersion int `json:"protocol_version,omitempty"`
	// Policy, when non-nil, is the authoritative policy the server pushes together
	// with the ack so a freshly registered worker converges without a second frame
	// (catch-up on register). Nil on old servers / when policy push is off. The frame
	// carrying + apply behaviour is implemented later (T4); T0 only declares the field
	// so the wire is stable.
	Policy *Policy `json:"policy,omitempty"`
	// Resume (s→w, RECOV-01) lists the jobs this hub is holding in `recovering` for
	// the registering worker process and has decided to resume from. It is carried on
	// the ack so the worker can rewind its log offsets BEFORE it resumes streaming —
	// a second frame would race the worker's own replay. Empty/nil on an old server
	// or when nothing is recovering (the fields are optional, no version bump).
	Resume []ResumeJob `json:"resume,omitempty"`
}

// Dispatch (s→w, P1): a job assignment = JobRequest projection. Runner is always
// "local" (the worker executes locally with its own config). worker_id is NOT
// carried — the worker already knows it is itself.
type Dispatch struct {
	JobID        string   `json:"job_id"`
	ProjectKey   string   `json:"project_key"`
	Agent        string   `json:"agent"`
	Runner       string   `json:"runner"`
	Prompt       string   `json:"prompt,omitempty"`
	AgentArgs    []string `json:"agent_args,omitempty"`
	SystemPrompt string   `json:"system_prompt,omitempty"`
	Cmd          []string `json:"cmd,omitempty"`
	Cwd          string   `json:"cwd,omitempty"`
	// Worktree/WorktreeBase (WT-01) ask the WORKER to run this job in a managed git
	// worktree of its own project checkout. The submitting service already resolved the
	// project-level worktree_default into Worktree, so the worker never re-derives it.
	// An OLD worker ignores the unknown fields and runs in the shared checkout.
	Worktree          bool   `json:"worktree,omitempty"`
	WorktreeBase      string `json:"worktree_base,omitempty"`
	TimeoutSec        int    `json:"timeout_sec,omitempty"`
	Interactive       bool   `json:"interactive,omitempty"`
	Cols              int    `json:"cols,omitempty"`
	Rows              int    `json:"rows,omitempty"`
	ResumeSourceAgent string `json:"resume_source_agent,omitempty"`
	// SessionID/ResumedFrom (ACP-01 S2) ask the worker to CONTINUE an existing agent
	// session instead of opening a fresh one: for an acp-agent the local job resolves
	// SessionID into the runner's session/load, and ResumedFrom is the lineage marker
	// that makes the session a LOAD rather than a plain binding. Both are set only for
	// a resume (the hub projects them from a Forward whose ResumedFrom is non-empty) —
	// a plain job's session_id never travels here. An OLD worker ignores the unknown
	// fields and starts a new session (see SessionLoadMinProtocolVersion).
	SessionID   string `json:"session_id,omitempty"`
	ResumedFrom string `json:"resumed_from,omitempty"`
	// ReadOnly (bd h-aii-0ql3) asks the worker to run the job in its own read-only mode
	// (cli-agent argv sandbox / acp-agent session/set_mode). The worker validates it
	// against its OWN agent config. An OLD worker ignores the field and runs the job
	// writable (see SessionLoadMinProtocolVersion).
	ReadOnly bool `json:"read_only,omitempty"`
	// InitialInput / InitialInputQuietMs carry path B's priming text and quiet
	// window (session relay §9.1 B): the worker starts the pty, so the text the hub
	// wants typed and the window it resolved must arrive with the dispatch. A hub
	// that predates them omits the keys and the worker behaves as before (nothing
	// is primed).
	InitialInput        string `json:"initial_input,omitempty"`
	InitialInputQuietMs int    `json:"initial_input_quiet_ms,omitempty"`
	RelayNonce          string `json:"relay_nonce,omitempty"`
	// PtySessionID is the host-minted relay session id the worker echoes back in
	// its pty-connect hello so the serve endpoint can strong-check it against the
	// binding (httpapi/pty_connect_handler; D-P2-4). Empty on non-interactive.
	PtySessionID string `json:"pty_session_id,omitempty"`
	// TodoID (SUP-01 C) is the hub's checklist item for this job, carried so the
	// worker's own job row can DISPLAY it. The todo lives in the hub's store, so the
	// worker neither resolves nor links it (JobRequest.TodoForeign). Empty on a plain
	// dispatch and on a hub that predates the field.
	TodoID string `json:"todo_id,omitempty"`
	// Verify / VerifyTimeoutSec are the job's验证步骤 (SUP-01 B): the argv run after
	// the agent finishes normally, and its own deadline in seconds. A hub that
	// predates them omits the keys; a worker that predates them never receives them,
	// because the hub refuses the dispatch instead (VerifyMinProtocolVersion).
	Verify           []string `json:"verify,omitempty"`
	VerifyTimeoutSec int      `json:"verify_timeout_sec,omitempty"`
}

// JobEvent (w→s, SUP-01 G, protocol v8): one job life-cycle event the WORKER raised
// for a job the hub dispatched to it. It is a MIRROR, not a new source of truth: the
// event is already recorded in the worker's own store (that is where it happened);
// the hub records the same type/detail against the HOST job so its event log,
// notifications and audit trail answer "what did this job do on the worker".
//
// Detail stays raw JSON: wsproto is a leaf (the job package owns the event
// vocabulary) and the hub needs the payload only to forward it.
type JobEvent struct {
	JobID string `json:"job_id"`
	// Type is the job-package event type ("job.permission_requested",
	// "job.verify_finished", …); the hub keeps its own whitelist of what it accepts.
	Type string `json:"type"`
	// Detail is the event's detail map as the worker recorded it (nil = none).
	Detail json.RawMessage `json:"detail,omitempty"`
	// TS is when the worker raised it (unix seconds) — part of the event's identity
	// for de-duplication, since a replayed frame carries the same TS and a genuinely
	// new event of the same type carries a later one.
	TS int64 `json:"ts"`
	// InteractionID is the interaction the event is about, when it has one (the
	// approval-gate events do; the verify events do not). It is lifted out of the
	// detail so de-duplication does not have to parse it.
	InteractionID string `json:"interaction_id,omitempty"`
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
	// WT-01：worker 侧受管 worktree 的位置与分支状态（该路径在那台机器上）。旧 worker
	// 不发这组字段 → host 行保持无 worktree（不伪造）。
	WorktreePath    string `json:"worktree_path,omitempty"`
	WorktreeBranch  string `json:"worktree_branch,omitempty"`
	WorktreeBaseSHA string `json:"worktree_base_sha,omitempty"`
	WorktreeHeadSHA string `json:"worktree_head_sha,omitempty"`
	CommitsAhead    int    `json:"commits_ahead,omitempty"`
	// BaseSHA / Commits are the SUP-01 C提交采集 captured on the worker (the host has
	// no checkout there): the commit the job started from and what it produced,
	// newest first. An old worker never sends them → the host row stays empty (the
	// capture is not faked).
	BaseSHA string   `json:"base_sha,omitempty"`
	Commits []Commit `json:"commits,omitempty"`
	// Verify (SUP-01 B) is the verification step's result: the worker ran it in ITS
	// checkout, so its structured outcome travels here. Nil = the job had no step
	// (the hub refuses a verify dispatch to a worker that cannot run it, so a nil
	// here never means "the step was dropped").
	Verify *VerifyResult `json:"verify,omitempty"`
}

// VerifyResult is the wire form of one job's verify step outcome (SUP-01 B). It is
// declared here (like Commit) so wsproto stays a leaf that does not import job.
type VerifyResult struct {
	// Command is the argv as submitted (element-wise).
	Command []string `json:"command,omitempty"`
	// Status is passed|failed|timeout|skipped.
	Status string `json:"status"`
	// ExitCode is the step's exit status (-1 when it did not end on its own).
	ExitCode int `json:"exit_code"`
	// DurationMs is how long the step ran, in milliseconds.
	DurationMs int64 `json:"duration_ms"`
	// Reason explains a non-obvious status (why it was skipped / timed out).
	Reason string `json:"reason,omitempty"`
}

// Commit is one commit captured for a job (SUP-01 C): the abbreviated sha and the
// subject line. Declared here (like Outcome.Artifacts) so wsproto stays a leaf
// that does not import job.
type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
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

// --- Policy push frames (protocol v4; gate with SupportsPolicy). ---
//
// The server is the authority for which projects a worker may run and under what
// guards; it computes a Policy and pushes it (TypePolicy, or bundled on the
// Registered ack) so an operator can add/change a project server-side with no worker
// edit. The worker projects the Policy onto its local config and reports back what it
// actually applied (TypeApplied). D1 boundary: a Policy conveys projects + guards
// ONLY — never agent definitions and never a "custom agents" escape hatch.

// PolicyProject is one project entry in a pushed Policy: the server-side identity +
// guards for a project a worker may run.
type PolicyProject struct {
	Key      string `json:"key"`
	HostPath string `json:"host_path"` // 逻辑路径; the worker maps it onto a local root
	// AllowedAgents: computePolicy guarantees a NON-nil value (T3). The wire form of an
	// empty list may still be null (a Go nil slice marshals to null even without
	// omitempty), so a DOWNSTREAM consumer must treat null and [] as equivalent — judge
	// by len, never by nil-ness (MEDIUM-1).
	AllowedAgents []string `json:"allowed_agents"`
	// InteractiveAllowedAgents is DEPRECATED (AGT-02 0.3 removed the narrowing list) and
	// is kept ONLY to READ what a pre-AGT-02 server still sends: this server never sets
	// it, so it now marshals to null/[] and carries no authority. A worker reads a
	// non-empty value as the old rule "the project allows interactive jobs" when
	// AllowInteractive is absent (see commands.policyAllowsInteractive); it must never
	// narrow with it again.
	InteractiveAllowedAgents []string `json:"interactive_allowed_agents"`
	// AllowInteractive is the project's interactive-job switch (AGT-02 §2). It is a
	// pointer for the same "unset ≠ explicit false" reason as CaptureDiff below: a
	// pre-AGT-02 server does not send it at all, and the worker must then fall back
	// to the legacy reading of InteractiveAllowedAgents (non-empty = allowed) instead
	// of treating the absent field as a denial. A server that does send it sends the
	// RESOLVED value (ProjectConfig.IsInteractiveAllowed), so a plain false here is
	// an explicit "no interactive jobs" even with a leftover allowlist.
	AllowInteractive *bool `json:"allow_interactive,omitempty"`
	AllowExec        bool  `json:"allow_exec"`
	// MaxConcurrentJobs uses omitempty (H2): "not sent" == 0 == unlimited concurrency.
	MaxConcurrentJobs int `json:"max_concurrent_jobs,omitempty"`
	// CaptureDiff is *bool (H2): "not sent" (nil) == default-on; only a present false
	// is an explicit opt-out. Same "unset ≠ explicit false" reason as AgentBrief.Available.
	CaptureDiff *bool `json:"capture_diff,omitempty"`
	// MaxTimeoutSec is the RESOLVED job-timeout ceiling for this project
	// (config.Config.EffectiveMaxTimeoutSec: the project's max_timeout_sec, else the
	// server's max_job_timeout_sec, else 1h — bd h-aii-s9ck). The server already
	// clamped the dispatched timeout_sec, so the worker applies THIS value as its own
	// clamp ceiling and the second clamp becomes a no-op instead of silently
	// re-clamping an admitted timeout against a worker-local default. omitempty: a
	// pre-change server sends nothing (0) and the worker falls back to its own config.
	MaxTimeoutSec int `json:"max_timeout_sec,omitempty"`
	// Approval is the project's RESOLVED approval gate (GATE-01 §1) — the worker
	// needs it because the acp-agent job runs THERE and the gate is decided at
	// permission-request time. omitempty: a pre-GATE server sends nothing (nil) and
	// the worker falls back to its own default (off).
	Approval *ApprovalPolicy `json:"approval,omitempty"`
}

// ApprovalPolicy is the wire form of a project's approval gate
// (config.ApprovalConfig). Every field is RESOLVED (the server applied the defaults)
// so the worker never re-derives them, and the type lives here because wsproto must
// not import internal/config.
type ApprovalPolicy struct {
	Mode                string   `json:"mode"`
	AutoAllowKinds      []string `json:"auto_allow_kinds"`
	AskKinds            []string `json:"ask_kinds"`
	TimeoutSec          int      `json:"timeout_sec"`
	OnTimeout           string   `json:"on_timeout"`
	RememberAllowAlways bool     `json:"remember_allow_always"`
}

// Policy (s→w): the full set of projects a worker may run at revision Rev. Rev is the
// config generation (monotonic); the worker applies latest-wins and reports the Rev
// it converged to in Applied.
type Policy struct {
	Rev      int64           `json:"rev"`
	Projects []PolicyProject `json:"projects"`
}

// AppliedRejection is one project the worker could NOT apply (e.g. host_path outside
// every local root). It is diagnostic only — surfaced on the Cluster page — and does
// NOT participate in routing.
type AppliedRejection struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// AppliedDegrade is one project the worker applied but with a capability gated off
// (e.g. a legacy local-projects worker that ignores the pushed policy). Diagnostic
// only, same as AppliedRejection.
type AppliedDegrade struct {
	Key  string `json:"key"`
	Gate string `json:"gate"`
}

// Applied (w→s): the worker's report of what it actually applied for a Policy Rev.
// Caps is EMBEDDED (not a new capability channel): the hub routes it through the same
// reg.UpdateCaps path that reload/caps use. Rejected/Degraded are diagnostic only and
// never gate routing.
type Applied struct {
	Rev      int64              `json:"rev"`
	Caps     *Caps              `json:"caps,omitempty"`
	Rejected []AppliedRejection `json:"rejected,omitempty"`
	Degraded []AppliedDegrade   `json:"degraded,omitempty"`
}

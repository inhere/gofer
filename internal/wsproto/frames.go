package wsproto

import "encoding/json"

// Register (w→s, P1): the worker announces its identity + capability snapshot on
// connect. The hub validates worker_id against the token binding (review #1);
// labels/projects/agents are display/optional-prehint only — the worker
// re-validates locally on dispatch (review #8).
type Register struct {
	WorkerID string `json:"worker_id"`
	// InstanceID is a per-PROCESS nonce minted once at worker start and reused
	// across reconnects. It lets the hub tell a transient network reconnect (same
	// instance → in-flight jobs survive, supersede exemption applies) from a worker
	// RESTART (new instance under the same worker_id → the old process's in-flight
	// jobs died with it and must be failed, not exempted). Empty on old workers →
	// the hub falls back to the legacy supersede-always behaviour (z8ow).
	InstanceID    string   `json:"instance_id,omitempty"`
	PtyCapable    bool     `json:"pty_capable,omitempty"`
	OS            string   `json:"os,omitempty"`
	Labels        []string `json:"labels,omitempty"`
	Projects      []string `json:"projects,omitempty"`
	Agents        []string `json:"agents,omitempty"`
	MaxConcurrent int      `json:"max_concurrent,omitempty"`
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
	JobID       string   `json:"job_id"`
	ProjectKey  string   `json:"project_key"`
	Agent       string   `json:"agent"`
	Runner      string   `json:"runner"`
	Prompt      string   `json:"prompt,omitempty"`
	Cmd         []string `json:"cmd,omitempty"`
	Cwd         string   `json:"cwd,omitempty"`
	TimeoutSec  int      `json:"timeout_sec,omitempty"`
	Interactive bool     `json:"interactive,omitempty"`
	Cols        int      `json:"cols,omitempty"`
	Rows        int      `json:"rows,omitempty"`
	RelayNonce  string   `json:"relay_nonce,omitempty"`
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

// Package job is the async job state machine: it accepts a JobRequest, resolves
// the agent+cwd, runs it on a runner in a background goroutine, streams logs to
// the store and tracks status/timeout/cancel. See plan §6.2 and §9 (P4).
package job

// JobRequest is the create-job payload. JSON tags are snake_case (plan §6.2).
type JobRequest struct {
	ProjectKey string   `json:"project_key"`
	Agent      string   `json:"agent"`
	Runner     string   `json:"runner"`
	Prompt     string   `json:"prompt,omitempty"`
	Cmd        []string `json:"cmd,omitempty"`
	Cwd        string   `json:"cwd,omitempty"`
	TimeoutSec int      `json:"timeout_sec,omitempty"`
	Title      string   `json:"title,omitempty"`
	// WorkerID selects which registered worker a runner=worker job dispatches to
	// (ws-worker §8). Required and must be a known server.workers entry when the
	// runner is type=worker; ignored for local/peer-http runners.
	WorkerID string `json:"worker_id,omitempty"`
	// CallerID is the authenticated submitter id (C2). It is set server-side by
	// the HTTP layer from the auth context (any client-supplied value is
	// overwritten); it is not part of the client-facing contract.
	CallerID string `json:"caller_id,omitempty"`
	// RequestID is the optional client-supplied idempotency key (C5, e.g. a
	// UUID). When set, re-submitting the same RequestID returns the existing job
	// instead of creating a new one (deduped by the jobs.request_id unique index).
	RequestID string `json:"request_id,omitempty"`
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
	Title  string `json:"title,omitempty"`
	Status string `json:"status"`
	ExitCode   int    `json:"exit_code"`
	Cwd        string `json:"cwd"`
	ResultDir  string `json:"result_dir"`
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

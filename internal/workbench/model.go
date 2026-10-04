// Package workbench projects persisted jobs and relay sessions into the
// conversation-first WEB-11 model. It owns projection and dispatch only; job and
// sessionrelay remain authoritative for execution and replies.
package workbench

import (
	"encoding/json"
	"errors"

	"github.com/inhere/gofer/internal/job"
)

type ThreadKind string

const (
	KindAgent ThreadKind = "agent"
	KindJob   ThreadKind = "job"
	KindRelay ThreadKind = "relay"
)

type Status string

const (
	StatusBlocked       Status = "blocked"
	StatusWorking       Status = "working"
	StatusReview        Status = "review"
	StatusDone          Status = "done"
	StatusIdle          Status = "idle"
	StatusAwaitingInput Status = "awaiting_input"
)

type AttentionAction string

const (
	ActionAnswer AttentionAction = "answer"
	ActionReview AttentionAction = "review"
	ActionReply  AttentionAction = "reply"
)

var (
	ErrInvalidThreadID = errors.New("workbench: invalid thread id")
	ErrUnknownThread   = errors.New("workbench: unknown thread")
	ErrNotResumable    = errors.New("workbench: thread is not resumable")
	ErrEmptyTurn       = errors.New("workbench: turn text is required")
	ErrUnavailable     = errors.New("workbench: service unavailable")
	ErrInvalidLayout   = errors.New("workbench: invalid layout")
	ErrLayoutTooLarge  = errors.New("workbench: layout body exceeds 64 KiB")
	ErrMissingDiffBase = errors.New("workbench: first turn has no git diff base")
	ErrDiffUnavailable = errors.New("workbench: thread diff unavailable")
	ErrDiffTimeout     = errors.New("workbench: thread diff timed out")
	ErrInvalidReview   = errors.New("workbench: invalid review")
)

const MaxLayoutBodyBytes = 64 * 1024

type Layout struct {
	Version int64           `json:"version"`
	Body    json.RawMessage `json:"body"`
}

// LayoutVersionConflict carries the server value a client must adopt after a
// stale optimistic write.
type LayoutVersionConflict struct {
	Current Layout
}

func (e *LayoutVersionConflict) Error() string {
	return "workbench: layout version conflict"
}

type Query struct {
	Project string
	Status  Status
	Q       string
	Since   int64
	// IncludeInternal also lists web session-messenger delivery jobs (all=1).
	IncludeInternal bool
}

type PatchInput struct {
	Title  *string
	Seen   *bool
	Pinned *bool
}

type Usage struct {
	InputTokens      int64   `json:"input_tokens,omitempty"`
	OutputTokens     int64   `json:"output_tokens,omitempty"`
	CacheReadTokens  int64   `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64   `json:"cache_write_tokens,omitempty"`
	TotalTokens      int64   `json:"total_tokens,omitempty"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
}

type JobTurn struct {
	JobID     string     `json:"job_id"`
	Status    string     `json:"status"`
	StartedAt int64      `json:"started_at"`
	EndedAt   int64      `json:"ended_at,omitempty"`
	Usage     *job.Usage `json:"usage,omitempty"`
}

type RelaySummary struct {
	SessionID   string `json:"session_id"`
	State       string `json:"state"`
	RelayMode   string `json:"relay_mode"`
	LastEvent   string `json:"last_event,omitempty"`
	LastMessage string `json:"last_message,omitempty"`
}

type Thread struct {
	ID                  string            `json:"id"`
	Kind                ThreadKind        `json:"kind"`
	Status              Status            `json:"status"`
	RawStatus           string            `json:"raw_status,omitempty"`
	Stalled             bool              `json:"stalled,omitempty"`
	Title               string            `json:"title"`
	ProjectKey          string            `json:"project_key"`
	Agent               string            `json:"agent,omitempty"`
	Runner              string            `json:"runner,omitempty"`
	Cwd                 string            `json:"cwd,omitempty"`
	Interactive         bool              `json:"interactive,omitempty"`
	Resumable           bool              `json:"resumable"`
	Pinned              bool              `json:"pinned,omitempty"`
	SeenAt              int64             `json:"seen_at,omitempty"`
	StartedAt           int64             `json:"started_at"`
	UpdatedAt           int64             `json:"updated_at"`
	WaitingSince        int64             `json:"waiting_since,omitempty"`
	Turns               int               `json:"turns"`
	Usage               *Usage            `json:"usage,omitempty"`
	LatestJobID         string            `json:"latest_job_id,omitempty"`
	JobIDs              []string          `json:"job_ids,omitempty"`
	Jobs                []JobTurn         `json:"jobs,omitempty"`
	PendingInteractions []job.Interaction `json:"pending_interactions,omitempty"`
	Relay               *RelaySummary     `json:"relay,omitempty"`
}

type Counts struct {
	Total         int `json:"total"`
	Blocked       int `json:"blocked"`
	Working       int `json:"working"`
	Review        int `json:"review"`
	Done          int `json:"done"`
	Idle          int `json:"idle"`
	OrphanBlocked int `json:"orphan_blocked"`
	OrphanReview  int `json:"orphan_review"`
}

type ProjectGroup struct {
	ProjectKey string   `json:"project_key"`
	Status     Status   `json:"status"`
	Counts     Counts   `json:"counts"`
	Threads    []Thread `json:"threads"`
}

type AttentionItem struct {
	ThreadID      string          `json:"thread_id"`
	ProjectKey    string          `json:"project_key"`
	Title         string          `json:"title"`
	Status        Status          `json:"status"`
	Action        AttentionAction `json:"action"`
	WaitingSince  int64           `json:"waiting_since"`
	JobID         string          `json:"job_id,omitempty"`
	InteractionID string          `json:"interaction_id,omitempty"`
	SessionID     string          `json:"session_id,omitempty"`
	DecisionID    string          `json:"decision_id,omitempty"`
}

type Response struct {
	Projects  []ProjectGroup  `json:"projects"`
	Attention []AttentionItem `json:"attention"`
	Total     int             `json:"total"`
	Since     int64           `json:"since"`
}

type TurnResult struct {
	ThreadID   string `json:"thread_id"`
	JobID      string `json:"job_id,omitempty"`
	DecisionID string `json:"decision_id,omitempty"`
}

type DiffSource string

const (
	DiffSourceLive     DiffSource = "live"
	DiffSourceCaptured DiffSource = "captured"
)

// ThreadDiffFile is one path changed since the thread's first Git baseline.
// Untracked files use status "?" and deliberately carry no content or line counts.
type ThreadDiffFile struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary"`
}

// ThreadDiff is the conversation-level review snapshot. Captured responses are
// explicitly scoped to the latest turn; live responses span base..working-tree.
type ThreadDiff struct {
	Source    DiffSource       `json:"source"`
	Base      string           `json:"base"`
	Head      string           `json:"head,omitempty"`
	Files     []ThreadDiffFile `json:"files"`
	Patch     string           `json:"patch"`
	Truncated bool             `json:"truncated"`
	Commits   []job.Commit     `json:"commits,omitempty"`
	Notice    string           `json:"notice,omitempty"`
}

type ReviewSide string

const (
	ReviewSideNew ReviewSide = "new"
	ReviewSideOld ReviewSide = "old"
)

const (
	MaxReviewComments     = 50
	MaxReviewCommentRunes = 4000
)

type ReviewComment struct {
	Path string     `json:"path"`
	Line int        `json:"line"`
	Side ReviewSide `json:"side"`
	Text string     `json:"text"`
}

type ReviewInput struct {
	Summary  string          `json:"summary,omitempty"`
	Comments []ReviewComment `json:"comments,omitempty"`
}

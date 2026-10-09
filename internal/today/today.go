// Package today is the N3 「今天」 decision home (design
// docs/design/2026-10-09-n3-today-decision-home-design.md §1/§2): it folds every
// "a person must decide" signal — pending interactions, OPEN decisions, relay turns,
// needs_review jobs, work items that wait for the person, the status / goal tidy-up
// suggestions, merge suggestions and blocked plans — into one ordered card queue, plus
// the one-line digest and the status bar. It only READS existing sources: every card
// action maps to an existing write API, which the console calls directly.
//
// The httpapi layer binds / validates and forwards here (G021).
package today

import (
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/steward"
	"github.com/inhere/gofer/internal/work"
)

// Card kinds (DecisionCard.kind).
const (
	KindInteraction = "interaction"
	KindDecision    = "decision"
	KindRelay       = "relay"
	KindReview      = "review"
	KindWork        = "work"
	KindSuggestion  = "suggestion"
	KindMerge       = "merge"
	KindPlanBlocked = "plan_blocked"
)

// Urgency values. now = will time out soon, blocking = something waits on it.
const (
	UrgencyNow      = "now"
	UrgencyBlocking = "blocking"
	UrgencyNormal   = "normal"
)

// nowWindowSec is how close a deadline must be for a card to count as 「会超时」.
const nowWindowSec = 3600

// Query is the GET /v1/today input.
type Query struct {
	// Since is the 「自上次打开」 watermark (unix seconds); 0 = local midnight today.
	Since int64
	// IncludeExec also lists needs_review jobs of the exec agent (off by default).
	IncludeExec bool
}

// Response is the GET /v1/today payload (design §2.1).
type Response struct {
	Digest    Digest `json:"digest"`
	Decisions []Card `json:"decisions"`
	// Snoozed is always 0 until the snooze table lands (T3).
	Snoozed     int    `json:"snoozed"`
	Status      Status `json:"status"`
	GeneratedAt int64  `json:"generated_at"`
}

// SinceLast is the 「自上次打开：完成 N · 失败 M · 新提交 K」 line.
type SinceLast struct {
	Since      int64 `json:"since"`
	JobsDone   int   `json:"jobs_done"`
	JobsFailed int   `json:"jobs_failed"`
	Commits    int   `json:"commits"`
}

// Digest is the folded one-line digest plus today's work digest (work.BuildDigest).
type Digest struct {
	SinceLast  SinceLast `json:"since_last"`
	Title      string    `json:"title"`
	Text       string    `json:"text"`
	Commentary string    `json:"commentary,omitempty"`
}

// Blocks says what a card holds up (design §2.2). Score drives the ordering, Items is
// the count behind the short 「卡住 N」 label and Text lists the sources.
type Blocks struct {
	Score int    `json:"score"`
	Items int    `json:"items"`
	Text  string `json:"text,omitempty"`
}

// Review is the hard data of a review card's 「详情」.
type Review struct {
	Commits int    `json:"commits"`
	Adds    int    `json:"adds"`
	Dels    int    `json:"dels"`
	Verify  string `json:"verify,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

// Refs are the ids a card's actions and links need.
type Refs struct {
	JobID         string `json:"job_id,omitempty"`
	InteractionID string `json:"interaction_id,omitempty"`
	DecisionID    string `json:"decision_id,omitempty"`
	// DecisionIDs (relay cards) are every unread open turn of the session, oldest
	// first: 「已读」 acks them all, since the card stands for all of them.
	DecisionIDs []string `json:"decision_ids,omitempty"`
	SessionID   string   `json:"session_id,omitempty"`
	ThreadID    string   `json:"thread_id,omitempty"`
	WorkItemID  string   `json:"work_item_id,omitempty"`
	PlanID      string   `json:"plan_id,omitempty"`
	TodoID      string   `json:"todo_id,omitempty"`
	Field       string   `json:"field,omitempty"`
	MergeID     int64    `json:"merge_id,omitempty"`
}

// Action is one card button. ID names the existing write API the console calls
// (answer / reply / ack / accept / rerun / diff / report / park / adopt /
// dismiss / resume / open); Value is the answer token for answer actions.
type Action struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Style     string `json:"style,omitempty"`
	Value     string `json:"value,omitempty"`
	NeedsText bool   `json:"needs_text,omitempty"`
}

// Advice is the steward's one-line suggestion (T4, advice.go): ActionID (an ActionKey of
// the card) becomes the 「按建议：X」 button, Digest (≤5 lines) sits in 「详情」.
type Advice struct {
	Text     string `json:"text"`
	ActionID string `json:"action_id,omitempty"`
	Digest   string `json:"digest,omitempty"`
	By       string `json:"by,omitempty"`
	At       int64  `json:"at,omitempty"`
}

// Suggestion is a pending tidy-up suggestion folded into a work card.
type Suggestion struct {
	Field string `json:"field"`
	Value string `json:"value"`
	Text  string `json:"text"`
}

// Card is one DecisionCard.
type Card struct {
	Key          string       `json:"key"`
	Kind         string       `json:"kind"`
	Tag          string       `json:"tag"`
	Urgency      string       `json:"urgency"`
	Blocks       Blocks       `json:"blocks"`
	Title        string       `json:"title"`
	ProjectKey   string       `json:"project_key,omitempty"`
	Agent        string       `json:"agent,omitempty"`
	WaitingSince int64        `json:"waiting_since"`
	ExpiresAt    int64        `json:"expires_at,omitempty"`
	ActivityAt   int64        `json:"activity_at"`
	Summary      string       `json:"summary"`
	Review       *Review      `json:"review,omitempty"`
	Suggestions  []Suggestion `json:"suggestions,omitempty"`
	Refs         Refs         `json:"refs"`
	Actions      []Action     `json:"actions"`
	Advice       *Advice      `json:"advice"`
}

// RunnerStatus is the runner part of the status bar, supplied by the server (it knows
// which workers are connected).
type RunnerStatus struct {
	Online  int      `json:"online"`
	Total   int      `json:"total"`
	Offline []string `json:"offline,omitempty"`
}

// StewardStatus reads the steward's live status (nil-safe through Deps).
type StewardStatus interface {
	Status() steward.Status
}

// Deps are the sources the service reads.
type Deps struct {
	Store   *jobstore.Store
	Work    *work.Service
	Steward StewardStatus
	Runners func() RunnerStatus
	Version func() string
	Now     func() time.Time
}

// Service builds the home page payloads.
type Service struct {
	d Deps
}

// New returns a service over deps; a nil Now uses time.Now.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d}
}

// Store exposes the underlying store (sibling aggregations reuse it).
func (s *Service) Store() *jobstore.Store { return s.d.Store }

// Now is the service clock.
func (s *Service) Now() time.Time { return s.d.Now() }

// Today builds the GET /v1/today payload.
func (s *Service) Today(q Query) (Response, error) {
	now := s.d.Now()
	cards, err := s.Decisions(q.IncludeExec)
	if err != nil {
		return Response{}, err
	}
	if err := s.applyAdvice(cards); err != nil { // T4: fill advice, prune the gone cards'
		return Response{}, err
	}
	digest, err := s.digest(now, q.Since)
	if err != nil {
		return Response{}, err
	}
	status, err := s.status(now)
	if err != nil {
		return Response{}, err
	}
	return Response{Digest: digest, Decisions: cards, Status: status, GeneratedAt: now.Unix()}, nil
}

func startOfDay(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

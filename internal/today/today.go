// Package today is the N3 「今天」 decision home (design
// docs/design/2026-10-09-n3-today-decision-home-design.md §1/§2): it folds every
// "a person must decide" signal — pending interactions, OPEN decisions, relay turns,
// needs_review jobs, jobs held for approval, work items that wait for the person, the status / goal tidy-up
// suggestions, merge suggestions and blocked plans — into one ordered card queue, plus
// the one-line digest and the status bar. It only READS existing sources: every card
// action maps to an existing write API, which the console calls directly.
//
// The httpapi layer binds / validates and forwards here (G021).
package today

import (
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/steward"
	"github.com/inhere/gofer/internal/work"
)

// Card kinds (DecisionCard.kind).
const (
	KindInteraction = "interaction"
	KindDecision    = "decision"
	KindRelay       = "relay"
	// KindPermission is a terminal agent's tool permission prompt waiting on the web
	// (Claude Code PermissionRequest; only the session's owner may answer it).
	KindPermission = "permission"
	KindReview     = "review"
	// KindApproval is a job held for a person's approval (gofer-9b1b, status
	// awaiting_approval): approve it (it then runs) or reject it (cancelled, never run).
	KindApproval    = "approval"
	KindWork        = "work"
	KindSuggestion  = "suggestion"
	KindMerge       = "merge"
	KindPlanBlocked = "plan_blocked"
	// KindMemory is a steward's memory hygiene suggestion (memory_suggest.go).
	KindMemory = "memory"
	// KindCapture is the single summary card for the capture box (design 2026-10-16 §3):
	// captured items never get a per-item KindWork card.
	KindCapture = "capture"
	// KindMailHeld is the single summary card for letters nobody could receive (the gofer
	// mailbox, plan M7): they wait as held until somebody readdresses or cancels them.
	KindMailHeld = "mail_held"
)

// CaptureCardKey is the stable key of the capture-box summary card; CaptureBoxLink is
// the web target its 「去整理」 action opens (the capture tab lands with the web
// capture box; until then /work simply loads).
const (
	CaptureCardKey = KindCapture + ":box"
	CaptureBoxLink = "/work?tab=capture"
)

// MailHeldCardKey is the stable key of the held-mail summary card; MailHeldLink is the web
// target its 「去指派」 action opens (the mailbox page's 待指派 tab).
const (
	MailHeldCardKey = KindMailHeld + ":box"
	MailHeldLink    = "/mail?state=held"
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
	// ProjectAllowed, when non-nil, keeps only the cards and snoozed rows whose
	// project_key it accepts (a caller ceiling that bounds projects, 7798 第二期). A
	// card without a project_key is a global summary (capture box, held mail) and is
	// dropped for such a caller. nil = no filter, the unchanged behaviour.
	ProjectAllowed func(string) bool
	// Projects, when non-nil, narrows the digest's 「自上次打开」 job / commit counts to
	// these literal project keys — the same restriction ProjectAllowed expresses as a
	// predicate, in the form the store's count query needs. nil = the whole server.
	Projects []string
	// Lang is the language of the generated text (a request's Accept-Language);
	// "" = the server's language.
	Lang i18n.Lang
}

// Response is the GET /v1/today payload (design §2.1).
type Response struct {
	Digest    Digest `json:"digest"`
	Decisions []Card `json:"decisions"`
	// Snoozed counts the cards held back by an active 「稍后」 (T3, snooze.go).
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

// Approval is the hard data of an approval card's 「详情」 (gofer-9b1b): why the job is
// held, who submitted it, and what it would run (an exec job's argv, or the head of an
// agent job's prompt) — the full thing is on the job page.
type Approval struct {
	Reason        string   `json:"reason,omitempty"`
	Origin        string   `json:"origin,omitempty"`
	Command       []string `json:"command,omitempty"`
	PromptPreview string   `json:"prompt_preview,omitempty"`
	TimeoutSec    int      `json:"timeout_sec,omitempty"`
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
	// Memory cards: the suggestion id and the repo-tracker memory it is about.
	MemorySuggestionID int64  `json:"memory_suggestion_id,omitempty"`
	TrackerID          string `json:"tracker_id,omitempty"`
	MemoryKey          string `json:"memory_key,omitempty"`
}

// Action is one card button. ID names the existing write API the console calls
// (answer / reply / ack / accept / rerun / diff / report / park / adopt /
// dismiss / resume / approve / reject / open); Value is the answer token for answer
// actions. NeedsText asks for a text the action cannot go without; OptionalText offers
// a text box the person may leave empty (an approval card's 「拒绝」 reason).
type Action struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	Style        string `json:"style,omitempty"`
	Value        string `json:"value,omitempty"`
	NeedsText    bool   `json:"needs_text,omitempty"`
	OptionalText bool   `json:"optional_text,omitempty"`
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
	Approval     *Approval    `json:"approval,omitempty"`
	Suggestions  []Suggestion `json:"suggestions,omitempty"`
	Memory       *MemoryCard  `json:"memory,omitempty"`
	Refs         Refs         `json:"refs"`
	Actions      []Action     `json:"actions"`
	Advice       *Advice      `json:"advice"`
	// Column is the additive inbox projection column; empty keeps old clients unchanged.
	Column     string `json:"column,omitempty"`
	Woke       bool   `json:"woke,omitempty"`
	WokeReason string `json:"woke_reason,omitempty"`
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
	// Language is the server's text language (server.language, auto resolved); nil
	// reads as the default language. It is the language of text that gets stored and of
	// reads that carry no request language.
	Language func() i18n.Lang
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

// lang resolves the language of a text: the request's, else the server's.
func (s *Service) lang(l i18n.Lang) i18n.Lang {
	if l == "" && s.d.Language != nil {
		l = s.d.Language()
	}
	return i18n.Resolve(l)
}

// Store exposes the underlying store (sibling aggregations reuse it).
func (s *Service) Store() *jobstore.Store { return s.d.Store }

// Now is the service clock.
func (s *Service) Now() time.Time { return s.d.Now() }

// Today builds the GET /v1/today payload.
func (s *Service) Today(q Query) (Response, error) {
	now := s.d.Now()
	lang := s.lang(q.Lang)
	cards, err := s.DecisionsIn(lang, q.IncludeExec)
	if err != nil {
		return Response{}, err
	}
	if err := s.applyAdvice(cards); err != nil { // T4: fill advice, prune the gone cards'
		return Response{}, err
	}
	cards, snoozed, err := s.applySnoozes(cards, q.IncludeExec)
	if err != nil {
		return Response{}, err
	}
	cards, snoozed = filterScope(cards, snoozed, q.ProjectAllowed)
	digest, err := s.digest(lang, now, q)
	if err != nil {
		return Response{}, err
	}
	status, err := s.status(lang, now)
	if err != nil {
		return Response{}, err
	}
	return Response{Digest: digest, Decisions: cards, Snoozed: len(snoozed), Status: status, GeneratedAt: now.Unix()}, nil
}

func startOfDay(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

// filterScope applies a caller's项目范围 to the card queue and the 「已稍后」 list.
// nil keeps everything (an unrestricted caller is byte-for-byte unchanged). A card
// without a project_key is a global summary: a scoped caller must not see it, because
// its counts cover every project.
func filterScope(cards []Card, snoozed []SnoozedCard, allow func(string) bool) ([]Card, []SnoozedCard) {
	if allow == nil {
		return cards, snoozed
	}
	kept := make([]Card, 0, len(cards))
	for _, c := range cards {
		if allow(c.ProjectKey) {
			kept = append(kept, c)
		}
	}
	keptSnoozed := make([]SnoozedCard, 0, len(snoozed))
	for _, sc := range snoozed {
		if allow(sc.ProjectKey) {
			keptSnoozed = append(keptSnoozed, sc)
		}
	}
	return kept, keptSnoozed
}

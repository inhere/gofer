// Package sessionrelay is the session-relay domain service (SESS-01): terminal
// agent-CLI sessions (Claude Code / Codex) register themselves through their
// hooks, the web observes them, and while a session's relay switch is on the
// Stop hook posts the agent's last message as a "turn" and blocks until a human
// answers it on the web — the answer is injected back into the SAME terminal
// session via the hook's `decision: block` continuation.
//
// Turns are plan_decisions rows (kind='relay', session_id set) so the existing
// bell / decision card / lazy-expiry machinery serves them unchanged (design D3).
// The package depends only on jobstore (data layer) and is consumed by httpapi
// (entry layer), never the other way round (G022).
package sessionrelay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// Hook event names shared by Claude Code and Codex (the hook stdin
// `hook_event_name`). Notification / Interrupt are agent-specific.
const (
	EventSessionStart     = "SessionStart"
	EventUserPromptSubmit = "UserPromptSubmit"
	EventStop             = "Stop"
	EventSessionEnd       = "SessionEnd"
	EventNotification     = "Notification"
	EventInterrupt        = "Interrupt"
)

// Turn outcomes reported by WaitTurn.
const (
	TurnOpen     = "open"      // still waiting (the wait window elapsed)
	TurnAnswered = "answered"  // a human answered; Decision.Answer holds the reply
	TurnExpired  = "expired"   // decision timeout hit (lazy expiry)
	TurnRelayOff = "relay_off" // relay switched off while waiting → hook must release
)

// Sentinel errors mapped to HTTP statuses by the entry layer.
var (
	ErrUnknownSession = errors.New("sessionrelay: unknown session")
	ErrUnknownTurn    = errors.New("sessionrelay: unknown turn")
	ErrNoOpenTurn     = errors.New("sessionrelay: session has no open turn")
	ErrRelayOff       = errors.New("sessionrelay: relay is off")
	ErrInvalidInput   = errors.New("sessionrelay: invalid input")
)

// Service owns the relay rules on top of the store.
type Service struct {
	store *jobstore.Store
	// AutoOffOnPrompt turns relay off when the human types in the terminal
	// (UserPromptSubmit): they are back at the keyboard (design D6). A reply
	// injected by the hook does NOT raise UserPromptSubmit, so web replies never
	// trip it. Default true.
	AutoOffOnPrompt bool
	// pollInterval is how often WaitTurn re-reads the decision while blocking.
	pollInterval time.Duration
	nowFn        func() time.Time
}

// NewService builds the relay service over the shared job store.
func NewService(store *jobstore.Store) *Service {
	return &Service{store: store, AutoOffOnPrompt: true, pollInterval: 500 * time.Millisecond, nowFn: time.Now}
}

// SetPollInterval overrides the WaitTurn re-read cadence (tests).
func (s *Service) SetPollInterval(d time.Duration) {
	if d > 0 {
		s.pollInterval = d
	}
}

// RegisterInput is the SessionStart / first-contact registration.
type RegisterInput struct {
	SessionID  string
	Agent      string
	ProjectKey string
	Runner     string
	Cwd        string
	Title      string
	Transcript string
	TmuxPane   string
	Event      string
}

// Register upserts a session (see jobstore.UpsertAgentSession for the merge
// rules). Agent is required on first contact.
func (s *Service) Register(in RegisterInput) (jobstore.AgentSession, error) {
	if strings.TrimSpace(in.SessionID) == "" {
		return jobstore.AgentSession{}, fmt.Errorf("%w: session_id required", ErrInvalidInput)
	}
	agent := strings.ToLower(strings.TrimSpace(in.Agent))
	if agent == "" {
		if _, ok, _ := s.store.GetAgentSession(in.SessionID); !ok {
			return jobstore.AgentSession{}, fmt.Errorf("%w: agent required", ErrInvalidInput)
		}
	}
	return s.store.UpsertAgentSession(jobstore.AgentSession{
		SessionID: in.SessionID, Agent: agent, ProjectKey: in.ProjectKey, Runner: in.Runner,
		Cwd: in.Cwd, Title: in.Title, Transcript: in.Transcript, TmuxPane: in.TmuxPane,
		LastEvent: in.Event,
	})
}

// HeartbeatInput is a per-event update. State "" derives from Event via
// DefaultState; LastMessage is the agent's last assistant text (Stop).
type HeartbeatInput struct {
	Event       string
	State       string
	LastMessage string
	Title       string
}

// DefaultState maps a hook event to the session state it implies when the
// caller does not choose one. Unknown events keep the current state ("").
func DefaultState(event string) string {
	switch event {
	case EventSessionStart, EventUserPromptSubmit:
		return jobstore.SessionRunning
	case EventStop, EventInterrupt:
		return jobstore.SessionIdle
	case EventSessionEnd:
		return jobstore.SessionEnded
	case EventNotification:
		return jobstore.SessionNeedsAttention
	}
	return ""
}

// Heartbeat applies a hook event to the session and returns the updated row
// (its Relay field is what the Stop hook keys on). A UserPromptSubmit auto-offs
// relay when AutoOffOnPrompt is set. Unknown session → ErrUnknownSession (the
// hook then registers and retries; hooks may start mid-session after an
// upgrade).
func (s *Service) Heartbeat(sid string, in HeartbeatInput) (jobstore.AgentSession, error) {
	state := in.State
	if state == "" {
		state = DefaultState(in.Event)
	}
	if in.Event == EventUserPromptSubmit && s.AutoOffOnPrompt {
		if _, err := s.SetRelay(sid, false); err != nil && !errors.Is(err, ErrUnknownSession) {
			return jobstore.AgentSession{}, err
		}
	}
	a, ok, err := s.store.TouchAgentSession(sid, jobstore.SessionHeartbeat{
		Event: in.Event, State: state, LastMessage: in.LastMessage, Title: in.Title,
	})
	if err != nil {
		return jobstore.AgentSession{}, err
	}
	if !ok {
		return jobstore.AgentSession{}, ErrUnknownSession
	}
	return a, nil
}

// SetRelay flips the switch. Turning it OFF also expires any OPEN turn (so a
// blocked hook releases on its next poll) and moves a waiting session to idle.
func (s *Service) SetRelay(sid string, on bool) (jobstore.AgentSession, error) {
	ok, err := s.store.SetSessionRelay(sid, on)
	if err != nil {
		return jobstore.AgentSession{}, err
	}
	if !ok {
		return jobstore.AgentSession{}, ErrUnknownSession
	}
	if !on {
		if _, err := s.store.ExpireSessionDecisions(sid); err != nil {
			return jobstore.AgentSession{}, err
		}
		if a, ok, _ := s.store.GetAgentSession(sid); ok && a.State == jobstore.SessionWaitingReply {
			if _, err := s.store.SetSessionState(sid, jobstore.SessionIdle); err != nil {
				return jobstore.AgentSession{}, err
			}
		}
	}
	a, ok, err := s.store.GetAgentSession(sid)
	if err != nil {
		return jobstore.AgentSession{}, err
	}
	if !ok {
		return jobstore.AgentSession{}, ErrUnknownSession
	}
	return a, nil
}

// maxTurnBody caps the relayed message (bytes); the hook truncates earlier.
const maxTurnBody = 16 * 1024

// OpenTurn posts the agent's last message as a relay turn: bumps turn_no,
// inserts an OPEN decision (kind=relay, free-text answer) and marks the session
// waiting_reply. timeoutSec is the hook's remaining budget (clamped by the
// store). Relay must be on (ErrRelayOff otherwise — the hook re-checks the
// switch through Heartbeat first, this guards the race).
func (s *Service) OpenTurn(sid, body string, timeoutSec int64) (jobstore.PlanDecision, error) {
	a, ok, err := s.store.GetAgentSession(sid)
	if err != nil {
		return jobstore.PlanDecision{}, err
	}
	if !ok {
		return jobstore.PlanDecision{}, ErrUnknownSession
	}
	if !a.Relay {
		return jobstore.PlanDecision{}, ErrRelayOff
	}
	body = strings.TrimSpace(body)
	if body == "" {
		body = "(agent stopped without a message)"
	}
	if len(body) > maxTurnBody {
		body = body[:maxTurnBody]
	}
	// A previous turn still OPEN (hook died before consuming) must not linger:
	// the newest turn is the only one that can be answered.
	if _, err := s.store.ExpireSessionDecisions(sid); err != nil {
		return jobstore.PlanDecision{}, err
	}
	turn, _, err := s.store.IncrSessionTurn(sid)
	if err != nil {
		return jobstore.PlanDecision{}, err
	}
	d := jobstore.PlanDecision{
		Title:      fmt.Sprintf("%s · turn %d", sessionLabel(a), turn),
		Question:   body,
		TimeoutSec: timeoutSec,
		SessionID:  sid,
		Kind:       jobstore.DecisionKindRelay,
	}
	if err := s.store.InsertDecision(&d); err != nil {
		return jobstore.PlanDecision{}, err
	}
	if _, _, err := s.store.TouchAgentSession(sid, jobstore.SessionHeartbeat{
		Event: EventStop, State: jobstore.SessionWaitingReply, LastMessage: body,
	}); err != nil {
		return jobstore.PlanDecision{}, err
	}
	return d, nil
}

// sessionLabel is the short human label used in turn titles: the session title
// when set, else agent + short id.
func sessionLabel(a jobstore.AgentSession) string {
	if t := strings.TrimSpace(a.Title); t != "" {
		if len(t) > 40 {
			return t[:40] + "…"
		}
		return t
	}
	id := a.SessionID
	if len(id) > 8 {
		id = id[:8]
	}
	return a.Agent + " " + id
}

// TurnStatus is WaitTurn's answer: the current decision row, the session's
// relay flag and the derived Outcome.
type TurnStatus struct {
	Outcome  string
	Relay    bool
	Decision jobstore.PlanDecision
}

// WaitTurn blocks up to wait (0 = single read) until the turn is answered,
// expired, or the session's relay is switched off; it then reports the outcome.
// ctx cancellation ends the wait early with the last observed status.
func (s *Service) WaitTurn(ctx context.Context, sid, decisionID string, wait time.Duration) (TurnStatus, error) {
	deadline := s.nowFn().Add(wait)
	for {
		st, err := s.readTurn(sid, decisionID)
		if err != nil {
			return TurnStatus{}, err
		}
		if st.Outcome != TurnOpen || wait <= 0 || !s.nowFn().Before(deadline) {
			return st, nil
		}
		select {
		case <-ctx.Done():
			return st, nil
		case <-time.After(s.pollInterval):
		}
	}
}

func (s *Service) readTurn(sid, decisionID string) (TurnStatus, error) {
	d, ok, err := s.store.GetDecision(decisionID)
	if err != nil {
		return TurnStatus{}, err
	}
	if !ok || d.SessionID != sid {
		return TurnStatus{}, ErrUnknownTurn
	}
	a, ok, err := s.store.GetAgentSession(sid)
	if err != nil {
		return TurnStatus{}, err
	}
	if !ok {
		return TurnStatus{}, ErrUnknownSession
	}
	st := TurnStatus{Relay: a.Relay, Decision: d}
	switch {
	case d.State == jobstore.DecisionAnswered:
		st.Outcome = TurnAnswered
		// The answer may have landed through the generic decision endpoint a
		// moment ago (or Say's OnAnswered may not have run yet): settle the
		// session state here too so whoever observes "answered" sees running.
		s.OnAnswered(d)
	case !a.Relay:
		st.Outcome = TurnRelayOff
	case d.State == jobstore.DecisionExpired:
		st.Outcome = TurnExpired
	default:
		st.Outcome = TurnOpen
	}
	return st, nil
}

// Say answers the session's newest OPEN turn (the web input box / `gofer
// session say`). ErrNoOpenTurn when nothing is waiting.
func (s *Service) Say(sid, answer, by string) (jobstore.PlanDecision, error) {
	if strings.TrimSpace(answer) == "" {
		return jobstore.PlanDecision{}, fmt.Errorf("%w: empty answer", ErrInvalidInput)
	}
	if _, ok, err := s.store.GetAgentSession(sid); err != nil {
		return jobstore.PlanDecision{}, err
	} else if !ok {
		return jobstore.PlanDecision{}, ErrUnknownSession
	}
	open, err := s.store.ListSessionDecisions(sid, jobstore.DecisionOpen, 1)
	if err != nil {
		return jobstore.PlanDecision{}, err
	}
	if len(open) == 0 {
		return jobstore.PlanDecision{}, ErrNoOpenTurn
	}
	id := open[0].ID
	ok, err := s.store.AnswerDecision(id, answer, by)
	if err != nil {
		return jobstore.PlanDecision{}, err
	}
	if !ok {
		return jobstore.PlanDecision{}, ErrNoOpenTurn
	}
	d, _, err := s.store.GetDecision(id)
	if err != nil {
		return jobstore.PlanDecision{}, err
	}
	s.OnAnswered(d)
	return d, nil
}

// OnAnswered is the post-answer hook shared with the generic decision endpoint
// (POST /v1/decisions/{id}/answer answering a relay turn from the bell): a
// waiting session goes back to running. Non-relay decisions are ignored.
func (s *Service) OnAnswered(d jobstore.PlanDecision) {
	if d.SessionID == "" || d.State != jobstore.DecisionAnswered {
		return
	}
	if a, ok, _ := s.store.GetAgentSession(d.SessionID); ok && a.State == jobstore.SessionWaitingReply {
		_, _ = s.store.SetSessionState(d.SessionID, jobstore.SessionRunning)
	}
}

// Detail is a session plus its recent turns (newest first).
type Detail struct {
	Session jobstore.AgentSession
	Turns   []*jobstore.PlanDecision
}

// Get returns one session with its recent turns.
func (s *Service) Get(sid string, turnLimit int) (Detail, error) {
	a, ok, err := s.store.GetAgentSession(sid)
	if err != nil {
		return Detail{}, err
	}
	if !ok {
		return Detail{}, ErrUnknownSession
	}
	turns, err := s.store.ListSessionDecisions(sid, "", turnLimit)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Session: a, Turns: turns}, nil
}

// List lists sessions (see jobstore.ListSessionsOpts).
func (s *Service) List(opts jobstore.ListSessionsOpts) ([]jobstore.AgentSession, error) {
	return s.store.ListAgentSessions(opts)
}

// Resolve picks the session `gofer session relay on` (run without --session)
// means: the live sessions whose cwd equals or contains cwd, most recent first.
// The caller disambiguates when more than one comes back.
func (s *Service) Resolve(cwd string) ([]jobstore.AgentSession, error) {
	if strings.TrimSpace(cwd) == "" {
		return nil, fmt.Errorf("%w: cwd required", ErrInvalidInput)
	}
	return s.store.ListAgentSessions(jobstore.ListSessionsOpts{Cwd: cwd, Limit: 20})
}

// Delete removes the registration (turns stay for audit).
func (s *Service) Delete(sid string) error {
	ok, err := s.store.DeleteAgentSession(sid)
	if err != nil {
		return err
	}
	if !ok {
		return ErrUnknownSession
	}
	return nil
}

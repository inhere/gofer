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

// ReleaseByUserReturned tags a turn closed without an answer because the hook
// saw the human come back to the keyboard (SR-A5 idle auto-arm).
const ReleaseByUserReturned = "user_returned"

// Wait reasons: why a Stop waits for a web reply, reported to the hook so it
// can log/pick its poll cadence (R2). "" = it does not wait at all.
const (
	// WaitModeOn is the human's explicit switch: every Stop waits.
	WaitModeOn = "mode_on"
	// WaitIdleProbe is the keyboard idle rule: the hook's system input reading
	// reached the threshold, and the wait is released when it drops back.
	WaitIdleProbe = "idle_probe"
	// WaitTurnAge is the fallback for a terminal that cannot be probed at all
	// (containers without X11): enough time passed since the last HUMAN input in
	// this session, and the wait is released by the next human-input event
	// (Esc / typing) rather than by a reading.
	WaitTurnAge = "turn_age"
)

// Notifier is the outbound-notification seam (OBS-07a). The relay knows WHEN a
// human is needed; it must not know about webhooks, IM adapters or config — the
// entry layer injects an implementation (job.Service). nil = no notification.
type Notifier interface {
	NotifySessionWaiting(sessionID, projectKey, title, lastMessage string, turn int64)
	NotifySessionAttention(sessionID, projectKey, title, detail string)
}

// Service owns the relay rules on top of the store.
type Service struct {
	store    *jobstore.Store
	notifier Notifier
	// AutoOffOnPrompt drops an explicit `on` switch back to `auto` when the
	// human types in the terminal (UserPromptSubmit): they are back at the
	// keyboard (design D6, R1). A reply injected by the hook does NOT raise a
	// human UserPromptSubmit, so web replies never trip it. Default true.
	AutoOffOnPrompt bool
	// AutoArmIdleSec is the keyboard idle auto-arm threshold in seconds
	// (session.auto_relay_idle_sec, SR-A5): a session whose last reported system
	// input idle is at least this long behaves as if its relay switch were on,
	// even though the human never flipped it. 0 = this criterion disabled. Set
	// once at construction from config.
	AutoArmIdleSec int
	// AutoArmTurnSec is the turn-age fallback threshold in seconds
	// (session.auto_relay_turn_sec, R2): on a terminal where the idle probe does
	// not work at all (idle_sec stays -1 — a Linux container with no X11), a
	// session that has seen no HUMAN input for this long waits anyway. 0 = this
	// criterion disabled. Set once at construction from config.
	AutoArmTurnSec int
	// pollInterval is how often WaitTurn re-reads the decision while blocking.
	pollInterval time.Duration
	nowFn        func() time.Time
	// injector runs the internal exec jobs of path A (deliver.go, design §9.1 A).
	// nil = no executor wired: Deliver reports no_runner instead of pretending.
	injector Injector
	// injectCommands is the foreground-process whitelist of path A
	// (session.inject_commands); empty keeps DefaultInjectCommands.
	injectCommands []string
}

// NewService builds the relay service over the shared job store.
func NewService(store *jobstore.Store) *Service {
	return &Service{
		store: store, AutoOffOnPrompt: true, pollInterval: 500 * time.Millisecond, nowFn: time.Now,
		injectCommands: DefaultInjectCommands(),
	}
}

// SetNotifier injects the outbound notifier (see Notifier). Safe to leave unset:
// the relay then simply sends no notifications.
func (s *Service) SetNotifier(n Notifier) { s.notifier = n }

// SetPollInterval overrides the WaitTurn re-read cadence (tests).
func (s *Service) SetPollInterval(d time.Duration) {
	if d > 0 {
		s.pollInterval = d
	}
}

// WaitReason answers "does a Stop on this session wait for a web reply, and
// why" — the single place the relay's rules live (R1/R2). "" means it does not
// wait; otherwise the value is one of WaitModeOn / WaitIdleProbe / WaitTurnAge.
//
//   - `on`: the human's explicit switch, authoritative over every rule below.
//   - `off`: never.
//   - `auto`: the idle probe when it WORKED (idle >= 0) — a machine where the
//     human is demonstrably at the keyboard or away is not second-guessed by the
//     turn-age clock — else, on a terminal that cannot be probed at all
//     (containers without X11 keep reporting -1), the time since the last HUMAN
//     input in this session: last_human_at == 0 means no human was ever seen and
//     is not evidence of absence.
func (s *Service) WaitReason(a jobstore.AgentSession) string {
	switch a.RelayMode {
	case jobstore.RelayModeOn:
		return WaitModeOn
	case jobstore.RelayModeOff:
		return ""
	}
	if s.AutoArmIdleSec > 0 && a.IdleSec >= 0 {
		if a.IdleSec >= int64(s.AutoArmIdleSec) {
			return WaitIdleProbe
		}
		return ""
	}
	if s.AutoArmTurnSec > 0 && a.LastHumanAt > 0 {
		if s.nowFn().Unix()-a.LastHumanAt >= int64(s.AutoArmTurnSec) {
			return WaitTurnAge
		}
	}
	return ""
}

// AutoArmed reports whether the KEYBOARD IDLE rule alone arms this session (the
// human has been away for >= AutoArmIdleSec, measured by the last reading the
// hook reported). It is the `auto_armed` the web renders as "auto (idle 12m)".
func (s *Service) AutoArmed(a jobstore.AgentSession) bool {
	return s.WaitReason(a) == WaitIdleProbe
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
// rules). Agent is required on first contact. A SessionStart stamps
// last_human_at: launching the agent is a human act, and it is the anchor the
// turn-age fallback measures from (R2).
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
	humanAt := int64(0)
	if in.Event == EventSessionStart {
		humanAt = s.nowFn().Unix()
	}
	return s.store.UpsertAgentSession(jobstore.AgentSession{
		SessionID: in.SessionID, Agent: agent, ProjectKey: in.ProjectKey, Runner: in.Runner,
		Cwd: in.Cwd, Title: in.Title, Transcript: in.Transcript, TmuxPane: in.TmuxPane,
		LastEvent: in.Event, LastHumanAt: humanAt,
	})
}

// HeartbeatInput is a per-event update. State "" derives from Event via
// DefaultState; LastMessage is the agent's last assistant text (Stop).
type HeartbeatInput struct {
	Event       string
	State       string
	LastMessage string
	Title       string
	// Injected marks a UserPromptSubmit raised by the relay's own continuation
	// (the hook recognises its ReplyPrefix): it must NOT auto-off relay — the
	// human is still on the web, not at the keyboard.
	Injected bool
	// IdleSec is the hook's system input idle reading (SR-A5, -1 = unknown). nil
	// = this event carried none, so the stored reading stays.
	IdleSec *int64
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
// (its WaitReason is what the Stop hook keys on). A UserPromptSubmit typed by a
// HUMAN demotes an explicit `on` switch back to `auto` when AutoOffOnPrompt is
// set, and releases any wait the automatic rules had armed (R1/R2). Unknown
// session → ErrUnknownSession (the hook then registers and retries; hooks may
// start mid-session after an upgrade).
func (s *Service) Heartbeat(sid string, in HeartbeatInput) (jobstore.AgentSession, error) {
	state := in.State
	if state == "" {
		state = DefaultState(in.Event)
	}
	// A prompt the human typed, or a terminal interrupt, PROVES they are back.
	// An injected prompt is our own continuation: the human is on the web, still
	// away, and nothing below may treat it as a return.
	human := in.Event == EventUserPromptSubmit && !in.Injected
	if human || in.Event == EventInterrupt {
		if human && s.AutoOffOnPrompt {
			if prev, ok, _ := s.store.GetAgentSession(sid); ok && prev.RelayMode == jobstore.RelayModeOn {
				if _, err := s.SetRelayMode(sid, jobstore.RelayModeAuto); err != nil && !errors.Is(err, ErrUnknownSession) {
					return jobstore.AgentSession{}, err
				}
			}
		}
		if err := s.releaseAutoTurns(sid); err != nil {
			return jobstore.AgentSession{}, err
		}
	}
	// Read the prior state only when this beat could raise attention, so the
	// common path (Stop / prompt) keeps its single write.
	prevState, prevMsg := "", ""
	if state == jobstore.SessionNeedsAttention {
		if prev, ok, _ := s.store.GetAgentSession(sid); ok {
			prevState, prevMsg = prev.State, prev.LastMessage
		}
	}
	a, ok, err := s.store.TouchAgentSession(sid, jobstore.SessionHeartbeat{
		Event: in.Event, State: state, LastMessage: in.LastMessage, Title: in.Title,
		IdleSec: in.IdleSec, HumanInput: human,
	})
	if err != nil {
		return jobstore.AgentSession{}, err
	}
	if !ok {
		return jobstore.AgentSession{}, ErrUnknownSession
	}
	// Needs attention while relayed: tell the human their session is blocked on a
	// terminal dialog. An agent re-raises the SAME notification while it waits, so
	// suppress identical repeats — but a different prompt raised while still
	// waiting is a new thing to know about, and must not be swallowed.
	if s.notifier != nil && s.WaitReason(a) != "" && a.State == jobstore.SessionNeedsAttention {
		fresh := prevState != jobstore.SessionNeedsAttention ||
			(in.LastMessage != "" && in.LastMessage != prevMsg)
		if fresh {
			s.notifier.NotifySessionAttention(sid, a.ProjectKey, a.Title, in.LastMessage)
		}
	}
	return a, nil
}

// releaseAutoTurns closes the session's OPEN turns because a human-input event
// just arrived (R2): the turn-age fallback has no keyboard probe, so "the human
// typed / interrupted" IS the evidence they are back, and the blocked hook sees
// the turn settle and lets the agent stop. Turns of an EXPLICITLY switched-on
// session are left alone — only their owner ends those (the switch, or /off).
// The caller's beat moves the session out of waiting_reply (the event's own
// state, running or idle), so this only settles turns.
func (s *Service) releaseAutoTurns(sid string) error {
	a, ok, err := s.store.GetAgentSession(sid)
	if err != nil || !ok {
		return err // unknown session: reported by the beat that follows
	}
	if a.RelayMode == jobstore.RelayModeOn {
		return nil
	}
	open, err := s.store.ListSessionDecisions(sid, jobstore.DecisionOpen, 20)
	if err != nil {
		return err
	}
	for _, d := range open {
		if _, err := s.store.ReleaseDecision(d.ID, ReleaseByUserReturned); err != nil {
			return err
		}
	}
	return nil
}

// SetRelayMode stores the session's three-state switch (auto | on | off, R1).
// A change that ends a wait — `off` (never waits), or dropping an explicit `on`
// back to `auto` — also expires any OPEN turn and moves a waiting session to
// idle, so a hook blocked on it releases.
func (s *Service) SetRelayMode(sid, mode string) (jobstore.AgentSession, error) {
	if !jobstore.ValidRelayMode(mode) {
		return jobstore.AgentSession{}, fmt.Errorf("%w: relay mode must be auto|on|off, got %q", ErrInvalidInput, mode)
	}
	prev, ok, err := s.store.GetAgentSession(sid)
	if err != nil {
		return jobstore.AgentSession{}, err
	}
	if !ok {
		return jobstore.AgentSession{}, ErrUnknownSession
	}
	// Order matters for a concurrently blocked WaitTurn: settle the session
	// state FIRST, then store the mode (which releases the waiter with
	// outcome relay_off, since the wait decision is now false), then expire the
	// turn — so whoever observes the release also sees idle, and sees relay_off
	// rather than expired.
	cancel := mode == jobstore.RelayModeOff ||
		(prev.RelayMode == jobstore.RelayModeOn && mode == jobstore.RelayModeAuto)
	if cancel && prev.State == jobstore.SessionWaitingReply {
		if _, err := s.store.SetSessionState(sid, jobstore.SessionIdle); err != nil {
			return jobstore.AgentSession{}, err
		}
	}
	changed, err := s.store.SetSessionRelayMode(sid, mode)
	if err != nil {
		return jobstore.AgentSession{}, err
	}
	if !changed {
		return jobstore.AgentSession{}, ErrUnknownSession
	}
	if cancel {
		if _, err := s.store.ExpireSessionDecisions(sid); err != nil {
			return jobstore.AgentSession{}, err
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
// store). The session must currently wait — explicitly (`on`), or through one of
// the auto rules (WaitReason) — else ErrRelayOff (the hook re-checks the switch
// through Heartbeat first, this guards the race).
func (s *Service) OpenTurn(sid, body string, timeoutSec int64) (jobstore.PlanDecision, error) {
	a, ok, err := s.store.GetAgentSession(sid)
	if err != nil {
		return jobstore.PlanDecision{}, err
	}
	if !ok {
		return jobstore.PlanDecision{}, ErrUnknownSession
	}
	if s.WaitReason(a) == "" {
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
	// Tell the human their session is waiting (best-effort, never blocks the
	// hook: the notifier only enqueues, the delivery sweeper does the posting).
	if s.notifier != nil {
		s.notifier.NotifySessionWaiting(sid, a.ProjectKey, a.Title, body, turn)
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

// TurnStatus is WaitTurn's answer: the current decision row, whether the
// session still waits (and why) and the derived Outcome.
type TurnStatus struct {
	Outcome string
	// Relay reports that the session currently waits for a reply (see WaitReason
	// for the vocabulary); Reason is WHY — it is how the hook picks its poll
	// cadence and what it logs.
	Relay    bool
	Reason   string
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
	reason := s.WaitReason(a)
	st := TurnStatus{Relay: reason != "", Reason: reason, Decision: d}
	switch {
	case d.State == jobstore.DecisionAnswered:
		st.Outcome = TurnAnswered
		// The answer may have landed through the generic decision endpoint a
		// moment ago (or Say's OnAnswered may not have run yet): settle the
		// session state here too so whoever observes "answered" sees running.
		s.OnAnswered(d)
	case !st.Relay:
		st.Outcome = TurnRelayOff
	case d.State == jobstore.DecisionExpired:
		st.Outcome = TurnExpired
		// Nobody answered within the hook's budget: the hook releases and the
		// agent stops normally, so the session is idle, not waiting.
		if a.State == jobstore.SessionWaitingReply {
			_, _ = s.store.SetSessionState(sid, jobstore.SessionIdle)
		}
	default:
		st.Outcome = TurnOpen
	}
	return st, nil
}

// ReleaseTurn closes an OPEN turn because the hook's FRESH idle reading shows
// the human is back at the keyboard (SR-A5 item 3): the wait was armed by the
// keyboard idle rule (WaitIdleProbe), not by an explicit switch, and the new
// reading is below the threshold. The turn ends EXPIRED tagged
// released_by=user_returned and the session goes idle, so the agent stops
// normally at its prompt. The turn-age fallback has no probe: its release
// arrives as a human-input event instead (see releaseAutoTurns).
//
// released=false means "keep waiting": still away (idle above the threshold or
// unreadable), an explicitly switched-on relay (that one is only ever turned
// off by the human — typing in the terminal or the web /off), or a turn that
// already settled (an answer that beat the release wins).
func (s *Service) ReleaseTurn(sid, turnID string, idleSec int64) (bool, error) {
	a, ok, err := s.store.GetAgentSession(sid)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, ErrUnknownSession
	}
	if !s.AutoArmed(a) {
		return false, nil
	}
	if idleSec < 0 || idleSec >= int64(s.AutoArmIdleSec) {
		return false, nil
	}
	d, ok, err := s.store.GetDecision(turnID)
	if err != nil {
		return false, err
	}
	if !ok || d.SessionID != sid {
		return false, ErrUnknownTurn
	}
	released, err := s.store.ReleaseDecision(turnID, ReleaseByUserReturned)
	if err != nil || !released {
		return false, err
	}
	if a.State == jobstore.SessionWaitingReply {
		_, _ = s.store.SetSessionState(sid, jobstore.SessionIdle)
	}
	return true, nil
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

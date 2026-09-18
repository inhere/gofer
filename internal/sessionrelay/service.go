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
	"log/slog"
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
	// ErrNotHandedOff reports a release of a session that is not (or no longer)
	// taken over by a pty job: the takeover button is shown only while it is, so
	// this is a stale request rather than a no-op to hide.
	ErrNotHandedOff = errors.New("sessionrelay: session is not handed off")
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
	// NotifySessionHandedOff is path B (design §9.1 B): the session was taken over
	// by a pty job, so the conversation continues there — the human who was about
	// to answer on their phone needs the new address, not another reply box.
	NotifySessionHandedOff(sessionID, projectKey, title, jobID string)
	// NotifySessionTakeoverReleased is its DUAL (SUP-02 R1): the takeover job
	// reached a terminal state and the session is back with its original terminal.
	// reason says why the job ended ("job_done" / "job_failed" / …), so a human
	// who was told the conversation had moved learns it can come back.
	NotifySessionTakeoverReleased(sessionID, projectKey, title, jobID, reason string)
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
	// SkipWhenSupervising is the SUP-01 D gate
	// (session.auto_relay_skip_when_supervising): while the session's caller has
	// jobs in flight, `auto` does not arm the relay at all — the person behind
	// that caller is watching the job, and an armed Stop would block the hook (and
	// the job's completion notice) until they came back. False (the zero value)
	// keeps the pre-D behaviour; config turns it ON by default. `on`/`off` are
	// never affected. Set once at construction from config.
	SkipWhenSupervising bool
	// SupervisingWindowSec is how far back the gate looks for the caller's live
	// jobs (session.supervising_window_sec); 0 = no window. Set with
	// SkipWhenSupervising.
	SupervisingWindowSec int
	// pollInterval is how often WaitTurn re-reads the decision while blocking.
	pollInterval time.Duration
	nowFn        func() time.Time
	// injector runs the internal exec jobs of path A (deliver.go, design §9.1 A).
	// nil = no executor wired: Deliver reports no_runner instead of pretending.
	injector Injector
	// takeoverer plans and submits path B's interactive resume jobs (deliver.go,
	// design §9.1 B). nil = no executor wired: a takeover reports no_runner.
	takeoverer Takeoverer
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
//   - `auto`: nothing at all while the caller still has live jobs (SUP-01 D, see
//     SkipWhenSupervising); else the idle probe when it WORKED (idle >= 0) — a
//     machine where the human is demonstrably at the keyboard or away is not
//     second-guessed by the turn-age clock — else, on a terminal that cannot be
//     probed at all (containers without X11 keep reporting -1), the time since
//     the last HUMAN input in this session: last_human_at == 0 means no human was
//     ever seen and is not evidence of absence.
func (s *Service) WaitReason(a jobstore.AgentSession) string {
	reason, _ := s.waitDecision(a)
	return reason
}

// WaitDecision is WaitReason plus the one-line explanation of a NON-waiting
// session ("supervising 2 jobs"): the web shows it next to the relay switch, and
// the hook logs it, so "why did my Stop not wait" is answerable without reading
// the store. One lookup, so a caller that needs both does not count jobs twice.
func (s *Service) WaitDecision(a jobstore.AgentSession) (reason, detail string) {
	return s.waitDecision(a)
}

func (s *Service) waitDecision(a jobstore.AgentSession) (reason, detail string) {
	// A handed-off session belongs to the takeover process (design §9.1 B): the
	// original terminal's relay is over — no switch, no idle rule, no turn-age
	// fallback may arm a Stop there again, or two processes would write one CLI
	// session.
	if a.State == jobstore.SessionHandedOff {
		return "", ""
	}
	switch a.RelayMode {
	case jobstore.RelayModeOn:
		return WaitModeOn, ""
	case jobstore.RelayModeOff:
		return "", ""
	}
	if d, ok := s.supervisingDetail(a); ok {
		return "", d
	}
	if s.AutoArmIdleSec > 0 && a.IdleSec >= 0 {
		if a.IdleSec >= int64(s.AutoArmIdleSec) {
			return WaitIdleProbe, ""
		}
		return "", ""
	}
	if s.AutoArmTurnSec > 0 && a.LastHumanAt > 0 {
		if s.nowFn().Unix()-a.LastHumanAt >= int64(s.AutoArmTurnSec) {
			return WaitTurnAge, ""
		}
	}
	return "", ""
}

// supervisingDetail applies the SUP-01 D gate (bd h-aii-s2v4): the caller behind
// this session still has jobs in flight, so the person (or supervisor agent)
// driving it is watching that work — an armed Stop would park the hook until they
// come back, and the job's completion notice would never reach them. ok=false
// means the gate does not apply (knob off, no caller to judge by, or nothing
// live), and the ordinary auto rules decide.
//
// A store failure is deliberately read as "not supervising": the gate exists to
// save the human a manual unblock, never to change what a Stop does on its own,
// so a broken lookup must fall back to the previous behaviour (logged, not
// silently swallowed).
func (s *Service) supervisingDetail(a jobstore.AgentSession) (string, bool) {
	if !s.SkipWhenSupervising || a.CallerID == "" {
		return "", false
	}
	since := int64(0)
	if s.SupervisingWindowSec > 0 {
		since = s.nowFn().Unix() - int64(s.SupervisingWindowSec)
	}
	n, err := s.store.CountActiveJobsByCaller(a.CallerID, since)
	if err != nil {
		slog.Warn("sessionrelay: count supervising jobs failed", "session_id", a.SessionID,
			"caller_id", a.CallerID, "err", err)
		return "", false
	}
	if n == 0 {
		return "", false
	}
	return fmt.Sprintf("supervising %d jobs", n), true
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
	// CallerID is the authenticated caller the entry layer stamped on this
	// request (SUP-01 D / bd h-aii-esus): the session's owner. "" when the server
	// has no token configured. It is recorded on first contact and never
	// overwritten by a later registration or beat.
	CallerID string
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
		LastEvent: in.Event, LastHumanAt: humanAt, CallerID: in.CallerID,
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
	// CallerID is the authenticated caller of this beat. It FILLS an owner-less
	// session (registered by a hook that predates the column, or before a token
	// was configured) on the same write; a session that already has an owner keeps
	// it — ownership is decided at first contact, never taken over by a later beat.
	CallerID string
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
		IdleSec: in.IdleSec, HumanInput: human, CallerID: in.CallerID,
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
	// Path B owns the session now: the original terminal must let its agent stop
	// instead of parking a turn nobody will answer there (design §9.1 B). WaitReason
	// already reports "" for it — this guards the race where the takeover landed
	// between the heartbeat and this call.
	if a.State == jobstore.SessionHandedOff {
		return jobstore.PlanDecision{}, fmt.Errorf("%w: the session was taken over by job %s",
			ErrRelayOff, a.HandedOffJobID)
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
	// cadence and what it logs. Detail explains a NON-waiting session (SUP-01 D:
	// "supervising 2 jobs"), so a hook released by the supervision gate can say so.
	Relay    bool
	Reason   string
	Detail   string
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
	reason, detail := s.WaitDecision(a)
	st := TurnStatus{Relay: reason != "", Reason: reason, Detail: detail, Decision: d}
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

// Session returns one session row without its turns. The entry layer needs it
// before dispatching to Say/Deliver/SetRelayMode: the owner check (SUP-01 D) is
// about WHO is calling, which only the entry layer knows, while the row itself
// is the domain service's to fetch.
func (s *Service) Session(sid string) (jobstore.AgentSession, error) {
	a, ok, err := s.store.GetAgentSession(sid)
	if err != nil {
		return jobstore.AgentSession{}, err
	}
	if !ok {
		return jobstore.AgentSession{}, ErrUnknownSession
	}
	return a, nil
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

// ReleaseTakeover undoes path B's takeover (design §9.1 B): the takeover job is
// cancelled FIRST — while it runs, IT owns the CLI session, and releasing the
// original terminal into a session two processes are writing would diverge it —
// and only then does the session return to idle with handed_off_* cleared, so the
// terminal that registered it relays again.
//
// A cancel failure is reported rather than swallowed: the caller asked for the
// session back, and the one thing we must not do is pretend they have it while a
// live process continues to hold it.
func (s *Service) ReleaseTakeover(ctx context.Context, sid string) (jobstore.AgentSession, error) {
	a, ok, err := s.store.GetAgentSession(sid)
	if err != nil {
		return jobstore.AgentSession{}, err
	}
	if !ok {
		return jobstore.AgentSession{}, ErrUnknownSession
	}
	if a.State != jobstore.SessionHandedOff {
		return jobstore.AgentSession{}, fmt.Errorf("%w: session %s is %s", ErrNotHandedOff, sid, a.State)
	}
	return s.releaseTakeover(ctx, a, true)
}

// ReleaseTakeoverForJob is the AUTOMATIC release (SUP-02 R1): the pty job that
// holds a session reached a terminal state, so the session no longer has an owner
// and goes back to idle — the alternative is a session nobody can speak to, since
// new turns are refused while it is handed off and the process that held it is
// gone.
//
// Unlike the manual release this does NOT cancel the job (there is nothing left to
// stop — its row only supplies the reason code) and it is a silent no-op for every
// job that is not a takeover (false). Callers run it from the job service's
// terminal hook, where a failure is logged rather than surfaced: released reports
// whether a session was actually handed back.
func (s *Service) ReleaseTakeoverForJob(ctx context.Context, jobID string) (bool, error) {
	a, ok, err := s.store.GetSessionByHandedOffJob(jobID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	released, err := s.releaseTakeover(ctx, a, false)
	if err != nil {
		return false, err
	}
	// Announced only after the release is durable: a notification claiming the
	// terminal can relay again must never precede the state that makes it true.
	if s.notifier != nil {
		s.notifier.NotifySessionTakeoverReleased(released.SessionID, released.ProjectKey,
			released.Title, jobID, "job_"+s.jobStatus(jobID))
	}
	return true, nil
}

// releaseTakeover is the shared body of both releases: cancel the takeover job
// (only when the caller is a LIVE release) and then clear the handoff.
func (s *Service) releaseTakeover(ctx context.Context, a jobstore.AgentSession, cancel bool) (jobstore.AgentSession, error) {
	if cancel && s.takeoverer != nil && a.HandedOffJobID != "" {
		if err := s.takeoverer.CancelTakeover(ctx, a.HandedOffJobID); err != nil {
			return jobstore.AgentSession{}, fmt.Errorf("cancel takeover job %s: %w", a.HandedOffJobID, err)
		}
	}
	released, err := s.store.ReleaseSessionHandedOff(a.SessionID)
	if err != nil {
		return jobstore.AgentSession{}, err
	}
	if !released {
		return jobstore.AgentSession{}, ErrUnknownSession
	}
	after, ok, err := s.store.GetAgentSession(a.SessionID)
	if err != nil {
		return jobstore.AgentSession{}, err
	}
	if !ok {
		return jobstore.AgentSession{}, ErrUnknownSession
	}
	return after, nil
}

// jobStatus reads the terminal status of the takeover job a release is reporting
// on ("job_done" / "job_failed" / …). The row is the authority, and it is already
// durable by the time the terminal hook runs; an unreadable one is reported as
// "unknown" rather than as an empty reason nobody can act on.
func (s *Service) jobStatus(jobID string) string {
	rec, ok, err := s.store.GetJob(jobID)
	if err != nil || !ok || strings.TrimSpace(rec.Status) == "" {
		return "unknown"
	}
	return rec.Status
}

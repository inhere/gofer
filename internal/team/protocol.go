package team

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/msgprefix"
)

// The leader protocol (plan 2026-11-26-team-plan.md §2.4): what the session sitting in a team's
// leader seat is told on every turn, the per-turn evaluation it must record, and what the server
// does when a turn ends without one or a chain of member results ends in silence.
//
// A TURN of a leader is, concretely:
//
//   - ACP (a resident job on the leader seat): from the moment gofer hands the job a say — the
//     first prompt, a letter, a person's message — to the job going back to awaiting input. The
//     host job service calls BeginTurn when it builds the text of that say (TurnNote, local and
//     remote worker alike) and EndTurn on the idle transition both paths share.
//   - Terminal (a session on the leader seat): from one UserPromptSubmit heartbeat to the next
//     Stop heartbeat of that session.
//
// The open turns live in memory. A server restart forgets a turn in progress; its end is then not
// seen, so no missing_eval is written for it (the safe direction: no false alarm), and an eval
// recorded afterwards simply opens an implicit turn. Letters a Stop hook hands the person-away
// session do not open a new turn (the Stop-block continuation is the same turn to the server).
//
// Identity is the server's: the leader is whichever session / job Router.IsLeader says sits in
// the leader seat right now (rule 8); nothing a letter claims counts.

var (
	// ErrNotLeader: the caller is not the session / job sitting in the leader seat of a team.
	ErrNotLeader = errors.New("team: only the session or job sitting in a team's leader seat may do this")
	// ErrIdleTurn: the leader already recorded idle in this turn, so it may not send a team
	// letter (or evaluate again) before the turn ends.
	ErrIdleTurn = errors.New("team: this turn already recorded idle, so no team letter may be sent in it; a new turn starts when the next message or letter wakes you")
)

// MissingEvalLimit is how many consecutive turns without an evaluation raise the 「等我」 card.
const MissingEvalLimit = 3

// chainWindow is how far back the ledger is read for the letters that woke a leader's first
// tracked turn (later turns read from the start of the previous one).
const chainWindow = 10 * time.Minute

const (
	wokenLetters = 3
	wokenRunes   = 300
	briefRunes   = 1500
)

// ChainEnd is the fact "a leader turn woken by a member result ended with nothing sent, nothing
// dispatched and no wakeup left": the result that started it must reach a person.
type ChainEnd struct {
	Team   jobstore.Team
	Leader jobstore.Seat
	// Member is the name of the member whose result it was ("" = unknown).
	Member string
	// Trigger is the result letter that woke the leader (the newest one when several did).
	Trigger jobstore.Mail
	TurnRef string
}

// leaderTurn is one open turn of a leader.
type leaderTurn struct {
	team    jobstore.Team
	ref     string
	turnRef string
	started time.Time
	// human: a person typed this turn (a terminal prompt), so somebody is already looking.
	human bool
	// woken: the turn was started by a member result and no person's letter.
	woken   bool
	trigger jobstore.Mail
	member  string
	eval    string
	sent    int
}

// Protocol is the leader protocol engine. The zero value is unusable; build it with NewProtocol.
type Protocol struct {
	store  *jobstore.Store
	svc    *Service
	router *Router
	nowFn  func() time.Time
	tagFn  func() string

	mu   sync.Mutex
	open map[string]*leaderTurn
	last map[string]time.Time

	// onChainEnd receives a chain end; nil drops it.
	onChainEnd func(ChainEnd) error
}

// NewProtocol builds the engine over the store, the team service (for the roster) and the router
// (for leader identity).
func NewProtocol(store *jobstore.Store, svc *Service, router *Router) *Protocol {
	return &Protocol{
		store: store, svc: svc, router: router, nowFn: time.Now,
		open: map[string]*leaderTurn{}, last: map[string]time.Time{},
	}
}

// SetClock replaces the clock (tests).
func (p *Protocol) SetClock(f func() time.Time) { p.nowFn = f }

// SetBoundary replaces the random boundary tag generator (tests).
func (p *Protocol) SetBoundary(f func() string) { p.tagFn = f }

// OnChainEnd installs the sink that puts a chain end in front of a person (the mailbox writes
// the creator a letter).
func (p *Protocol) OnChainEnd(f func(ChainEnd) error) { p.onChainEnd = f }

func (p *Protocol) tag() string {
	if p.tagFn != nil {
		return p.tagFn()
	}
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "team-turn-" + strconv.FormatInt(p.nowFn().UnixNano(), 36)
	}
	return "team-turn-" + hex.EncodeToString(b[:])
}

func (p *Protocol) leaderOf(ref string) (jobstore.Team, bool) {
	if strings.TrimSpace(ref) == "" {
		return jobstore.Team{}, false
	}
	t, ok, err := p.router.IsLeader(Party{Kind: jobstore.MailKindJob, ID: ref})
	if err != nil {
		slog.Warn("team: leader lookup failed", "ref", ref, "err", err)
		return jobstore.Team{}, false
	}
	return t, ok
}

// IsLeader reports whether ref (a job or session id) sits in a leader seat right now.
func (p *Protocol) IsLeader(o Party) (jobstore.Team, bool, error) { return p.router.IsLeader(o) }

// TurnNote starts a turn of the leader known by the first of ids that sits in a leader seat and
// returns the text to put in front of it: "" when none of them does (no team ⇒ no change).
// ids are the names a resident session is known by (its job id, the agent's own session id); the
// first one is the turn's key.
func (p *Protocol) TurnNote(lang i18n.Lang, ids ...string) string {
	for _, id := range ids {
		if _, ok := p.leaderOf(id); ok {
			return p.BeginTurn(lang, id, false)
		}
	}
	return ""
}

// PeekNote is TurnNote without opening a turn: the block of a say that is sent again.
func (p *Protocol) PeekNote(lang i18n.Lang, ids ...string) string {
	for _, id := range ids {
		if t, ok := p.leaderOf(id); ok {
			return p.render(lang, t, nil)
		}
	}
	return ""
}

// TurnEnded ends the open turn of the first of ids that has one.
func (p *Protocol) TurnEnded(ids ...string) {
	for _, id := range ids {
		if id != "" && p.EndTurn(id) {
			return
		}
	}
}

// BeginTurn opens a turn for ref when it sits in a leader seat and returns the injection text;
// "" when it does not. human says a person typed the prompt that starts the turn.
func (p *Protocol) BeginTurn(lang i18n.Lang, ref string, human bool) string {
	t, ok := p.leaderOf(ref)
	if !ok {
		return ""
	}
	now := p.nowFn()
	p.mu.Lock()
	prev := p.open[ref]
	delete(p.open, ref)
	since := p.last[ref]
	p.mu.Unlock()
	if prev != nil {
		// Its end was never reported (a hook that missed the Stop, a worker that reconnected):
		// close it before the next one so its evaluation still counts.
		p.finish(prev)
	}
	if since.IsZero() {
		since = now.Add(-chainWindow)
	}
	turn := &leaderTurn{team: t, ref: ref, started: now, human: human, turnRef: fmt.Sprintf("%s#%d", ref, now.UnixMilli())}
	woken := p.wokenBy(t, since, turn)
	p.mu.Lock()
	p.open[ref] = turn
	p.last[ref] = now
	p.mu.Unlock()
	return p.render(lang, t, woken)
}

// EndTurn closes the open turn of ref (false: it had none).
func (p *Protocol) EndTurn(ref string) bool {
	p.mu.Lock()
	turn := p.open[ref]
	delete(p.open, ref)
	p.mu.Unlock()
	if turn == nil {
		return false
	}
	p.finish(turn)
	return true
}

// finish applies what a turn's end means: a turn without an evaluation is recorded as
// missing_eval, and a turn a member result woke that sent nothing, dispatched nothing and left no
// wakeup is the end of the chain.
func (p *Protocol) finish(turn *leaderTurn) {
	p.mu.Lock()
	eval, sent := turn.eval, turn.sent
	p.mu.Unlock()
	if eval == "" {
		if _, err := p.store.AppendTeamEval(jobstore.TeamEval{
			TeamID: turn.team.ID, LeaderRef: turn.ref, TurnRef: turn.turnRef, Outcome: jobstore.TeamEvalMissing,
			Reason: "the turn ended without gofer team eval",
		}); err != nil {
			slog.Warn("team: recording missing_eval failed", "team", turn.team.ID, "err", err)
		}
	}
	if !turn.woken || turn.human || sent > 0 || eval == jobstore.TeamEvalDispatched || p.wakeupLeft(turn.ref) {
		return
	}
	if p.onChainEnd == nil {
		return
	}
	lead, ok, err := p.store.GetSeat(turn.team.LeaderSeatID)
	if err != nil || !ok {
		lead = jobstore.Seat{ID: turn.team.LeaderSeatID}
	}
	if err := p.onChainEnd(ChainEnd{Team: turn.team, Leader: lead, Member: turn.member, Trigger: turn.trigger, TurnRef: turn.turnRef}); err != nil {
		slog.Warn("team: chain end notice failed", "team", turn.team.ID, "err", err)
	}
}

// wakeupLeft reports whether the leader still has something that will wake it: an enabled wakeup
// (a resident job's own timer, or the receipt of a child it submitted), a watched job or an
// active nudge of a terminal session.
func (p *Protocol) wakeupLeft(ref string) bool {
	if ws, err := p.store.ListWakeups(ref); err == nil {
		for _, w := range ws {
			if w.Enabled != 0 {
				return true
			}
		}
	}
	if ws, err := p.store.ListSessionJobWatches(ref); err == nil && len(ws) > 0 {
		return true
	}
	if ns, err := p.store.ListSessionNudges(ref, false); err == nil && len(ns) > 0 {
		return true
	}
	return false
}

// wokenLetter is one letter that woke the leader this turn.
type wokenLetter struct {
	from, text string
}

// wokenBy reads the route ledger for the letters that reached the leader seat since `since` and
// fills the turn's chain facts. The rule of the row says what kind of letter it was: a turn is
// woken by a member result when member results arrived and no person's letter did.
func (p *Protocol) wokenBy(t jobstore.Team, since time.Time, turn *leaderTurn) []wokenLetter {
	rows, err := p.store.ListTeamRoutesSince(t.ID, since.Unix(), 200)
	if err != nil {
		return nil
	}
	var out []wokenLetter
	people := false
	for _, r := range rows { // newest first
		if r.MailID == 0 || len(r.ToSeatIDs) != 1 || r.ToSeatIDs[0] != t.LeaderSeatID || !reachedMailbox(r.Outcome) {
			continue
		}
		member := false
		switch r.Rule {
		case RuleMemberResult, RuleMemberFailed:
			member = true
		case RuleRelay:
			member = r.Outcome == jobstore.TeamOutcomeHandedToLeader
		case RuleHumanDefault, RuleHumanNamed, RulePeerLeader, RuleDirect:
			people = true
		default:
			continue
		}
		m, ok, err := p.store.GetMail(r.MailID)
		if err != nil || !ok {
			continue
		}
		from := p.fromLabel(r.FromRef)
		if member && turn.trigger.ID == 0 {
			turn.trigger, turn.member = m, from
		}
		if len(out) < wokenLetters {
			out = append(out, wokenLetter{from: from, text: cut(strings.Join(strings.Fields(m.Body), " "), wokenRunes)})
		}
	}
	turn.woken = turn.trigger.ID != 0 && !people
	return out
}

func reachedMailbox(outcome string) bool {
	switch outcome {
	case jobstore.TeamOutcomeDelivered, jobstore.TeamOutcomeQueued, jobstore.TeamOutcomeMerged, jobstore.TeamOutcomeHandedToLeader:
		return true
	}
	return false
}

func (p *Protocol) fromLabel(ref string) string {
	if id := strings.TrimPrefix(ref, "seat:"); id != ref {
		if x, ok, err := p.store.GetSeat(id); err == nil && ok {
			return x.Name
		}
	}
	return ref
}

func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// RecordEval records the leader's evaluation of its turn. Only the session / job sitting in a
// leader seat may (rule 8); once a turn recorded idle it is final.
func (p *Protocol) RecordEval(origin Party, outcome, reason string) (jobstore.TeamEval, error) {
	switch outcome {
	case jobstore.TeamEvalDispatched, jobstore.TeamEvalIdle, jobstore.TeamEvalFailed:
	default:
		return jobstore.TeamEval{}, fmt.Errorf("%w: outcome must be dispatched, idle or failed", jobstore.ErrTeamInvalid)
	}
	t, ok, err := p.router.IsLeader(origin)
	if err != nil {
		return jobstore.TeamEval{}, err
	}
	if !ok {
		return jobstore.TeamEval{}, ErrNotLeader
	}
	p.mu.Lock()
	turn := p.open[origin.ID]
	if turn == nil {
		// No injection opened a turn (a restart, an agent whose hook output never reached the
		// model): the evaluation opens one so the idle binding still holds.
		now := p.nowFn()
		turn = &leaderTurn{team: t, ref: origin.ID, started: now, turnRef: fmt.Sprintf("%s#%d", origin.ID, now.UnixMilli())}
		p.open[origin.ID] = turn
	}
	if turn.eval == jobstore.TeamEvalIdle {
		p.mu.Unlock()
		return jobstore.TeamEval{}, ErrIdleTurn
	}
	turn.eval = outcome
	turnRef := turn.turnRef
	p.mu.Unlock()
	return p.store.AppendTeamEval(jobstore.TeamEval{
		TeamID: t.ID, LeaderRef: origin.ID, TurnRef: turnRef, Outcome: outcome, Reason: strings.TrimSpace(reason),
	})
}

// CheckSend refuses a team letter from the leader in a turn that already recorded idle.
func (p *Protocol) CheckSend(origin Party) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if turn := p.open[origin.ID]; turn != nil && turn.eval == jobstore.TeamEvalIdle {
		return ErrIdleTurn
	}
	return nil
}

// NoteSent records that the leader sent a team letter in its open turn.
func (p *Protocol) NoteSent(origin Party) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if turn := p.open[origin.ID]; turn != nil {
		turn.sent++
	}
}

// render writes the injection: one stable block (protocol, roster, brief) and, behind it, the
// per-turn part. Letter bodies are untrusted, so they only ever sit inside a tag drawn per turn
// that no body can know in advance.
func (p *Protocol) render(lang i18n.Lang, t jobstore.Team, woken []wokenLetter) string {
	var b strings.Builder
	line := func(key string, args ...any) {
		b.WriteString(i18n.T(lang, "team.protocol."+key, args...))
		b.WriteByte('\n')
	}
	line("head", "team", t.Name, "project", t.ProjectKey)
	for _, k := range []string{"dispatch", "eval", "quiet", "human", "untrusted"} {
		line(k)
	}
	line("roster")
	b.WriteString(p.rosterLines(lang, t))
	if brief := strings.TrimSpace(t.Brief); brief != "" {
		line("brief")
		b.WriteString(cut(brief, briefRunes))
		b.WriteByte('\n')
	}
	if len(woken) > 0 {
		tag := p.tag()
		line("woken", "tag", tag)
		b.WriteString("<" + tag + ">\n")
		for _, w := range woken {
			b.WriteString(i18n.T(lang, "team.protocol.woken_item", "from", w.from, "text", w.text))
			b.WriteByte('\n')
		}
		b.WriteString("</" + tag + ">\n")
	}
	return msgprefix.FormatIn(lang, msgprefix.Team, "", strings.TrimRight(b.String(), "\n")) + "\n\n"
}

func (p *Protocol) rosterLines(lang i18n.Lang, t jobstore.Team) string {
	if p.svc == nil {
		return ""
	}
	r, err := p.svc.Roster("", t.ID)
	if err != nil {
		return ""
	}
	var b strings.Builder
	now := p.nowFn()
	for _, e := range r.Entries {
		purpose := strings.Join(strings.Fields(e.Status.Seat.Purpose), " ")
		if purpose == "" {
			purpose = i18n.T(lang, "team.protocol.nopurpose")
		}
		step := "-"
		if e.Stage != nil && e.Stage.InFlow() {
			step = fmt.Sprintf("%s (%d/%d)", e.Stage.Step, e.Stage.StepNo, e.Stage.StepTotal)
		}
		b.WriteString(i18n.T(lang, "team.protocol.member", "name", e.Status.Seat.Name, "role", i18n.T(lang, "team.protocol.role_"+e.Role),
			"purpose", cut(purpose, 120), "state", e.Status.State, "step", step, "active", agoText(lang, now, e.Status.ActivityAt)))
		b.WriteByte('\n')
	}
	return b.String()
}

func agoText(lang i18n.Lang, now time.Time, at int64) string {
	if at <= 0 {
		return i18n.T(lang, "team.protocol.ago.never")
	}
	d := now.Sub(time.Unix(at, 0))
	switch {
	case d < time.Minute:
		return i18n.T(lang, "team.protocol.ago.now")
	case d < time.Hour:
		return i18n.T(lang, "team.protocol.ago.min", "n", int(d/time.Minute))
	case d < 48*time.Hour:
		return i18n.T(lang, "team.protocol.ago.hour", "n", int(d/time.Hour))
	}
	return i18n.T(lang, "team.protocol.ago.day", "n", int(d/(24*time.Hour)))
}

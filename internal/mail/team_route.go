package mail

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/team"
)

// The mailbox side of team routing (plan 2026-11-26-team-plan.md §2.3). The deciding half is
// the team engine behind the TeamRouter seam; this file stores the letters it decided, writing
// the route ledger around the delivery: a pending row first, then the letter, then the row is
// settled with the letter's id and outcome. A letter nobody routes (no team involved) never
// reaches this code's writes — Send behaves exactly as before.

// TeamRouter is the routing engine seam (*team.Router satisfies it). Nil switches team routing
// off.
type TeamRouter interface {
	Decide(team.Input) (team.Decision, error)
	MemberFailure(jobID string) (team.Failure, bool, error)
}

// LeaderTurns is the leader protocol seam (*team.Protocol satisfies it): a team letter of the
// leader is refused in a turn that already recorded idle, and a letter it did send is noted so the
// end of the turn can tell a quiet turn from a busy one. Nil switches the binding off.
type LeaderTurns interface {
	CheckSend(team.Party) error
	NoteSent(team.Party)
}

// SetLeaderTurns installs the leader protocol's turn state (nil = none).
func (s *Service) SetLeaderTurns(t LeaderTurns) { s.turns = t }

// SetTeamRouter replaces the routing engine (tests; nil = no team routing).
func (s *Service) SetTeamRouter(r TeamRouter) { s.router = r }

// ErrRouteInFlight: a letter with the same route key is being stored right now (its pending
// row is younger than routeInFlightFor). The sender retries later instead of storing twice.
var ErrRouteInFlight = errors.New("mail: a letter with this route key is still being stored")

// routeInFlightFor is how long a pending row without a letter is believed to belong to a live
// send rather than a dead one.
const routeInFlightFor = time.Minute

// staleRouteAfter is when the deliverer sweep gives a pending row without a letter up as
// unknown.
const staleRouteAfter = 5 * time.Minute

// teamSend is one letter on its way through the router.
type teamSend struct {
	org          Origin
	addr         Address
	res          resolution
	body         string
	urgent       bool
	threadID     string
	replyTo      int64
	sourceMailID int64
	replyHop     int
	idem         string
	captureItem  string
	visible      func(jobstore.AgentSession) bool
	authorize    func(Target) error
}

// jobIdemKey is the automatic idempotency key of a letter a job sends: the job, the recipient
// address and the body. Running the same command again after a timeout therefore cannot store
// the letter twice.
func jobIdemKey(org Origin, to, body string) string {
	if org.Kind != jobstore.MailKindJob {
		return ""
	}
	h := sha256.Sum256([]byte(org.ID + "\x00" + to + "\x00" + body))
	return "job:" + org.ID + ":" + hex.EncodeToString(h[:8])
}

type teamPlan struct {
	res    resolution
	seatID string
}

// routeTeam runs the router over one letter. handled=false means no team rule applies and the
// caller stores the letter the ordinary way.
func (s *Service) routeTeam(ts teamSend) (out SendResult, handled bool, err error) {
	if s.router == nil {
		return SendResult{}, false, nil
	}
	if ts.idem == "" {
		ts.idem = jobIdemKey(ts.org, ts.addr.Raw, ts.body)
	}
	d, err := s.router.Decide(team.Input{
		Origin: team.Party{Kind: ts.org.Kind, ID: ts.org.ID, Caller: ts.org.Caller},
		ToRaw:  ts.addr.Raw, ViaTeamAddr: ts.addr.Scheme == SchemeTeam, TeamID: ts.res.target.TeamID,
		TargetKind: ts.res.target.Kind, TargetID: ts.res.target.ID, TargetSeatID: ts.res.target.SeatID,
		Body: ts.body, ReplyHop: ts.replyHop, SourceMailID: ts.sourceMailID, IdemKey: ts.idem, Now: s.now(),
	})
	if err != nil {
		return SendResult{}, true, err
	}
	if !d.Applies {
		return SendResult{}, false, nil
	}
	leader := team.Party{Kind: ts.org.Kind, ID: ts.org.ID, Caller: ts.org.Caller}
	deliverable := d.SenderLeader && !d.Drop && s.turns != nil
	if deliverable {
		// Eval bound to the turn: after idle the leader sends nothing more in the same turn.
		if err := s.turns.CheckSend(leader); err != nil {
			return SendResult{}, true, err
		}
	}
	plans, err := s.teamPlans(ts, d)
	if err != nil {
		return SendResult{}, true, err
	}
	body := teamBody(ts.body, d)
	var first *SendResult
	var extra []jobstore.Mail
	for _, p := range plans {
		one, err := s.storeTeamLetter(ts, d, p, body)
		if err != nil {
			return one, true, err
		}
		if first == nil {
			first = &one
			continue
		}
		extra = append(extra, one.Mail)
	}
	first.Extra = extra
	if deliverable {
		s.turns.NoteSent(leader)
	}
	return *first, true, nil
}

// teamPlans resolves the recipients a decision names and authorizes the ones the address did not
// name itself.
func (s *Service) teamPlans(ts teamSend, d team.Decision) ([]teamPlan, error) {
	if len(d.Recipients) == 0 {
		res := ts.res
		if d.Hold != "" {
			res.target.Deliver, res.reason = false, d.Hold
		}
		return []teamPlan{{res: res, seatID: res.target.SeatID}}, nil
	}
	plans := make([]teamPlan, 0, len(d.Recipients))
	for _, x := range d.Recipients {
		if x.ID == ts.res.target.SeatID && (ts.res.target.TeamID == "" || ts.res.target.TeamID == d.Team.ID) {
			// The address already named this seat: keep its own resolution (and its spelling).
			res := ts.res
			if d.Hold != "" {
				res.target.Deliver, res.reason = false, d.Hold
			}
			plans = append(plans, teamPlan{res: res, seatID: x.ID})
			continue
		}
		rin := resolveInput{
			addr:   Address{Raw: "seat:" + x.Name + "@" + x.ProjectKey, Scheme: SchemeSeat, Value: x.Name, Project: x.ProjectKey},
			origin: ts.org, visible: ts.visible,
		}
		res := s.seatResolution(rin, x, d.Team.ID)
		if d.Hold != "" {
			res.target.Deliver, res.reason = false, d.Hold
		}
		if ts.authorize != nil && res.target.Kind != "" {
			if err := ts.authorize(res.target); err != nil {
				return nil, err
			}
		}
		plans = append(plans, teamPlan{res: res, seatID: x.ID})
	}
	return plans, nil
}

// teamBody prefixes the source tag (rule 7) and, for a redirect, why the letter came to the
// leader. A body that already starts with a tag (a readdressed letter) is left alone.
func teamBody(body string, d team.Decision) string {
	if d.Tag == "" || strings.HasPrefix(body, "[team ") {
		return body
	}
	head := d.Tag
	if d.Note != "" {
		head += "\n[handed to the leader: " + d.Note + "]"
	}
	return head + "\n" + body
}

func (s *Service) outcomeOf(d team.Decision, m jobstore.Mail, held bool) string {
	switch {
	case d.Drop:
		return jobstore.TeamOutcomeDroppedSelf
	case held:
		return jobstore.TeamOutcomeHeld
	case d.Handed:
		return jobstore.TeamOutcomeHandedToLeader
	}
	return jobstore.TeamOutcomeQueued
}

// storeTeamLetter writes the ledger rows and the letter of one recipient.
func (s *Service) storeTeamLetter(ts teamSend, d team.Decision, p teamPlan, body string) (SendResult, error) {
	key := d.KeyFor(p.seatID)
	var reuse int64
	if key != "" {
		ex, ok, err := s.store.TeamRouteByKey(d.Team.ID, key)
		if err != nil {
			return SendResult{}, err
		}
		if ok {
			m, dup, err := s.reconcileRoute(d, ex, key)
			if err != nil {
				return SendResult{}, err
			}
			if dup {
				return SendResult{Mail: m, Target: p.res.target, Held: m.State == jobstore.MailHeld, Deduped: true}, nil
			}
			reuse = ex.ID
		}
	}
	rows, err := s.openRoutes(d, p, key, reuse)
	if err != nil {
		return SendResult{}, err
	}
	mi := jobstore.MailInput{
		ThreadID: ts.threadID, ReplyTo: ts.replyTo,
		FromKind: ts.org.Kind, FromID: ts.org.ID, FromCaller: ts.org.Caller,
		ToAddress: p.res.target.Address, ToKind: p.res.target.Kind, ToID: p.res.target.ID, ToCaller: p.res.target.Caller,
		JobID: mailJobID(p.res.target.Kind, p.res.target.ID, ts.org), Urgent: ts.urgent, Body: body,
		DeliverBy: s.deliverBy(), CaptureItemID: ts.captureItem, TeamID: d.Team.ID, Hop: d.Hop, RouteKey: key,
	}
	var out SendResult
	if d.Drop {
		m, err := s.store.InsertMail(mi)
		if err != nil {
			s.settleRoutes(rows, 0, jobstore.TeamOutcomeUnknown)
			return SendResult{}, err
		}
		if c, _, cerr := s.store.CancelMail(m.ID, "team: the leader's own letter to the team is only recorded"); cerr == nil {
			m = c
		}
		out = SendResult{Mail: m, Target: p.res.target}
	} else {
		out, err = s.storeLetter(mi, p.res, ts.org)
		if out.Mail.ID == 0 {
			// The insert failed: whether the row reached the table is not known, so the
			// ledger keeps that fact instead of guessing; a retry reconciles by key.
			s.settleRoutes(rows, 0, jobstore.TeamOutcomeUnknown)
			return out, err
		}
	}
	s.settleRoutes(rows, out.Mail.ID, s.outcomeOf(d, out.Mail, out.Held))
	return out, err
}

// openRoutes writes one pending row per ledger (the primary one keyed). A row left by an
// earlier attempt (reuse) is taken over instead of inserted.
func (s *Service) openRoutes(d team.Decision, p teamPlan, key string, reuse int64) ([]int64, error) {
	ids := make([]int64, 0, len(d.Ledgers))
	to := []string{}
	if p.seatID != "" && d.Rule != team.RuleOutbound {
		to = []string{p.seatID}
	}
	for i, t := range d.Ledgers {
		if i == 0 && reuse != 0 {
			if _, err := s.store.SettleTeamRoute(reuse, jobstore.TeamOutcomePending); err != nil {
				return nil, err
			}
			ids = append(ids, reuse)
			continue
		}
		k := ""
		if i == 0 {
			k = key
		}
		r, err := s.store.AppendTeamRoute(jobstore.TeamRoute{
			TeamID: t.ID, FromRef: d.FromRef, ToSeatIDs: to, Rule: d.Rule, Hop: d.Hop,
			RouteKey: k, Outcome: jobstore.TeamOutcomePending,
		})
		if err != nil {
			return nil, err
		}
		ids = append(ids, r.ID)
	}
	return ids, nil
}

func (s *Service) settleRoutes(ids []int64, mailID int64, outcome string) {
	for _, id := range ids {
		if _, err := s.store.SettleTeamRouteMail(id, mailID, outcome); err != nil {
			// The letter is stored; the row stays pending and the key lookup still finds the
			// letter on a retry.
			logRouteWarn(id, err)
		}
	}
}

// reconcileRoute handles a send whose route key already has a ledger row. dup=true: the letter
// exists (m) and nothing is stored again; a dup row records the repeat. dup=false: the earlier
// attempt left no letter, so this send takes the row over — and only when that is safe: a
// pending row younger than routeInFlightFor may still be a live send.
func (s *Service) reconcileRoute(d team.Decision, ex jobstore.TeamRoute, key string) (jobstore.Mail, bool, error) {
	m, found, err := s.store.MailByRouteKey(key)
	if err != nil {
		return jobstore.Mail{}, false, err
	}
	if !found && ex.MailID > 0 {
		m, found, err = s.store.GetMail(ex.MailID)
		if err != nil {
			return jobstore.Mail{}, false, err
		}
	}
	unsettled := ex.Outcome == jobstore.TeamOutcomePending || ex.Outcome == jobstore.TeamOutcomeUnknown
	if !found {
		if !unsettled {
			// A settled row whose letter is gone (pruned): still the same send.
			m = jobstore.Mail{ID: ex.MailID}
			found = true
		} else if ex.Outcome == jobstore.TeamOutcomePending && s.now().Unix()-ex.DecidedAt < int64(routeInFlightFor/time.Second) {
			return jobstore.Mail{}, false, fmt.Errorf("%w (%s)", ErrRouteInFlight, key)
		} else {
			return jobstore.Mail{}, false, nil
		}
	}
	if unsettled {
		out := jobstore.TeamOutcomeQueued
		switch m.State {
		case jobstore.MailDelivered, jobstore.MailAnswered:
			out = jobstore.TeamOutcomeDelivered
		case jobstore.MailHeld:
			out = jobstore.TeamOutcomeHeld
		}
		if _, err := s.store.SettleTeamRouteMail(ex.ID, m.ID, out); err != nil {
			return jobstore.Mail{}, false, err
		}
	}
	if _, err := s.store.AppendTeamRoute(jobstore.TeamRoute{
		TeamID: d.Team.ID, MailID: m.ID, FromRef: d.FromRef, ToSeatIDs: ex.ToSeatIDs, Rule: d.Rule, Hop: d.Hop,
		Outcome: jobstore.TeamOutcomeDup,
	}); err != nil {
		return jobstore.Mail{}, false, err
	}
	return m, true, nil
}

func logRouteWarn(id int64, err error) {
	slog.Warn("mail: settling a team route failed", "route", id, "error", err)
}

// mergeGroup is the letters one say to an ACP job carries (plan §2.3 rule 6): the queue head and,
// while they follow it inside the head's team's merge window, the letters behind it. Merging
// stops at a human's letter, an urgent letter, another team's letter or a letter with no team;
// a head that is itself one of those goes alone, and so does every letter when the team's
// window is 0. Terminals already take one injection per turn and need nothing.
func (d *Deliverer) mergeGroup(letters []jobstore.Mail) []jobstore.Mail {
	if len(letters) == 0 {
		return nil
	}
	head := letters[0]
	alone := letters[:1]
	if len(letters) == 1 || head.TeamID == "" || !d.svc.mergeable(head) {
		return alone
	}
	t, ok, err := d.svc.store.GetTeam(head.TeamID)
	if err != nil || !ok || t.MergeWindowSec <= 0 {
		return alone
	}
	group := []jobstore.Mail{head}
	for _, m := range letters[1:] {
		if m.TeamID != head.TeamID || !d.svc.mergeable(m) || m.CreatedAt-head.CreatedAt > int64(t.MergeWindowSec) {
			break
		}
		group = append(group, m)
	}
	return group
}

// mergeable reports whether a letter may be combined with its neighbours.
func (s *Service) mergeable(m jobstore.Mail) bool {
	if m.Urgent || m.FromKind == jobstore.MailKindCaller || m.SystemKind != "" {
		return false
	}
	rule, err := s.store.TeamRuleOfMail(m.ID)
	if err != nil {
		return false
	}
	switch rule {
	case team.RuleHumanNamed, team.RuleHumanDefault, team.RuleDirect:
		return false
	}
	return true
}

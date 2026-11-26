package mail

import (
	"fmt"
	"log/slog"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/team"
)

// NoteJobEnded is told about every job that reached a terminal state. A job that failed while
// sitting in a MEMBER seat of a team makes gofer write the leader a system letter (plan §2.3
// rule 9, rule member_failed). The letter goes to the leader — never back to the member whose
// job it was, so a failure cannot start a recovery loop between them. A leader nobody can reach
// (vacant seat, ended job) sends it to the team creator's box, which is the capture box and so
// the person's 「等我」. The letter is a system letter: terminal, never routed, not counted by
// the loop guard, never escalated again.
//
// It is idempotent per job: the ledger row carries a route key, so a repeated notification
// (a restarted hook) stores nothing.
func (s *Service) NoteJobEnded(jobID, status string) error {
	if s.router == nil || status != job.StatusFailed {
		return nil
	}
	f, ok, err := s.router.MemberFailure(jobID)
	if err != nil || !ok {
		return err
	}
	key := fmt.Sprintf("system:%s:%s:%s", team.RuleMemberFailed, jobID, f.Leader.ID)
	if _, exists, err := s.store.TeamRouteByKey(f.Team.ID, key); err != nil || exists {
		return err
	}
	row, err := s.store.AppendTeamRoute(jobstore.TeamRoute{
		TeamID: f.Team.ID, FromRef: "gofer", ToSeatIDs: []string{f.Leader.ID}, Rule: team.RuleMemberFailed,
		RouteKey: key, Outcome: jobstore.TeamOutcomePending,
	})
	if err != nil {
		return err
	}
	body := fmt.Sprintf("[team %s · from gofer · system]\nmember %s: its job %s failed (status %s). Decide the next step; this notice cannot be answered.",
		f.Team.Name, f.Member.Name, jobID, status)
	rin := resolveInput{
		addr:   Address{Raw: "seat:" + f.Leader.Name + "@" + f.Leader.ProjectKey, Scheme: SchemeSeat, Value: f.Leader.Name, Project: f.Leader.ProjectKey},
		origin: Origin{Kind: jobstore.MailKindGofer},
	}
	res := s.seatResolution(rin, f.Leader, f.Team.ID)
	if !res.target.Deliver {
		// Leader unreachable: the creator's inbox.
		res = resolution{
			target: Target{Kind: jobstore.MailKindCaller, Caller: f.Team.CallerID, Address: "caller:" + f.Team.CallerID, TeamID: f.Team.ID},
			reason: res.reason + "; the letter goes to the team creator",
		}
	}
	mi := jobstore.MailInput{
		FromKind: jobstore.MailKindGofer, ToAddress: res.target.Address, ToKind: res.target.Kind, ToID: res.target.ID,
		ToCaller: res.target.Caller, JobID: jobID, Body: body, DeliverBy: s.deliverBy(), TeamID: f.Team.ID,
		SystemKind: team.SystemMemberFailed,
	}
	out, err := s.storeLetter(mi, res, Origin{Kind: jobstore.MailKindGofer})
	if out.Mail.ID == 0 {
		s.settleRoutes([]int64{row.ID}, 0, jobstore.TeamOutcomeUnknown)
		return err
	}
	outcome := jobstore.TeamOutcomeQueued
	if out.Held {
		outcome = jobstore.TeamOutcomeHeld
	}
	s.settleRoutes([]int64{row.ID}, out.Mail.ID, outcome)
	if err != nil {
		slog.Warn("mail: member_failed letter stored with a follow-up error", "job", jobID, "error", err)
	}
	return nil
}

// NoteChainEnd puts the result that ended a team's chain in front of a person: a system letter
// to the team creator's box (the capture box, hence their 「等我」 list as a held letter). The
// leader protocol calls it when a leader turn woken by a member result ends with no letter sent,
// nothing dispatched and no wakeup left (plan §2.4). Why a letter and not a card of its own: the
// card of a leader that missed its evaluations is a STATE (derived, it clears itself when the
// leader recovers), while this is an EVENT — a result the person has not seen — and must stay
// until they read it, which is what a letter does. The letter is a system letter: terminal,
// never routed, not counted by the loop guard. It is idempotent per triggering letter.
func (s *Service) NoteChainEnd(ce team.ChainEnd) error {
	if ce.Trigger.ID == 0 {
		return nil
	}
	key := fmt.Sprintf("system:%s:%d:%s", team.RuleChainEnd, ce.Trigger.ID, ce.Leader.ID)
	if _, exists, err := s.store.TeamRouteByKey(ce.Team.ID, key); err != nil || exists {
		return err
	}
	row, err := s.store.AppendTeamRoute(jobstore.TeamRoute{
		TeamID: ce.Team.ID, MailID: ce.Trigger.ID, FromRef: "gofer", Rule: team.RuleChainEnd,
		RouteKey: key, Outcome: jobstore.TeamOutcomePending,
	})
	if err != nil {
		return err
	}
	lang := s.textLanguage()
	member := ce.Member
	if member == "" {
		member = "?"
	}
	body := fmt.Sprintf("[team %s · from gofer · system]\n%s\n%s", ce.Team.Name,
		i18n.T(lang, "team.chain_end.body", "team", ce.Team.Name, "member", member), ce.Trigger.Body)
	res := resolution{
		target: Target{Kind: jobstore.MailKindCaller, Caller: ce.Team.CallerID, Address: "caller:" + ce.Team.CallerID, TeamID: ce.Team.ID},
		reason: "the team's chain ended; the result goes to the team creator",
	}
	mi := jobstore.MailInput{
		FromKind: jobstore.MailKindGofer, ToAddress: res.target.Address, ToKind: res.target.Kind,
		ToCaller: res.target.Caller, Body: body, DeliverBy: s.deliverBy(), TeamID: ce.Team.ID,
		SystemKind: team.SystemChainEnd,
	}
	out, err := s.storeLetter(mi, res, Origin{Kind: jobstore.MailKindGofer})
	if out.Mail.ID == 0 {
		s.settleRoutes([]int64{row.ID}, 0, jobstore.TeamOutcomeUnknown)
		return err
	}
	outcome := jobstore.TeamOutcomeQueued
	if out.Held {
		outcome = jobstore.TeamOutcomeHeld
	}
	s.settleRoutes([]int64{row.ID}, out.Mail.ID, outcome)
	return err
}

// sweepStaleRoutes gives pending ledger rows nobody settled up as unknown (the deliverer's tick).
func (s *Service) sweepStaleRoutes() {
	if _, err := s.store.MarkStaleTeamRoutesUnknown(s.now().Add(-staleRouteAfter).Unix()); err != nil {
		slog.Warn("mail: marking stale team routes failed", "error", err)
	}
}

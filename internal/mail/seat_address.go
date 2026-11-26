package mail

import (
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
)

// The `seat:<name>[@<项目>]` and `team:<name>[@<项目>]` addresses (plan 2026-11-26-team-plan.md
// §2.2). A seat is a long-lived working position: the address follows whoever sits in it now,
// so it stays valid when the seat changes session. A team address is the leader seat of the
// team, and tags the letter with the team id.
//
// Both resolve to an ordinary session / job recipient, so the delivery path, the recipient
// authorization (permissions, project ceiling) and the offline queue are unchanged. Nothing to
// deliver to — a vacant seat, or a session / job that is gone — parks the letter in the capture
// box (held), exactly like any address that matched nobody.

// senderProject is the project an address without `@<项目>` means: the request's project, else
// the project of the sending session / job.
func (s *Service) senderProject(in resolveInput) string {
	if p := strings.TrimSpace(in.project); p != "" {
		return p
	}
	switch in.origin.Kind {
	case jobstore.MailKindSession:
		if a, ok, err := s.store.GetAgentSession(in.origin.ID); err == nil && ok {
			return strings.TrimSpace(a.ProjectKey)
		}
	case jobstore.MailKindJob:
		if rec, ok, err := s.store.GetJob(in.origin.ID); err == nil && ok {
			return strings.TrimSpace(rec.ProjectKey)
		}
	}
	return ""
}

// addressProject is the project a seat: / team: address names, or an error when there is none.
func (s *Service) addressProject(in resolveInput) (string, error) {
	if p := strings.TrimSpace(in.addr.Project); p != "" {
		return p, nil
	}
	if p := s.senderProject(in); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("%w: %s has no project: write %s:<名字>@<项目> or send from a session of the project", ErrInvalidAddress, in.addr.Raw, in.addr.Scheme)
}

func (s *Service) resolveSeatAddress(in resolveInput) (resolution, error) {
	project, err := s.addressProject(in)
	if err != nil {
		return resolution{}, err
	}
	x, ok, err := s.store.FindSeatByName(in.origin.Caller, project, in.addr.Value)
	if err != nil {
		return resolution{}, err
	}
	if !ok {
		return resolution{}, fmt.Errorf("%w: %s names no seat in project %q (see `gofer seat ls`)", ErrSeatNotFound, in.addr.Raw, project)
	}
	return s.seatResolution(in, x, ""), nil
}

func (s *Service) resolveTeamAddress(in resolveInput) (resolution, error) {
	project, err := s.addressProject(in)
	if err != nil {
		return resolution{}, err
	}
	t, ok, err := s.store.GetTeamByName(project, in.addr.Value)
	if err != nil {
		return resolution{}, err
	}
	// Another caller's team is as good as absent: the error must not confirm it exists.
	if ok && in.origin.Caller != "" && t.CallerID != "" && t.CallerID != in.origin.Caller {
		ok = false
	}
	if !ok {
		return resolution{}, fmt.Errorf("%w: %s names no team in project %q", ErrTeamNotFound, in.addr.Raw, project)
	}
	lead, found, err := s.store.GetSeat(t.LeaderSeatID)
	if err != nil {
		return resolution{}, err
	}
	if !found || lead.ArchivedAt != 0 {
		return resolution{
			target: Target{Address: in.addr.Raw, TeamID: t.ID},
			reason: fmt.Sprintf("%s: the leader seat of team %s is gone", in.addr.Raw, t.Name),
		}, nil
	}
	return s.seatResolution(in, lead, t.ID), nil
}

// seatResolution turns a seat into the recipient of the session / job sitting in it, or into a
// hold when nothing can take the letter.
func (s *Service) seatResolution(in resolveInput, x jobstore.Seat, teamID string) resolution {
	held := func(reason string) resolution {
		return resolution{target: Target{Address: in.addr.Raw, TeamID: teamID, SeatID: x.ID}, reason: reason}
	}
	if x.CurrentRef == "" {
		return held(fmt.Sprintf("%s: seat %s is vacant (nobody sits in it); the letter waits in the capture box", in.addr.Raw, x.Name))
	}
	if x.Kind == jobstore.SeatKindACP {
		rec, ok, err := s.store.GetJob(x.CurrentRef)
		if err != nil || !ok || job.IsTerminal(rec.Status) {
			return held(fmt.Sprintf("%s: the job sitting in seat %s has ended", in.addr.Raw, x.Name))
		}
		if s.sessionJob != nil {
			if session, found := s.sessionJob(rec.ID); found && !session {
				return held(fmt.Sprintf("%s: the job sitting in seat %s is a one-off run that cannot take a letter", in.addr.Raw, x.Name))
			}
		}
		return resolution{target: Target{
			Kind: jobstore.MailKindJob, ID: rec.ID, Address: in.addr.Raw, Deliver: true, TeamID: teamID, SeatID: x.ID,
		}}
	}
	a, ok, err := s.store.GetAgentSession(x.CurrentRef)
	if err != nil || !ok || !sessionrelay.IsDeliverableState(a.State) {
		return held(fmt.Sprintf("%s: the session sitting in seat %s is gone", in.addr.Raw, x.Name))
	}
	if in.visible != nil && !in.visible(a) {
		return held(fmt.Sprintf("%s: seat %s has no session you may reach", in.addr.Raw, x.Name))
	}
	return resolution{target: Target{
		Kind: jobstore.MailKindSession, ID: a.SessionID, Address: in.addr.Raw,
		Topic: a.Topic, Source: a.TopicSource, Offline: a.State == jobstore.SessionOffline,
		Deliver: true, TeamID: teamID, SeatID: x.ID,
	}}
}

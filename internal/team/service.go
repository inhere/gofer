// Package team is the business layer of the Team entity (design
// docs/design/2026-11-18-resident-sessions-and-team-design.md §7, plan
// docs/plans/2026-11-26-team-plan.md): a team is one leader seat plus member seats of one
// project. The rows live in jobstore (teams / team_members / team_routes / team_evals); this
// package adds validation, seat-name resolution, the roster (members joined with their seat
// status) and the route / eval record writers.
//
// The routing engine and the leader protocol are not here: they build on this package. It
// imports neither httpapi nor commands (G022).
package team

import (
	"errors"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/seat"
	"github.com/inhere/gofer/internal/stagenote"
)

// StageBriefer returns where the flow of the work item the session `ref` is on stands, or nil
// (it must read stored rows only: the roster is built per request).
type StageBriefer func(ref string) *stagenote.Brief

// Deps are the collaborators of a Service. Seats is required for the roster; StageBrief is
// optional (no flow information in the roster without it).
type Deps struct {
	Seats      *seat.Service
	StageBrief StageBriefer
}

// Service is the team service.
type Service struct {
	store *jobstore.Store
	d     Deps
}

// New builds a Service over the metadata store.
func New(store *jobstore.Store, d Deps) *Service { return &Service{store: store, d: d} }

// CreateInput is what a new team needs. Seat references are a seat id or a seat name of the
// caller in the project. A nil limit takes the default.
type CreateInput struct {
	CallerID   string
	ProjectKey string
	Name       string
	// Leader is the leader seat; Members the initial member seats.
	Leader  string
	Members []string
	Brief   string
	// RelayMode is leader (default) or direct.
	RelayMode       string
	RelayRoundLimit *int
	RelayDailyLimit *int
	MergeWindowSec  *int
}

func orDefault(p *int, d int) int {
	if p == nil {
		return d
	}
	return *p
}

// SeatRef resolves a seat reference (id, or name inside the project of the caller) to a seat.
func (s *Service) SeatRef(callerID, projectKey, ref string) (jobstore.Seat, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return jobstore.Seat{}, fmt.Errorf("%w: seat required", jobstore.ErrTeamInvalid)
	}
	if x, ok, err := s.store.GetSeat(ref); err != nil {
		return jobstore.Seat{}, err
	} else if ok {
		return x, nil
	}
	x, ok, err := s.store.FindSeatByName(callerID, projectKey, ref)
	if err != nil {
		return jobstore.Seat{}, err
	}
	if !ok {
		return jobstore.Seat{}, fmt.Errorf("%w: %q in project %q", jobstore.ErrSeatNotFound, ref, projectKey)
	}
	return x, nil
}

// Create adds a team with its leader and initial members.
func (s *Service) Create(in CreateInput) (jobstore.Team, error) {
	project := strings.TrimSpace(in.ProjectKey)
	lead, err := s.SeatRef(in.CallerID, project, in.Leader)
	if err != nil {
		return jobstore.Team{}, err
	}
	ids := make([]string, 0, len(in.Members))
	for _, ref := range in.Members {
		m, err := s.SeatRef(in.CallerID, project, ref)
		if err != nil {
			return jobstore.Team{}, err
		}
		ids = append(ids, m.ID)
	}
	return s.store.CreateTeam(jobstore.Team{
		ProjectKey: project, Name: in.Name, LeaderSeatID: lead.ID, Brief: in.Brief, RelayMode: in.RelayMode,
		RelayRoundLimit: orDefault(in.RelayRoundLimit, jobstore.DefaultTeamRelayRoundLimit),
		RelayDailyLimit: orDefault(in.RelayDailyLimit, jobstore.DefaultTeamRelayDailyLimit),
		MergeWindowSec:  orDefault(in.MergeWindowSec, jobstore.DefaultTeamMergeWindowSec),
	}, ids)
}

// Get reads a team by id.
func (s *Service) Get(id string) (jobstore.Team, error) {
	t, ok, err := s.store.GetTeam(id)
	if err != nil {
		return jobstore.Team{}, err
	}
	if !ok {
		return jobstore.Team{}, jobstore.ErrTeamNotFound
	}
	return t, nil
}

// ByName reads the active team of a project by name.
func (s *Service) ByName(projectKey, name string) (jobstore.Team, error) {
	t, ok, err := s.store.GetTeamByName(projectKey, name)
	if err != nil {
		return jobstore.Team{}, err
	}
	if !ok {
		return jobstore.Team{}, jobstore.ErrTeamNotFound
	}
	return t, nil
}

// Find reads a team by id, or — when ref is not an id — by name inside project.
func (s *Service) Find(projectKey, ref string) (jobstore.Team, error) {
	if t, err := s.Get(ref); err == nil {
		return t, nil
	} else if !errors.Is(err, jobstore.ErrTeamNotFound) {
		return jobstore.Team{}, err
	}
	return s.ByName(projectKey, ref)
}

// List lists teams.
func (s *Service) List(f jobstore.TeamFilter) ([]jobstore.Team, error) { return s.store.ListTeams(f) }

// UpdateInput is a partial update; nil / empty fields are left alone. Leader is a seat
// reference.
type UpdateInput struct {
	Name            *string
	Leader          *string
	Brief           *string
	RelayMode       *string
	RelayRoundLimit *int
	RelayDailyLimit *int
	MergeWindowSec  *int
}

// Update patches a team (name, leader, brief, relay limits, merge window).
func (s *Service) Update(id string, in UpdateInput) (jobstore.Team, error) {
	p := jobstore.TeamPatch{Name: in.Name, Brief: in.Brief, RelayMode: in.RelayMode,
		RelayRoundLimit: in.RelayRoundLimit, RelayDailyLimit: in.RelayDailyLimit, MergeWindowSec: in.MergeWindowSec}
	if in.Leader != nil {
		cur, err := s.Get(id)
		if err != nil {
			return jobstore.Team{}, err
		}
		lead, err := s.SeatRef(cur.CallerID, cur.ProjectKey, *in.Leader)
		if err != nil {
			return jobstore.Team{}, err
		}
		p.LeaderSeatID = &lead.ID
	}
	return s.store.UpdateTeam(id, p)
}

// SetBrief replaces the team description.
func (s *Service) SetBrief(id, brief string) (jobstore.Team, error) {
	return s.store.UpdateTeam(id, jobstore.TeamPatch{Brief: &brief})
}

// SetLeader makes the seat (id or name) the leader.
func (s *Service) SetLeader(id, seatRef string) (jobstore.Team, error) {
	return s.Update(id, UpdateInput{Leader: &seatRef})
}

// Archive archives a team (idempotent): it frees the name and its seats.
func (s *Service) Archive(id string) (jobstore.Team, error) { return s.store.ArchiveTeam(id) }

// AddMember adds a seat (id or name) as a member.
func (s *Service) AddMember(teamID, seatRef string) (jobstore.Seat, error) {
	t, err := s.Get(teamID)
	if err != nil {
		return jobstore.Seat{}, err
	}
	x, err := s.SeatRef(t.CallerID, t.ProjectKey, seatRef)
	if err != nil {
		return jobstore.Seat{}, err
	}
	return x, s.store.AddTeamMember(t.ID, x.ID)
}

// RemoveMember removes a member seat (id or name); the seat itself is untouched. It reports
// whether the seat was a member.
func (s *Service) RemoveMember(teamID, seatRef string) (bool, error) {
	t, err := s.Get(teamID)
	if err != nil {
		return false, err
	}
	x, err := s.SeatRef(t.CallerID, t.ProjectKey, seatRef)
	if err != nil {
		return false, err
	}
	return s.store.RemoveTeamMember(t.ID, x.ID)
}

// TeamOfSeat returns the active team a seat belongs to (ok=false: none).
func (s *Service) TeamOfSeat(seatID string) (jobstore.Team, bool, error) {
	return s.store.TeamOfSeat(seatID)
}

// Role of a roster row.
const (
	RoleLeader = "leader"
	RoleMember = jobstore.TeamRoleMember
)

// RosterEntry is one seat of a team with what is derived from the session sitting in it.
type RosterEntry struct {
	Role   string
	Status seat.Status
	// Stage is where the flow of the sitting session stands (nil: none, or not wired).
	Stage *stagenote.Brief
}

// Roster is a team with its leader and members, leader first.
type Roster struct {
	Team    jobstore.Team
	Entries []RosterEntry
}

// Roster reads the roster of a team for one viewer (the viewer decides "waiting" and
// "unread"). It costs a fixed number of queries plus one stage lookup per seated member.
func (s *Service) Roster(viewer, teamID string) (Roster, error) {
	t, err := s.Get(teamID)
	if err != nil {
		return Roster{}, err
	}
	if s.d.Seats == nil {
		return Roster{}, errors.New("team: roster needs the seat service")
	}
	members, err := s.store.ListTeamMembers(t.ID)
	if err != nil {
		return Roster{}, err
	}
	roles := make([]string, 0, len(members)+1)
	seats := make([]jobstore.Seat, 0, len(members)+1)
	lead, err := s.d.Seats.Get(t.LeaderSeatID)
	if err != nil {
		return Roster{}, fmt.Errorf("team: leader seat: %w", err)
	}
	roles, seats = append(roles, RoleLeader), append(seats, lead)
	for _, m := range members {
		x, err := s.d.Seats.Get(m.SeatID)
		if errors.Is(err, jobstore.ErrSeatNotFound) {
			continue // a deleted seat leaves the roster; the membership row is only history
		}
		if err != nil {
			return Roster{}, err
		}
		roles, seats = append(roles, m.Role), append(seats, x)
	}
	sts, err := s.d.Seats.Statuses(viewer, seats)
	if err != nil {
		return Roster{}, err
	}
	out := Roster{Team: t, Entries: make([]RosterEntry, 0, len(sts))}
	for i, st := range sts {
		e := RosterEntry{Role: roles[i], Status: st}
		if s.d.StageBrief != nil && st.Seat.CurrentRef != "" {
			e.Stage = s.d.StageBrief(st.Seat.CurrentRef)
		}
		out.Entries = append(out.Entries, e)
	}
	return out, nil
}

// RecordRoute appends one routing decision. It is the writer the routing engine calls.
func (s *Service) RecordRoute(r jobstore.TeamRoute) (jobstore.TeamRoute, error) {
	return s.store.AppendTeamRoute(r)
}

// Routes lists a team's routing decisions, newest first.
func (s *Service) Routes(teamID string, limit int) ([]jobstore.TeamRoute, error) {
	return s.store.ListTeamRoutes(teamID, limit)
}

// RecordEval appends one leader evaluation.
func (s *Service) RecordEval(e jobstore.TeamEval) (jobstore.TeamEval, error) {
	return s.store.AppendTeamEval(e)
}

// Evals lists a team's evaluations, newest first.
func (s *Service) Evals(teamID string, limit int) ([]jobstore.TeamEval, error) {
	return s.store.ListTeamEvals(teamID, limit)
}

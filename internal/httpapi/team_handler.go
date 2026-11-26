package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/team"
)

// The team routes (plan 2026-11-26-team-plan.md §2.1, §3 T1): /v1/teams. The business lives in
// internal/team; this file binds requests, applies the owner rule and maps errors to statuses.
//
// A team is owned by the caller of its leader seat, so it takes the seat roster's rule: the
// owner needs nothing, anyone else needs seat.manage.others (a team is made of seats, and the
// permission already means "manage other callers' seats and what they are grouped into"). Only
// a user-class caller reaches the routes: worker and job credentials are refused, like seats.

// teamHolder builds the team service once, on first use.
type teamHolder struct {
	once sync.Once
	svc  *team.Service
}

// teams returns the team service; nil when this server has no metadata store.
func (s *Server) teams() *team.Service {
	s.teamSvc.once.Do(func() {
		seats := s.seats()
		if seats == nil {
			return
		}
		s.teamSvc.svc = team.New(s.jobs.Meta(), team.Deps{Seats: seats, StageBrief: s.seatStageBriefOf})
	})
	return s.teamSvc.svc
}

// teamProjectsOf is the resource resolver of Resource("team"): the project of a team.
func teamProjectsOf(s *Server, id string) ([]string, bool, error) {
	svc := s.teams()
	if svc == nil || id == "" {
		return nil, false, nil
	}
	t, err := svc.Get(id)
	if errors.Is(err, jobstore.ErrTeamNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return []string{t.ProjectKey}, true, nil
}

func teamStatus(err error) int {
	switch {
	case errors.Is(err, jobstore.ErrTeamNotFound), errors.Is(err, jobstore.ErrSeatNotFound):
		return http.StatusNotFound
	case errors.Is(err, jobstore.ErrTeamNameTaken), errors.Is(err, jobstore.ErrTeamSeatTaken),
		errors.Is(err, jobstore.ErrTeamArchived), errors.Is(err, jobstore.ErrSeatArchived):
		return http.StatusConflict
	case errors.Is(err, jobstore.ErrTeamInvalid), errors.Is(err, jobstore.ErrSeatInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func (s *Server) teamsReady(c *rux.Context) (*team.Service, bool) {
	if callerKindFromCtx(c) != callerKindUser {
		writeError(c, http.StatusForbidden, "teams require a user caller", "worker and job credentials cannot use teams")
		return nil, false
	}
	svc := s.teams()
	if svc == nil {
		writeError(c, http.StatusServiceUnavailable, "teams unavailable", "no job store wired on this server")
		return nil, false
	}
	return svc, true
}

// loadTeam reads the team named by the path and applies the owner rule.
func (s *Server) loadTeam(c *rux.Context) (*team.Service, jobstore.Team, bool) {
	svc, ok := s.teamsReady(c)
	if !ok {
		return nil, jobstore.Team{}, false
	}
	t, err := svc.Get(c.Param("id"))
	if err != nil {
		writeError(c, teamStatus(err), "team lookup failed", err.Error())
		return nil, jobstore.Team{}, false
	}
	if !s.maySeat(c, t.CallerID) {
		s.writePermissionDenied(c, authz.PermSeatManageOthers)
		return nil, jobstore.Team{}, false
	}
	return svc, t, true
}

// teamView is the wire form of a team. Roster is only present on the single-team answers.
type teamView struct {
	ID              string `json:"id"`
	CallerID        string `json:"caller_id,omitempty"`
	ProjectKey      string `json:"project_key"`
	Name            string `json:"name"`
	LeaderSeatID    string `json:"leader_seat_id"`
	Brief           string `json:"brief,omitempty"`
	RelayMode       string `json:"relay_mode"`
	RelayRoundLimit int    `json:"relay_round_limit"`
	RelayDailyLimit int    `json:"relay_daily_limit"`
	MergeWindowSec  int    `json:"merge_window_sec"`
	ArchivedAt      int64  `json:"archived_at,omitempty"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
	// Roster lists the leader first, then the members, each with its seat status.
	Roster []teamRosterView `json:"roster,omitempty"`
}

// teamRosterView is one seat of a team: the seat row with its derived status plus its role.
type teamRosterView struct {
	Role string `json:"role"`
	seatView
}

func teamViewOf(t jobstore.Team) teamView {
	return teamView{
		ID: t.ID, CallerID: t.CallerID, ProjectKey: t.ProjectKey, Name: t.Name, LeaderSeatID: t.LeaderSeatID,
		Brief: t.Brief, RelayMode: t.RelayMode, RelayRoundLimit: t.RelayRoundLimit, RelayDailyLimit: t.RelayDailyLimit,
		MergeWindowSec: t.MergeWindowSec, ArchivedAt: t.ArchivedAt, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

func (s *Server) writeTeam(c *rux.Context, svc *team.Service, t jobstore.Team) {
	r, err := svc.Roster(callerFromCtx(c), t.ID)
	if err != nil {
		writeError(c, teamStatus(err), "team roster failed", err.Error())
		return
	}
	v := teamViewOf(r.Team)
	v.Roster = make([]teamRosterView, 0, len(r.Entries))
	for _, e := range r.Entries {
		sv := seatViewOf(e.Status)
		sv.Stage = e.Stage
		v.Roster = append(v.Roster, teamRosterView{Role: e.Role, seatView: sv})
	}
	c.JSON(http.StatusOK, v)
}

// GET /v1/teams: the caller's own teams; all=1 adds everyone's for a holder of
// seat.manage.others. Teams outside the caller's project ceiling are left out.
func (s *Server) handleTeamList(c *rux.Context) {
	svc, ok := s.teamsReady(c)
	if !ok {
		return
	}
	f := jobstore.TeamFilter{CallerID: callerFromCtx(c), ProjectKey: strings.TrimSpace(c.Query("project")),
		IncludeArchived: c.Query("include_archived") == "1"}
	if c.Query("all") == "1" && s.can(c, authz.PermSeatManageOthers) {
		f.CallerID = ""
	}
	rows, err := svc.List(f)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list teams failed", err.Error())
		return
	}
	rows = scopeRows(s, c, rows, func(t jobstore.Team) string { return t.ProjectKey })
	out := make([]teamView, 0, len(rows))
	for _, t := range rows {
		out = append(out, teamViewOf(t))
	}
	c.JSON(http.StatusOK, map[string]any{"teams": out})
}

type teamCreateReq struct {
	ProjectKey      string   `json:"project_key"`
	Name            string   `json:"name"`
	Leader          string   `json:"leader"`
	Members         []string `json:"members"`
	Brief           string   `json:"brief"`
	RelayMode       string   `json:"relay_mode"`
	RelayRoundLimit *int     `json:"relay_round_limit"`
	RelayDailyLimit *int     `json:"relay_daily_limit"`
	MergeWindowSec  *int     `json:"merge_window_sec"`
}

// POST /v1/teams creates a team for the caller. leader / members are seat ids or seat names of
// the caller in the project.
func (s *Server) handleTeamCreate(c *rux.Context) {
	svc, ok := s.teamsReady(c)
	if !ok {
		return
	}
	var body teamCreateReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	project := strings.TrimSpace(body.ProjectKey)
	if !s.requireProjectScope(c, project) {
		return
	}
	t, err := svc.Create(team.CreateInput{
		CallerID: callerFromCtx(c), ProjectKey: project, Name: body.Name, Leader: body.Leader, Members: body.Members,
		Brief: body.Brief, RelayMode: body.RelayMode, RelayRoundLimit: body.RelayRoundLimit,
		RelayDailyLimit: body.RelayDailyLimit, MergeWindowSec: body.MergeWindowSec,
	})
	if err != nil {
		writeError(c, teamStatus(err), "create team failed", err.Error())
		return
	}
	s.writeTeam(c, svc, t)
}

// GET /v1/teams/{id}: the team with its roster.
func (s *Server) handleTeamGet(c *rux.Context) {
	svc, t, ok := s.loadTeam(c)
	if !ok {
		return
	}
	s.writeTeam(c, svc, t)
}

type teamPatchReq struct {
	Name            *string `json:"name"`
	Leader          *string `json:"leader"`
	Brief           *string `json:"brief"`
	RelayMode       *string `json:"relay_mode"`
	RelayRoundLimit *int    `json:"relay_round_limit"`
	RelayDailyLimit *int    `json:"relay_daily_limit"`
	MergeWindowSec  *int    `json:"merge_window_sec"`
	Archived        *bool   `json:"archived"`
}

// PATCH /v1/teams/{id}: name, leader (seat id or name), brief, relay limits, merge window;
// "archived": true archives the team.
func (s *Server) handleTeamPatch(c *rux.Context) {
	svc, t, ok := s.loadTeam(c)
	if !ok {
		return
	}
	var body teamPatchReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if body.Name != nil || body.Leader != nil || body.Brief != nil || body.RelayMode != nil ||
		body.RelayRoundLimit != nil || body.RelayDailyLimit != nil || body.MergeWindowSec != nil {
		var err error
		t, err = svc.Update(t.ID, team.UpdateInput{Name: body.Name, Leader: body.Leader, Brief: body.Brief,
			RelayMode: body.RelayMode, RelayRoundLimit: body.RelayRoundLimit, RelayDailyLimit: body.RelayDailyLimit,
			MergeWindowSec: body.MergeWindowSec})
		if err != nil {
			writeError(c, teamStatus(err), "update team failed", err.Error())
			return
		}
	}
	if body.Archived != nil && *body.Archived {
		var err error
		if t, err = svc.Archive(t.ID); err != nil {
			writeError(c, teamStatus(err), "archive team failed", err.Error())
			return
		}
	}
	s.writeTeam(c, svc, t)
}

// DELETE /v1/teams/{id} archives the team (its rows stay; its name and seats are freed).
func (s *Server) handleTeamArchive(c *rux.Context) {
	svc, t, ok := s.loadTeam(c)
	if !ok {
		return
	}
	got, err := svc.Archive(t.ID)
	if err != nil {
		writeError(c, teamStatus(err), "archive team failed", err.Error())
		return
	}
	s.writeTeam(c, svc, got)
}

type teamMemberReq struct {
	// Seat is a seat id or a seat name of the team's owner in the team's project.
	Seat string `json:"seat"`
}

// POST /v1/teams/{id}/members adds a member seat.
func (s *Server) handleTeamMemberAdd(c *rux.Context) {
	svc, t, ok := s.loadTeam(c)
	if !ok {
		return
	}
	var body teamMemberReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if _, err := svc.AddMember(t.ID, body.Seat); err != nil {
		writeError(c, teamStatus(err), "add team member failed", err.Error())
		return
	}
	s.writeTeam(c, svc, t)
}

// DELETE /v1/teams/{id}/members/{seat} removes a member seat (an id or a name).
func (s *Server) handleTeamMemberRemove(c *rux.Context) {
	svc, t, ok := s.loadTeam(c)
	if !ok {
		return
	}
	removed, err := svc.RemoveMember(t.ID, c.Param("seat"))
	if err != nil {
		writeError(c, teamStatus(err), "remove team member failed", err.Error())
		return
	}
	if !removed {
		writeError(c, http.StatusNotFound, "not a member", c.Param("seat")+" is not a member of this team")
		return
	}
	s.writeTeam(c, svc, t)
}

// teamListLimit reads the ?limit= of a record list (default 100, at most 500).
func teamListLimit(c *rux.Context) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if err != nil || n <= 0 {
		return 100
	}
	return min(n, 500)
}

type teamRouteView struct {
	ID        int64    `json:"id"`
	MailID    int64    `json:"mail_id,omitempty"`
	FromRef   string   `json:"from_ref,omitempty"`
	ToSeatIDs []string `json:"to_seat_ids"`
	Rule      string   `json:"rule"`
	Hop       int      `json:"hop"`
	RouteKey  string   `json:"route_key,omitempty"`
	Outcome   string   `json:"outcome"`
	DecidedAt int64    `json:"decided_at"`
}

// GET /v1/teams/{id}/routes: the routing decisions, newest first.
func (s *Server) handleTeamRoutes(c *rux.Context) {
	svc, t, ok := s.loadTeam(c)
	if !ok {
		return
	}
	rows, err := svc.Routes(t.ID, teamListLimit(c))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list team routes failed", err.Error())
		return
	}
	out := make([]teamRouteView, 0, len(rows))
	for _, r := range rows {
		ids := r.ToSeatIDs
		if ids == nil {
			ids = []string{}
		}
		out = append(out, teamRouteView{ID: r.ID, MailID: r.MailID, FromRef: r.FromRef, ToSeatIDs: ids, Rule: r.Rule,
			Hop: r.Hop, RouteKey: r.RouteKey, Outcome: r.Outcome, DecidedAt: r.DecidedAt})
	}
	c.JSON(http.StatusOK, map[string]any{"routes": out})
}

type teamEvalView struct {
	ID        int64  `json:"id"`
	LeaderRef string `json:"leader_ref,omitempty"`
	TurnRef   string `json:"turn_ref,omitempty"`
	Outcome   string `json:"outcome"`
	Reason    string `json:"reason,omitempty"`
	At        int64  `json:"at"`
}

// GET /v1/teams/{id}/evals: the leader's evaluations, newest first.
func (s *Server) handleTeamEvals(c *rux.Context) {
	svc, t, ok := s.loadTeam(c)
	if !ok {
		return
	}
	rows, err := svc.Evals(t.ID, teamListLimit(c))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list team evals failed", err.Error())
		return
	}
	out := make([]teamEvalView, 0, len(rows))
	for _, e := range rows {
		out = append(out, teamEvalView{ID: e.ID, LeaderRef: e.LeaderRef, TurnRef: e.TurnRef, Outcome: e.Outcome, Reason: e.Reason, At: e.At})
	}
	c.JSON(http.StatusOK, map[string]any{"evals": out})
}

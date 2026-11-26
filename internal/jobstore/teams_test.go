package jobstore

import (
	"errors"
	"testing"
)

func mustTeam(t *testing.T, s *Store, project, name string, leader Seat, members ...Seat) Team {
	t.Helper()
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.ID)
	}
	tm, err := s.CreateTeam(Team{ProjectKey: project, Name: name, LeaderSeatID: leader.ID,
		RelayRoundLimit: DefaultTeamRelayRoundLimit, RelayDailyLimit: DefaultTeamRelayDailyLimit, MergeWindowSec: DefaultTeamMergeWindowSec}, ids)
	if err != nil {
		t.Fatalf("create team %s: %v", name, err)
	}
	return tm
}

func TestTeamCreateAndRead(t *testing.T) {
	s := openTest(t)
	lead := mustSeat(t, s, "u1", "proj", "lead")
	a := mustSeat(t, s, "u1", "proj", "a")
	tm := mustTeam(t, s, "proj", "core", lead, a)
	if tm.CallerID != "u1" || tm.RelayMode != TeamRelayLeader {
		t.Fatalf("team = %+v", tm)
	}
	got, ok, err := s.GetTeamByName("proj", "core")
	if err != nil || !ok || got.ID != tm.ID || got.RelayRoundLimit != 3 || got.RelayDailyLimit != 20 || got.MergeWindowSec != 120 {
		t.Fatalf("by name = %+v ok=%v err=%v", got, ok, err)
	}
	members, err := s.ListTeamMembers(tm.ID)
	if err != nil || len(members) != 1 || members[0].SeatID != a.ID || members[0].Role != TeamRoleMember {
		t.Fatalf("members = %+v %v", members, err)
	}
	if of, ok, _ := s.TeamOfSeat(a.ID); !ok || of.ID != tm.ID {
		t.Fatalf("team of member = %+v %v", of, ok)
	}
	if of, ok, _ := s.TeamOfSeat(lead.ID); !ok || of.ID != tm.ID {
		t.Fatalf("team of leader = %+v %v", of, ok)
	}
}

func TestTeamConstraints(t *testing.T) {
	s := openTest(t)
	lead := mustSeat(t, s, "u1", "proj", "lead")
	a := mustSeat(t, s, "u1", "proj", "a")
	b := mustSeat(t, s, "u1", "proj", "b")
	tm := mustTeam(t, s, "proj", "core", lead, a)

	// name unique per project among active teams
	lead2 := mustSeat(t, s, "u1", "proj", "lead2")
	if _, err := s.CreateTeam(Team{ProjectKey: "proj", Name: "core", LeaderSeatID: lead2.ID}, nil); !errors.Is(err, ErrTeamNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	// a seat belongs to at most one team (member of another, leader of another)
	if _, err := s.CreateTeam(Team{ProjectKey: "proj", Name: "other", LeaderSeatID: lead2.ID}, []string{a.ID}); !errors.Is(err, ErrTeamSeatTaken) {
		t.Fatalf("member of two teams: %v", err)
	}
	if _, err := s.CreateTeam(Team{ProjectKey: "proj", Name: "other2", LeaderSeatID: a.ID}, nil); !errors.Is(err, ErrTeamSeatTaken) {
		t.Fatalf("member leading another team: %v", err)
	}
	if err := s.AddTeamMember(tm.ID, lead.ID); !errors.Is(err, ErrTeamInvalid) {
		t.Fatalf("leader as member: %v", err)
	}
	// leader and members must be seats of the team's project
	foreign := mustSeat(t, s, "u1", "elsewhere", "f")
	if err := s.AddTeamMember(tm.ID, foreign.ID); !errors.Is(err, ErrTeamInvalid) {
		t.Fatalf("foreign project member: %v", err)
	}
	if _, err := s.CreateTeam(Team{ProjectKey: "proj", Name: "x", LeaderSeatID: foreign.ID}, nil); !errors.Is(err, ErrTeamInvalid) {
		t.Fatalf("foreign project leader: %v", err)
	}
	if err := s.AddTeamMember(tm.ID, "seat-missing"); !errors.Is(err, ErrSeatNotFound) {
		t.Fatalf("missing seat: %v", err)
	}
	// adding twice is idempotent
	if err := s.AddTeamMember(tm.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTeamMember(tm.ID, b.ID); err != nil {
		t.Fatalf("second add: %v", err)
	}
	if m, _ := s.ListTeamMembers(tm.ID); len(m) != 2 {
		t.Fatalf("members after double add = %d", len(m))
	}
	// a new leader may not be a current member
	if _, err := s.UpdateTeam(tm.ID, TeamPatch{LeaderSeatID: &a.ID}); !errors.Is(err, ErrTeamInvalid) {
		t.Fatalf("member as new leader: %v", err)
	}
	if ok, err := s.RemoveTeamMember(tm.ID, a.ID); err != nil || !ok {
		t.Fatalf("remove: %v %v", ok, err)
	}
	if _, err := s.UpdateTeam(tm.ID, TeamPatch{LeaderSeatID: &a.ID}); err != nil {
		t.Fatalf("removed member as leader: %v", err)
	}
	if ok, _ := s.RemoveTeamMember(tm.ID, b.ID); !ok {
		t.Fatal("remove b")
	}
	if ok, _ := s.RemoveTeamMember(tm.ID, b.ID); ok {
		t.Fatal("second remove should report false")
	}
}

func TestTeamArchiveFreesSeatsAndName(t *testing.T) {
	s := openTest(t)
	lead := mustSeat(t, s, "u1", "proj", "lead")
	a := mustSeat(t, s, "u1", "proj", "a")
	tm := mustTeam(t, s, "proj", "core", lead, a)
	if _, err := s.ArchiveTeam(tm.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetTeamByName("proj", "core"); ok {
		t.Fatal("archived team found by name")
	}
	if _, ok, _ := s.TeamOfSeat(a.ID); ok {
		t.Fatal("archived team still owns its seat")
	}
	mustTeam(t, s, "proj", "core", lead, a) // name and seats are free again
	if err := s.AddTeamMember(tm.ID, a.ID); !errors.Is(err, ErrTeamArchived) {
		t.Fatalf("edit archived: %v", err)
	}
	if all, _ := s.ListTeams(TeamFilter{ProjectKey: "proj", IncludeArchived: true}); len(all) != 2 {
		t.Fatalf("list with archived = %d", len(all))
	}
	if act, _ := s.ListTeams(TeamFilter{ProjectKey: "proj"}); len(act) != 1 {
		t.Fatalf("list active = %d", len(act))
	}
}

func TestTeamDifferentCallerSeatRejected(t *testing.T) {
	s := openTest(t)
	lead := mustSeat(t, s, "u1", "proj", "lead")
	other := mustSeat(t, s, "u2", "proj", "o")
	tm := mustTeam(t, s, "proj", "core", lead)
	if err := s.AddTeamMember(tm.ID, other.ID); !errors.Is(err, ErrTeamInvalid) {
		t.Fatalf("other caller's seat: %v", err)
	}
}

func TestTeamRoutesAndEvals(t *testing.T) {
	s := openTest(t)
	lead := mustSeat(t, s, "u1", "proj", "lead")
	tm := mustTeam(t, s, "proj", "core", lead)
	r, err := s.AppendTeamRoute(TeamRoute{TeamID: tm.ID, MailID: 7, FromRef: "s1", ToSeatIDs: []string{lead.ID},
		Rule: "member_result", Hop: 1, RouteKey: "7:" + lead.ID, Outcome: TeamOutcomeDelivered})
	if err != nil || r.ID == 0 {
		t.Fatalf("append route: %+v %v", r, err)
	}
	if _, err := s.AppendTeamRoute(TeamRoute{TeamID: tm.ID, RouteKey: "7:" + lead.ID, Outcome: TeamOutcomePending}); !errors.Is(err, ErrTeamRouteKeyTaken) {
		t.Fatalf("duplicate route key: %v", err)
	}
	// rows without a key (dup / self records) may repeat
	for i := 0; i < 2; i++ {
		if _, err := s.AppendTeamRoute(TeamRoute{TeamID: tm.ID, Outcome: TeamOutcomeDup}); err != nil {
			t.Fatalf("keyless route %d: %v", i, err)
		}
	}
	p, err := s.AppendTeamRoute(TeamRoute{TeamID: tm.ID, RouteKey: "8:x", Outcome: TeamOutcomePending})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SettleTeamRoute(p.ID, TeamOutcomeQueued); err != nil || !ok {
		t.Fatalf("settle: %v %v", ok, err)
	}
	if got, ok, _ := s.TeamRouteByKey(tm.ID, "8:x"); !ok || got.Outcome != TeamOutcomeQueued {
		t.Fatalf("by key = %+v %v", got, ok)
	}
	if _, ok, _ := s.TeamRouteByKey(tm.ID, ""); ok {
		t.Fatal("empty key must not match")
	}
	if _, err := s.SettleTeamRoute(p.ID, "bogus"); !errors.Is(err, ErrTeamInvalid) {
		t.Fatalf("bad settle: %v", err)
	}
	if _, err := s.AppendTeamRoute(TeamRoute{TeamID: tm.ID, Outcome: "bogus"}); !errors.Is(err, ErrTeamInvalid) {
		t.Fatalf("bad outcome: %v", err)
	}
	routes, err := s.ListTeamRoutes(tm.ID, 0)
	if err != nil || len(routes) != 4 || len(routes[3].ToSeatIDs) != 1 || routes[3].ToSeatIDs[0] != lead.ID || routes[3].Rule != "member_result" {
		t.Fatalf("routes = %+v %v", routes, err)
	}
	if _, err := s.AppendTeamEval(TeamEval{TeamID: tm.ID, LeaderRef: "s1", Outcome: TeamEvalIdle, Reason: "nothing"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendTeamEval(TeamEval{TeamID: tm.ID, Outcome: "nope"}); !errors.Is(err, ErrTeamInvalid) {
		t.Fatalf("bad eval outcome: %v", err)
	}
	evals, err := s.ListTeamEvals(tm.ID, 10)
	if err != nil || len(evals) != 1 || evals[0].Outcome != TeamEvalIdle || evals[0].Reason != "nothing" {
		t.Fatalf("evals = %+v %v", evals, err)
	}
}

func TestFindSeatByName(t *testing.T) {
	s := openTest(t)
	x := mustSeat(t, s, "u1", "proj", "a")
	if got, ok, _ := s.FindSeatByName("u1", "proj", "a"); !ok || got.ID != x.ID {
		t.Fatalf("find = %+v %v", got, ok)
	}
	if _, ok, _ := s.FindSeatByName("u2", "proj", "a"); ok {
		t.Fatal("another caller's seat found")
	}
	if _, ok, _ := s.FindSeatByName("", "proj", "a"); !ok {
		t.Fatal("any-caller lookup failed")
	}
}

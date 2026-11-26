package team

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/seat"
	"github.com/inhere/gofer/internal/stagenote"
)

type env struct {
	st   *jobstore.Store
	seat *seat.Service
	svc  *Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ss := seat.New(st, seat.Deps{})
	e := &env{st: st, seat: ss}
	e.svc = New(st, Deps{Seats: ss, StageBrief: func(ref string) *stagenote.Brief {
		if ref == "sess-a" {
			return &stagenote.Brief{WorkItemID: "w1", Step: "doing"}
		}
		return nil
	}})
	return e
}

func (e *env) seatOf(t *testing.T, project, name string) jobstore.Seat {
	t.Helper()
	x, err := e.seat.Create(seat.CreateInput{CallerID: "u1", ProjectKey: project, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func TestCreateByNamesAndDefaults(t *testing.T) {
	e := newEnv(t)
	e.seatOf(t, "proj", "lead")
	e.seatOf(t, "proj", "a")
	tm, err := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: "core", Leader: "lead", Members: []string{"a"}, Brief: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if tm.RelayRoundLimit != 3 || tm.RelayDailyLimit != 20 || tm.MergeWindowSec != 120 || tm.RelayMode != jobstore.TeamRelayLeader {
		t.Fatalf("defaults = %+v", tm)
	}
	zero := 0
	t2, err := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: "z", Leader: e.seatOf(t, "proj", "l2").ID, MergeWindowSec: &zero})
	if err != nil || t2.MergeWindowSec != 0 {
		t.Fatalf("explicit zero merge window = %+v %v", t2, err)
	}
}

func TestCreateNegatives(t *testing.T) {
	e := newEnv(t)
	e.seatOf(t, "proj", "lead")
	a := e.seatOf(t, "proj", "a")
	e.seatOf(t, "other", "o")
	if _, err := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: "t", Leader: "missing"}); !errors.Is(err, jobstore.ErrSeatNotFound) {
		t.Fatalf("unknown leader: %v", err)
	}
	if _, err := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: "t", Leader: "lead", Members: []string{"lead"}}); !errors.Is(err, jobstore.ErrTeamInvalid) {
		t.Fatalf("leader in members: %v", err)
	}
	// a seat name of another project does not resolve in this one
	if _, err := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: "t", Leader: "lead", Members: []string{"o"}}); !errors.Is(err, jobstore.ErrSeatNotFound) {
		t.Fatalf("other project's seat by name: %v", err)
	}
	if _, err := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: "t", Leader: "lead", RelayMode: "weird"}); !errors.Is(err, jobstore.ErrTeamInvalid) {
		t.Fatalf("bad relay mode: %v", err)
	}
	if _, err := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: "t", Leader: "lead", Members: []string{a.Name}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: "t2", Leader: "a"}); !errors.Is(err, jobstore.ErrTeamSeatTaken) {
		t.Fatalf("member as another team's leader: %v", err)
	}
}

func TestMembersLeaderBriefArchive(t *testing.T) {
	e := newEnv(t)
	e.seatOf(t, "proj", "lead")
	e.seatOf(t, "proj", "a")
	e.seatOf(t, "proj", "b")
	tm, _ := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: "core", Leader: "lead"})
	if _, err := e.svc.AddMember(tm.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetBrief(tm.ID, "new brief"); err != nil {
		t.Fatal(err)
	}
	// b cannot lead while a still leads... switching the leader to b frees lead
	if got, err := e.svc.SetLeader(tm.ID, "b"); err != nil || got.LeaderSeatID == tm.LeaderSeatID {
		t.Fatalf("set leader: %+v %v", got, err)
	}
	if _, err := e.svc.SetLeader(tm.ID, "a"); !errors.Is(err, jobstore.ErrTeamInvalid) {
		t.Fatalf("member as leader: %v", err)
	}
	if ok, err := e.svc.RemoveMember(tm.ID, "a"); err != nil || !ok {
		t.Fatalf("remove: %v %v", ok, err)
	}
	found, err := e.svc.Find("proj", "core")
	if err != nil || found.Brief != "new brief" {
		t.Fatalf("find by name: %+v %v", found, err)
	}
	if _, err := e.svc.Archive(tm.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Find("proj", "core"); !errors.Is(err, jobstore.ErrTeamNotFound) {
		t.Fatalf("archived team found: %v", err)
	}
	if _, err := e.svc.SetBrief(tm.ID, "x"); !errors.Is(err, jobstore.ErrTeamArchived) {
		t.Fatalf("edit archived: %v", err)
	}
}

func TestRoster(t *testing.T) {
	e := newEnv(t)
	lead := e.seatOf(t, "proj", "lead")
	a := e.seatOf(t, "proj", "a")
	e.seatOf(t, "proj", "b")
	if _, err := e.st.UpsertAgentSession(jobstore.AgentSession{SessionID: "sess-a", Agent: "claude", ProjectKey: "proj", Runner: "server", CallerID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.seat.Bind(a.ID, "sess-a", false, "user"); err != nil {
		t.Fatal(err)
	}
	tm, _ := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: "core", Leader: lead.Name, Members: []string{"a", "b"}})
	r, err := e.svc.Roster("u1", tm.ID)
	if err != nil || len(r.Entries) != 3 {
		t.Fatalf("roster = %+v %v", r, err)
	}
	if r.Entries[0].Role != RoleLeader || r.Entries[0].Status.State != seat.StateNone {
		t.Fatalf("leader entry = %+v", r.Entries[0])
	}
	var gotA RosterEntry
	for _, en := range r.Entries {
		if en.Status.Seat.ID == a.ID {
			gotA = en
		}
	}
	if gotA.Role != RoleMember || gotA.Status.State != seat.StateRunning || gotA.Stage == nil || gotA.Stage.Step != "doing" {
		t.Fatalf("member a entry = %+v", gotA)
	}
	if _, err := e.svc.Roster("u1", "team-none"); !errors.Is(err, jobstore.ErrTeamNotFound) {
		t.Fatalf("unknown roster: %v", err)
	}
}

func TestRecordRouteAndEval(t *testing.T) {
	e := newEnv(t)
	lead := e.seatOf(t, "proj", "lead")
	tm, _ := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: "core", Leader: lead.ID})
	if _, err := e.svc.RecordRoute(jobstore.TeamRoute{TeamID: tm.ID, MailID: 1, Rule: "human_default", Outcome: jobstore.TeamOutcomeDelivered, ToSeatIDs: []string{lead.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RecordEval(jobstore.TeamEval{TeamID: tm.ID, Outcome: jobstore.TeamEvalDispatched, Reason: "r"}); err != nil {
		t.Fatal(err)
	}
	if rs, _ := e.svc.Routes(tm.ID, 10); len(rs) != 1 || rs[0].Rule != "human_default" {
		t.Fatalf("routes = %+v", rs)
	}
	if es, _ := e.svc.Evals(tm.ID, 10); len(es) != 1 {
		t.Fatalf("evals = %+v", es)
	}
	if _, err := e.svc.RecordEval(jobstore.TeamEval{TeamID: tm.ID, Outcome: "x"}); err == nil {
		t.Fatal("bad eval outcome accepted")
	}
}

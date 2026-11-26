package mail

import (
	"errors"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// seat: / team: addresses (plan 2026-11-26-team-plan.md §2.2).

func TestParseSeatTeamAddress(t *testing.T) {
	for raw, want := range map[string]Address{
		"seat:review":         {Scheme: SchemeSeat, Value: "review"},
		"seat:review@proj":    {Scheme: SchemeSeat, Value: "review", Project: "proj"},
		"TEAM: core @ other ": {Scheme: SchemeTeam, Value: "core", Project: "other"},
	} {
		got, err := ParseAddress(raw)
		if err != nil || got.Scheme != want.Scheme || got.Value != want.Value || got.Project != want.Project || got.Each {
			t.Fatalf("ParseAddress(%q) = %+v, %v; want %+v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"seat:", "team:", "seat:@proj", "each:seat:a", "each:team:a"} {
		if _, err := ParseAddress(raw); !errors.Is(err, ErrInvalidAddress) {
			t.Fatalf("ParseAddress(%q) err = %v, want ErrInvalidAddress", raw, err)
		}
	}
}

func (f *fixture) seat(t *testing.T, project, name, kind, ref string) jobstore.Seat {
	t.Helper()
	x, err := f.store.CreateSeat(jobstore.Seat{CallerID: "alice", ProjectKey: project, Name: name, Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	if ref != "" {
		if x, err = f.store.BindSeat(x.ID, ref, false, "user"); err != nil {
			t.Fatal(err)
		}
	}
	return x
}

func (f *fixture) team(t *testing.T, project, name string, leader jobstore.Seat) jobstore.Team {
	t.Helper()
	tm, err := f.store.CreateTeam(jobstore.Team{ProjectKey: project, Name: name, LeaderSeatID: leader.ID,
		RelayRoundLimit: 3, RelayDailyLimit: 20, MergeWindowSec: 120}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestSendSeatAddress(t *testing.T) {
	f := newFixture(t)
	f.addSession(t, "sess-s1", "claude", "", jobstore.SessionRunning)
	f.addSession(t, "sess-off", "claude", "", jobstore.SessionOffline)
	f.addJob(t, "job-live", "running")
	f.addJob(t, "job-done", "done")
	f.seat(t, "proj", "term", "terminal", "sess-s1")
	f.seat(t, "proj", "away", "terminal", "sess-off")
	f.seat(t, "proj", "acp", "acp", "job-live")
	f.seat(t, "proj", "acpdone", "acp", "job-done")
	f.seat(t, "proj", "empty", "terminal", "")
	f.seat(t, "other", "far", "terminal", "")
	org := Origin{Kind: jobstore.MailKindCaller, Caller: "alice"}

	// a terminal seat resolves to the session sitting in it; the project comes from the request
	res, err := f.svc.Send(SendInput{To: "seat:term", Body: "hi", Origin: org, Project: "proj"})
	if err != nil || res.Held || res.Target.Kind != jobstore.MailKindSession || res.Target.ID != "sess-s1" || res.Mail.TeamID != "" {
		t.Fatalf("terminal seat = %+v err %v", res, err)
	}
	// an ACP seat resolves to its live job
	if res, err = f.svc.Send(SendInput{To: "seat:acp@proj", Body: "hi", Origin: org}); err != nil || res.Held || res.Target.Kind != jobstore.MailKindJob || res.Target.ID != "job-live" {
		t.Fatalf("acp seat = %+v err %v", res, err)
	}
	// an offline session is queued as today (pending, flagged offline)
	if res, err = f.svc.Send(SendInput{To: "seat:away@proj", Body: "hi", Origin: org}); err != nil || res.Held || !res.Target.Offline || res.Mail.State != jobstore.MailPending {
		t.Fatalf("offline seat = %+v err %v", res, err)
	}
	// an empty seat holds the letter in the inbox (capture box)
	if res, err = f.svc.Send(SendInput{To: "seat:empty@proj", Body: "hi", Origin: org}); err != nil || !res.Held || res.CaptureItemID == "" || res.Mail.State != jobstore.MailHeld {
		t.Fatalf("empty seat = %+v err %v", res, err)
	}
	// so does a seat whose job ended
	if res, err = f.svc.Send(SendInput{To: "seat:acpdone@proj", Body: "hi", Origin: org}); err != nil || !res.Held {
		t.Fatalf("ended job seat = %+v err %v", res, err)
	}
	// the sender's own project is the default when the sender is a session
	f.store.UpsertAgentSession(jobstore.AgentSession{SessionID: "sess-from", Agent: "claude", CallerID: "alice", ProjectKey: "proj", State: jobstore.SessionRunning})
	from := Origin{Kind: jobstore.MailKindSession, ID: "sess-from", Caller: "alice"}
	if res, err = f.svc.Send(SendInput{To: "seat:term", Body: "hi", Origin: from}); err != nil || res.Target.ID != "sess-s1" {
		t.Fatalf("sender project default = %+v err %v", res, err)
	}
	// ...and an explicit @project beats it
	if res, err = f.svc.Send(SendInput{To: "seat:far@other", Body: "hi", Origin: from}); err != nil || !res.Held {
		t.Fatalf("explicit project = %+v err %v", res, err)
	}

	// negatives: unknown seat, another project's seat name, no project at all, another caller
	if _, err = f.svc.Send(SendInput{To: "seat:nope@proj", Body: "hi", Origin: org}); !errors.Is(err, ErrSeatNotFound) {
		t.Fatalf("unknown seat err = %v", err)
	}
	if _, err = f.svc.Send(SendInput{To: "seat:far@proj", Body: "hi", Origin: org}); !errors.Is(err, ErrSeatNotFound) {
		t.Fatalf("seat of another project err = %v", err)
	}
	if _, err = f.svc.Send(SendInput{To: "seat:term", Body: "hi", Origin: org}); !errors.Is(err, ErrInvalidAddress) {
		t.Fatalf("no project err = %v", err)
	}
	if _, err = f.svc.Send(SendInput{To: "seat:term@proj", Body: "hi", Origin: Origin{Kind: jobstore.MailKindCaller, Caller: "mallory"}}); !errors.Is(err, ErrSeatNotFound) {
		t.Fatalf("another caller's seat err = %v", err)
	}
}

func TestSendTeamAddress(t *testing.T) {
	f := newFixture(t)
	f.addSession(t, "sess-lead", "claude", "", jobstore.SessionRunning)
	f.addSession(t, "sess-xlead", "claude", "", jobstore.SessionRunning)
	lead := f.seat(t, "proj", "lead", "terminal", "sess-lead")
	tm := f.team(t, "proj", "core", lead)
	xlead := f.seat(t, "other", "xlead", "terminal", "sess-xlead")
	xtm := f.team(t, "other", "core", xlead) // same name, another project
	vacant := f.seat(t, "proj", "lead2", "terminal", "")
	vt := f.team(t, "proj", "quiet", vacant)
	org := Origin{Kind: jobstore.MailKindCaller, Caller: "alice"}

	// the team address is the leader seat, and the letter is tagged with the team
	res, err := f.svc.Send(SendInput{To: "team:core", Body: "hi", Origin: org, Project: "proj"})
	if err != nil || res.Held || res.Target.ID != "sess-lead" || res.Mail.TeamID != tm.ID || res.Mail.ToAddress != "team:core" {
		t.Fatalf("team send = %+v err %v", res, err)
	}
	// cross-project: a leader writes to another project's leader
	from := Origin{Kind: jobstore.MailKindSession, ID: "sess-lead", Caller: "alice"}
	if res, err = f.svc.Send(SendInput{To: "team:core@other", Body: "hi", Origin: from, Project: "proj"}); err != nil || res.Target.ID != "sess-xlead" || res.Mail.TeamID != xtm.ID {
		t.Fatalf("cross-project team send = %+v err %v", res, err)
	}
	// a team whose leader seat is vacant: held, still tagged
	if res, err = f.svc.Send(SendInput{To: "team:quiet", Body: "hi", Origin: org, Project: "proj"}); err != nil || !res.Held || res.Mail.TeamID != vt.ID {
		t.Fatalf("vacant leader = %+v err %v", res, err)
	}
	// negatives
	if _, err = f.svc.Send(SendInput{To: "team:missing", Body: "hi", Origin: org, Project: "proj"}); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("unknown team err = %v", err)
	}
	if _, err = f.svc.Send(SendInput{To: "team:core", Body: "hi", Origin: Origin{Kind: jobstore.MailKindCaller, Caller: "mallory"}, Project: "proj"}); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("another caller's team err = %v", err)
	}
	if _, err = f.store.ArchiveTeam(tm.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Send(SendInput{To: "team:core", Body: "hi", Origin: org, Project: "proj"}); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("archived team err = %v", err)
	}
}

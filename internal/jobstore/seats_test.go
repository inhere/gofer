package jobstore

import (
	"errors"
	"sync"
	"testing"
)

func mustSeat(t *testing.T, s *Store, caller, project, name string) Seat {
	t.Helper()
	x, err := s.CreateSeat(Seat{CallerID: caller, ProjectKey: project, Name: name, Purpose: "p", Charter: "c"})
	if err != nil {
		t.Fatalf("create seat %s: %v", name, err)
	}
	return x
}

func TestSeatNameUniqueness(t *testing.T) {
	s := openTest(t)
	a := mustSeat(t, s, "u1", "proj", "review")
	if _, err := s.CreateSeat(Seat{CallerID: "u1", ProjectKey: "proj", Name: "review"}); !errors.Is(err, ErrSeatNameTaken) {
		t.Fatalf("duplicate name in a project: want ErrSeatNameTaken, got %v", err)
	}
	mustSeat(t, s, "u1", "other", "review") // another project: allowed
	mustSeat(t, s, "u2", "proj", "review")  // another caller: allowed
	if _, err := s.ArchiveSeat(a.ID, "user"); err != nil {
		t.Fatal(err)
	}
	mustSeat(t, s, "u1", "proj", "review") // archived frees the name
}

func TestSeatRebindWritesHistory(t *testing.T) {
	s := openTest(t)
	x := mustSeat(t, s, "u1", "proj", "a")
	if _, err := s.BindSeat(x.ID, "sess-1", false, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindSeat(x.ID, "sess-2", false, "user"); !errors.Is(err, ErrSeatOccupied) {
		t.Fatalf("bind over an occupied seat: want ErrSeatOccupied, got %v", err)
	}
	if _, err := s.BindSeat(x.ID, "sess-2", true, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnbindSeat(x.ID, "user"); err != nil {
		t.Fatal(err)
	}
	h, err := s.ListSeatBindings(x.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, b := range h {
		got = append(got, b.Action+":"+b.FromRef+">"+b.ToRef)
	}
	want := []string{"bind:>sess-1", "rebind:sess-1>sess-2", "unbind:sess-2>"}
	if len(got) != len(want) {
		t.Fatalf("history %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("history %v, want %v", got, want)
		}
	}
}

func TestSeatSessionSitsInOneSeatOnly(t *testing.T) {
	s := openTest(t)
	a, b := mustSeat(t, s, "u1", "proj", "a"), mustSeat(t, s, "u1", "proj", "b")
	if _, err := s.BindSeat(a.ID, "sess-1", false, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindSeat(b.ID, "sess-1", false, "user"); !errors.Is(err, ErrSeatRefTaken) {
		t.Fatalf("one session in two seats: want ErrSeatRefTaken, got %v", err)
	}
}

func TestSeatConcurrentBindIsDeterministic(t *testing.T) {
	s := openTest(t)
	x := mustSeat(t, s, "u1", "proj", "a")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, ref := range []string{"sess-1", "sess-2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.BindSeat(x.ID, ref, false, "user")
		}()
	}
	wg.Wait()
	ok, occupied := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrSeatOccupied):
			occupied++
		default:
			t.Fatalf("unexpected error %v", err)
		}
	}
	if ok != 1 || occupied != 1 {
		t.Fatalf("want one success and one ErrSeatOccupied, got %v", errs)
	}
}

func TestSeatReRegisterKeepsBinding(t *testing.T) {
	s := openTest(t)
	x := mustSeat(t, s, "u1", "proj", "a")
	if _, err := s.UpsertAgentSession(AgentSession{SessionID: "sess-1", Agent: "claude", ProjectKey: "proj", Cwd: "/w", CallerID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindSeat(x.ID, "sess-1", false, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSessionState("sess-1", SessionEnded); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertAgentSession(AgentSession{SessionID: "sess-1", Agent: "claude", ProjectKey: "proj", Cwd: "/w", CallerID: "u1"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.GetSeat(x.ID)
	if got.CurrentRef != "sess-1" {
		t.Fatalf("an ended session re-registering under the same id must keep its seat, current_ref=%q", got.CurrentRef)
	}
}

func TestSeatClearedWhenSessionDeleted(t *testing.T) {
	s := openTest(t)
	x := mustSeat(t, s, "u1", "proj", "a")
	if _, err := s.UpsertAgentSession(AgentSession{SessionID: "sess-1", Agent: "claude", ProjectKey: "proj", Cwd: "/w", CallerID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindSeat(x.ID, "sess-1", false, "user"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.DeleteAgentSession("sess-1"); err != nil || !ok {
		t.Fatalf("delete session: %v %v", ok, err)
	}
	got, _, _ := s.GetSeat(x.ID)
	if got.CurrentRef != "" {
		t.Fatalf("current_ref must be cleared, got %q", got.CurrentRef)
	}
	h, _ := s.ListSeatBindings(x.ID)
	if len(h) != 2 || h[1].Action != SeatActionSessionDeleted || h[1].FromRef != "sess-1" {
		t.Fatalf("history must record the deletion: %+v", h)
	}
}

func TestSeatCharterHistory(t *testing.T) {
	s := openTest(t)
	x := mustSeat(t, s, "u1", "proj", "a")
	np := "new purpose"
	if _, err := s.UpdateSeat(x.ID, SeatPatch{Purpose: &np, By: "session:s1"}); err != nil {
		t.Fatal(err)
	}
	nm := "renamed"
	got, err := s.UpdateSeat(x.ID, SeatPatch{Name: &nm, By: "session:s2"})
	if err != nil {
		t.Fatal(err)
	}
	if got.CharterBy != "session:s1" {
		t.Fatalf("a rename must not touch charter_by, got %q", got.CharterBy)
	}
	h, _ := s.ListSeatCharterHistory(x.ID)
	if len(h) != 2 || h[1].By != "session:s1" || h[1].Purpose != np {
		t.Fatalf("charter history: %+v", h)
	}
}

func TestSeatTablesExistAfterUpgrade(t *testing.T) {
	// the base schema statements for seats are IF NOT EXISTS and reference no migrated column
	s := openTest(t)
	for _, tbl := range []string{"seats", "seat_bindings", "seat_charter_history"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name=?`, tbl).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s missing: %d %v", tbl, n, err)
		}
	}
}

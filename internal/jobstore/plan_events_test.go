package jobstore

import "testing"

func TestPlanEventsRoundTrip(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	a, err := s.InsertPlanEvent(PlanEventRow{PlanID: "p1", Kind: "advance", By: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.InsertPlanEvent(PlanEventRow{PlanID: "p2", TodoID: "t", JobID: "j", Kind: "block"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListPlanEvents(10)
	if err != nil || len(rows) != 2 || rows[0].ID != a || rows[1].ID != b {
		t.Fatalf("rows = %+v, err = %v", rows, err)
	}
	if rows[0].By != "alice" || rows[1].JobID != "j" || rows[0].CreatedAt == 0 {
		t.Fatalf("fields lost: %+v", rows)
	}
	if err := s.DeletePlanEvent(a); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePlanEvent(a); err != nil {
		t.Fatalf("deleting a missing row must not fail: %v", err)
	}
	rows, _ = s.ListPlanEvents(10)
	if len(rows) != 1 || rows[0].ID != b {
		t.Fatalf("after delete rows = %+v", rows)
	}
}

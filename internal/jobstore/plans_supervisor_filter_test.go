package jobstore

import "testing"

// TestPlanFilterSupervisorSessionID locks the filter GET /v1/sessions/{sid}/focus
// uses: only the plans bound to that session, whatever their state.
func TestPlanFilterSupervisorSessionID(t *testing.T) {
	s := openTest(t)
	for _, p := range []Plan{
		{PlanID: "p-a", Title: "mine", Status: PlanOpen, SupervisorSessionID: "sess-a"},
		{PlanID: "p-b", Title: "other", Status: PlanOpen, SupervisorSessionID: "sess-b"},
		{PlanID: "p-c", Title: "unbound", Status: PlanOpen},
	} {
		if err := s.InsertPlan(p); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListPlans(PlanFilter{SupervisorSessionID: "sess-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PlanID != "p-a" {
		t.Fatalf("filter returned %+v, want only p-a", got)
	}
	if all, err := s.ListPlans(PlanFilter{}); err != nil || len(all) != 3 {
		t.Fatalf("an empty filter must not narrow the list: %+v %v", all, err)
	}
}

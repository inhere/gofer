package mcpserver

import "testing"

// TestSetPlanCompleteOn: the MCP path validates job|merge and updates the plan.
func TestSetPlanCompleteOn(t *testing.T) {
	jobs, projects, agents := testCore(t)
	b := newLocalBackend(jobs, projects, agents)
	pv, err := b.CreatePlan("complete-on plan", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setPlanCompleteOn(b, pv.PlanID, "later"); err == nil {
		t.Fatal("invalid complete_on was accepted")
	}
	got, err := setPlanCompleteOn(b, pv.PlanID, " merge ")
	if err != nil || got.CompleteOn != "merge" {
		t.Fatalf("set merge = %+v err=%v", got, err)
	}
	if _, err := setPlanCompleteOn(b, "plan-does-not-exist", "job"); err == nil {
		t.Fatal("unknown plan was accepted")
	}
}

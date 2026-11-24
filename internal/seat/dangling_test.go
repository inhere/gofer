package seat

import (
	"context"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestDeletedJobLeavesTheSeatVacant: the seat roster empties a seat whose ACP job is deleted (the
// store does it in the delete transaction), the history says why, and a schedule then treats the
// seat as vacant instead of aiming a say at a job that is gone.
func TestDeletedJobLeavesTheSeatVacant(t *testing.T) {
	e, _, sy := scheduleEnv(t)
	x := acpSeat(t, e, job.StatusDone)
	if got, _ := e.svc.Get(x.ID); got.CurrentRef != "job-old" {
		t.Fatalf("the seat must sit on the job before the delete: %q", got.CurrentRef)
	}
	if err := e.st.DeleteJob("job-old", "u1"); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.Get(x.ID)
	if err != nil || got.CurrentRef != "" {
		t.Fatalf("after the delete the seat must be vacant: %+v, %v", got, err)
	}
	st, err := e.svc.Status("u1", got)
	if err != nil || st.State != StateNone {
		t.Fatalf("a vacant seat reads %q (err %v), want %q", st.State, err, StateNone)
	}
	h, _, err := e.svc.History(x.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := h[len(h)-1]
	if last.Action != jobstore.SeatActionJobDeleted || last.FromRef != "job-old" || last.By != "system" {
		t.Fatalf("the history must record the deletion of the job: %+v", h)
	}
	// a schedule for the seat now finds nobody and starts no session
	said := len(sy.said)
	res, err := e.svc.DeliverToSeat(context.Background(), deliverReq(x.ID))
	if err != nil || res.Outcome != jobstore.ScheduleResultOffline {
		t.Fatalf("delivery to the emptied seat = %+v, %v", res, err)
	}
	if len(sy.said) != said || len(e.rs.calls) != 0 {
		t.Fatalf("nothing may be said or restarted: said=%+v restarts=%+v", sy.said[said:], e.rs.calls)
	}
}

// TestSeatWithAMissingJobReadsVacant: a current_ref whose job row is gone (an older database, or a
// row removed behind gofer's back) is a vacant seat, not an error — the roster, the open plan and
// the schedules all read it that way.
func TestSeatWithAMissingJobReadsVacant(t *testing.T) {
	e, _, sy := scheduleEnv(t)
	x, err := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: "desk", Kind: jobstore.SeatKindACP, Agent: "acp"})
	if err != nil {
		t.Fatal(err)
	}
	// DeleteJob would clear the seat; a row written before that rule existed (or removed behind
	// gofer's back) leaves a current_ref naming nothing, which is what this test reproduces: the
	// store's bind does not require the job to exist.
	if _, err := e.st.BindSeat(x.ID, "job-gone", false, "user"); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.Get(x.ID)
	if err != nil {
		t.Fatal(err)
	}
	st, err := e.svc.Status("u1", got)
	if err != nil || st.State != StateNone {
		t.Fatalf("a seat whose job is gone reads %q (err %v), want %q", st.State, err, StateNone)
	}
	plan, err := e.svc.OpenPlan(x.ID)
	if err != nil || plan.Reason != ReasonVacant {
		t.Fatalf("open plan = %+v, %v", plan, err)
	}
	said := len(sy.said)
	res, err := e.svc.DeliverToSeat(context.Background(), deliverReq(x.ID))
	if err != nil || res.Outcome != jobstore.ScheduleResultOffline {
		t.Fatalf("delivery = %+v, %v", res, err)
	}
	if len(sy.said) != said || len(e.rs.calls) != 0 {
		t.Fatalf("nothing may be said or restarted for a gone job: said=%+v restarts=%+v", sy.said, e.rs.calls)
	}
}

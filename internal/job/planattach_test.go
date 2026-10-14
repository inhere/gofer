package job

import (
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// seedPlan inserts one plan row that the attach tests bind jobs to (attaching is
// only meaningful against a plan that exists).
func seedPlan(t *testing.T, s *Service, planID string) {
	t.Helper()
	plan := jobstore.Plan{PlanID: planID, Title: planID, Status: jobstore.PlanOpen, ProjectKey: "self", CreatedAt: 1, UpdatedAt: 1}
	if err := s.Meta().InsertPlan(plan); err != nil {
		t.Fatalf("insert plan %s: %v", planID, err)
	}
}

// TestPlanAttachReachesSnapshotAndSurvivesPersist (gofer-z414): `plan attach` used to
// UPDATE the row behind the Service's back. The row is a projection of the in-memory
// entry, so the job went on running "in no plan" for every in-process reader
// (Get/ListJobs serve the entry) and the job's own next persist — the finish write
// carries plan_id from the snapshot — wrote the attachment straight back out.
func TestPlanAttachReachesSnapshotAndSurvivesPersist(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newTestService(t, root)
	seedPlan(t, s, "plan-attach")

	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "sleep", "2s"), Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForStatus(t, s, res.ID, StatusRunning, 5*time.Second)

	if ok, err := s.AttachJobToPlan(res.ID, "plan-attach"); err != nil || !ok {
		t.Fatalf("AttachJobToPlan: ok=%v err=%v", ok, err)
	}
	if got, ok := s.Get(res.ID); !ok || got.PlanID != "plan-attach" {
		t.Fatalf("snapshot plan_id = %q (found=%v), want plan-attach", got.PlanID, ok)
	}
	if rec, ok, err := s.Meta().GetJob(res.ID); err != nil || !ok || rec.PlanID != "plan-attach" {
		t.Fatalf("stored plan_id = %q (found=%v err=%v), want plan-attach", rec.PlanID, ok, err)
	}

	// The job's own later writes must keep the attachment: cancel → terminal persist.
	_ = s.Cancel(res.ID)
	final, ok := s.Wait(res.ID)
	if !ok || !IsTerminal(final.Status) {
		t.Fatalf("source did not reach a terminal state: %+v", final)
	}
	if final.PlanID != "plan-attach" {
		t.Fatalf("terminal snapshot plan_id = %q, want plan-attach", final.PlanID)
	}
	if rec, _, err := s.Meta().GetJob(res.ID); err != nil || rec.PlanID != "plan-attach" {
		t.Fatalf("stored plan_id after finish = %q (err=%v), want plan-attach", rec.PlanID, err)
	}

	// Unknown job: nothing is tracked here, so the store decides (false → 404).
	if ok, err := s.AttachJobToPlan("no-such-job", "plan-attach"); err != nil || ok {
		t.Fatalf("attach of an unknown job = ok=%v err=%v, want false/nil", ok, err)
	}
}

// TestResumeInheritsAttachedPlan (gofer-z414 requirement 1): a plan bound with
// `plan attach` — not passed at submit — reaches the continuation too.
func TestResumeInheritsAttachedPlan(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const sid = "sess-attach-plan"
	s := newResumeRunnableService(t, root, "codex")
	seedPlan(t, s, "plan-late")

	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "remember 42", Cwd: ".", TimeoutSec: 30, SessionID: sid,
	})
	if src.PlanID != "" {
		t.Fatalf("setup: source plan = %q, want none (it is attached afterwards)", src.PlanID)
	}
	if ok, err := s.AttachJobToPlan(src.ID, "plan-late"); err != nil || !ok {
		t.Fatalf("AttachJobToPlan: ok=%v err=%v", ok, err)
	}

	newJob, err := s.ResumeJob(src.ID, "what number", "", "caller-late")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(newJob.ID); s.Wait(newJob.ID) })

	if newJob.PlanID != "plan-late" {
		t.Fatalf("continuation plan_id = %q, want the attached plan-late", newJob.PlanID)
	}
}

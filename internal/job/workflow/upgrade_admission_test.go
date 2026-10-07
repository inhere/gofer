package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestUpgradeAdmissionPreservesPendingWorkflowStep(t *testing.T) {
	e, jobs := newTestEngineWithService(t, t.TempDir())
	spec := Spec{Steps: []StepSpec{echoStep("work after upgrade")}}
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	wf := jobstore.Workflow{ID: "upgrade-gated-wf", Status: jobstore.WorkflowRunning, CurrentStep: 1,
		StepAttempt: 1, TotalSteps: 1, SpecJSON: string(encoded), CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}
	if err := e.meta.InsertWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	if err := jobs.CloseUpgradeAdmission(context.Background(), "upgrade-workflow"); err != nil {
		t.Fatal(err)
	}
	e.Advance(wf.ID)
	got, ok, err := e.meta.GetWorkflow(wf.ID)
	if err != nil || !ok || got.Status != jobstore.WorkflowRunning || got.CurrentStep != 1 {
		t.Fatalf("paused workflow changed: %+v, %v", got, err)
	}
	if children, err := e.meta.ListWorkflowJobs(wf.ID); err != nil || len(children) != 0 {
		t.Fatalf("paused workflow started jobs: %d, %v", len(children), err)
	}
	if err := jobs.OpenUpgradeAdmission("upgrade-workflow"); err != nil {
		t.Fatal(err)
	}
	e.Advance(wf.ID)
	if children, err := e.meta.ListWorkflowJobs(wf.ID); err != nil || len(children) != 1 {
		t.Fatalf("workflow did not resume: %d, %v", len(children), err)
	}
}

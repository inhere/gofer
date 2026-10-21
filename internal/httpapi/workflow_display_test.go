package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
)

// TestWorkflowStepRunLocationAndJobFilter pins gofer-plkx at the HTTP boundary: each
// detail step row says where / when it ran (cwd, started_at, finished_at), and
// GET /v1/jobs?workflow_id= lists exactly that workflow's step-jobs — the filter the CLI
// (`job list --wf`) and the web board's workflow box use.
func TestWorkflowStepRunLocationAndJobFilter(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)

	resp := do(t, s, http.MethodPost, "/v1/workflows", testToken, workflow.Spec{
		Title: "loc",
		Steps: []workflow.StepSpec{echoStep("a"), echoStep("b")},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created struct {
		ID string `json:"id"`
	}
	decode(t, resp, &created)
	if created.ID == "" {
		t.Fatal("create returned empty workflow id")
	}
	waitWorkflowStatus(t, s, created.ID, "done")

	resp = do(t, s, http.MethodGet, "/v1/workflows/"+created.ID, testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status=%d, want 200", resp.StatusCode)
	}
	var detail struct {
		Steps []struct {
			StepIndex  int    `json:"step_index"`
			Cwd        string `json:"cwd"`
			StartedAt  int64  `json:"started_at"`
			FinishedAt int64  `json:"finished_at"`
		} `json:"steps"`
	}
	decode(t, resp, &detail)
	if len(detail.Steps) != 2 {
		t.Fatalf("detail steps = %d, want 2: %+v", len(detail.Steps), detail.Steps)
	}
	for _, st := range detail.Steps {
		if st.Cwd == "" {
			t.Fatalf("step %d cwd is empty, want the directory it ran in", st.StepIndex)
		}
		if st.StartedAt <= 0 {
			t.Fatalf("step %d started_at = %d, want a unix timestamp", st.StepIndex, st.StartedAt)
		}
		if st.FinishedAt < st.StartedAt {
			t.Fatalf("step %d finished_at = %d before started_at = %d", st.StepIndex, st.FinishedAt, st.StartedAt)
		}
	}

	resp = do(t, s, http.MethodGet, "/v1/jobs?workflow_id="+created.ID, testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list jobs status=%d, want 200", resp.StatusCode)
	}
	var listed struct {
		Jobs []job.JobResult `json:"jobs"`
	}
	decode(t, resp, &listed)
	if len(listed.Jobs) != 2 {
		t.Fatalf("jobs = %d, want the workflow's 2 step-jobs: %+v", len(listed.Jobs), listed.Jobs)
	}
	steps := map[int]bool{}
	for _, j := range listed.Jobs {
		if j.WorkflowID != created.ID {
			t.Fatalf("job %s workflow_id = %q, want %q", j.ID, j.WorkflowID, created.ID)
		}
		steps[j.StepIndex] = true
	}
	if !steps[1] || !steps[2] {
		t.Fatalf("step indexes = %v, want 1 and 2", steps)
	}

	// A workflow id nobody has lists nothing (the filter is exact, not a prefix match).
	resp = do(t, s, http.MethodGet, "/v1/jobs?workflow_id=wf-none", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list jobs (unknown wf) status=%d, want 200", resp.StatusCode)
	}
	listed.Jobs = nil
	decode(t, resp, &listed)
	if len(listed.Jobs) != 0 {
		t.Fatalf("jobs for an unknown workflow = %+v, want none", listed.Jobs)
	}
}

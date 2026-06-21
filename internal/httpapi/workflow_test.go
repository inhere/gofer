package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
)

// echoStep builds a fast exec step for the "self" project (matches newTestServer).
func echoStep(name string) job.StepSpec {
	return job.StepSpec{
		Name: name, ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "echo " + name}, Cwd: ".", TimeoutSec: 30,
	}
}

// TestCreateWorkflow asserts POST /v1/workflows starts a workflow (200 + running
// header with step 1 active), and GET /v1/workflows/{id} carries the step list.
func TestCreateWorkflow(t *testing.T) {
	s := newTestServer(t, testToken, false)

	resp := do(t, s, http.MethodPost, "/v1/workflows", testToken, job.WorkflowSpec{
		Title: "chain",
		Steps: []job.StepSpec{echoStep("a"), echoStep("b"), echoStep("c")},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		CurrentStep int    `json:"current_step"`
		TotalSteps  int    `json:"total_steps"`
		CallerID    string `json:"caller_id"`
	}
	decode(t, resp, &created)
	if created.ID == "" {
		t.Fatal("create returned empty workflow id")
	}
	if created.Status != "running" || created.CurrentStep != 1 || created.TotalSteps != 3 {
		t.Fatalf("created = %+v, want running/1/3", created)
	}
	if created.CallerID != "default" {
		t.Fatalf("caller_id = %q, want default (server-stamped)", created.CallerID)
	}

	// GET detail includes the step chain (at least step 1 started).
	resp = do(t, s, http.MethodGet, "/v1/workflows/"+created.ID, testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status=%d, want 200", resp.StatusCode)
	}
	var detail struct {
		ID    string `json:"id"`
		Steps []struct {
			StepIndex int    `json:"step_index"`
			JobID     string `json:"job_id"`
			Status    string `json:"status"`
		} `json:"steps"`
	}
	decode(t, resp, &detail)
	if detail.ID != created.ID {
		t.Fatalf("detail id = %q, want %q", detail.ID, created.ID)
	}
	if len(detail.Steps) < 1 || detail.Steps[0].StepIndex != 1 || detail.Steps[0].JobID == "" {
		t.Fatalf("detail steps = %+v, want step 1 with a job id", detail.Steps)
	}
}

// TestListWorkflowsStatusFilter asserts GET /v1/workflows?status= filters.
func TestListWorkflowsStatusFilter(t *testing.T) {
	s := newTestServer(t, testToken, false)

	// Start a workflow and let it run to completion.
	resp := do(t, s, http.MethodPost, "/v1/workflows", testToken, job.WorkflowSpec{
		Steps: []job.StepSpec{echoStep("one"), echoStep("two")},
	})
	var created struct {
		ID string `json:"id"`
	}
	decode(t, resp, &created)
	waitWorkflowStatus(t, s, created.ID, "done")

	// Unfiltered list contains it.
	all := listWorkflows(t, s, "")
	if !containsWorkflow(all, created.ID) {
		t.Fatalf("unfiltered list missing %s", created.ID)
	}
	// status=done contains it; status=running does not.
	done := listWorkflows(t, s, "done")
	if !containsWorkflow(done, created.ID) {
		t.Fatalf("status=done list missing %s", created.ID)
	}
	running := listWorkflows(t, s, "running")
	if containsWorkflow(running, created.ID) {
		t.Fatalf("status=running list should not contain done workflow %s", created.ID)
	}
}

// TestCancelWorkflowAPI asserts POST /v1/workflows/{id}/cancel marks cancelled.
func TestCancelWorkflowAPI(t *testing.T) {
	s := newTestServer(t, testToken, false)

	// Step 1 sleeps so the workflow stays running when we cancel.
	sleepStep := job.StepSpec{
		Name: "sleep1", ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "10"}, Cwd: ".", TimeoutSec: 30,
	}
	resp := do(t, s, http.MethodPost, "/v1/workflows", testToken, job.WorkflowSpec{
		Steps: []job.StepSpec{sleepStep, echoStep("two")},
	})
	var created struct {
		ID string `json:"id"`
	}
	decode(t, resp, &created)

	resp = do(t, s, http.MethodPost, "/v1/workflows/"+created.ID+"/cancel", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status=%d, want 200", resp.StatusCode)
	}
	var cancelled struct {
		Status string `json:"status"`
	}
	decode(t, resp, &cancelled)
	if cancelled.Status != "cancelled" {
		t.Fatalf("status after cancel = %q, want cancelled", cancelled.Status)
	}
	// Let the cancelled step-1 job drain before teardown.
	waitWorkflowStatus(t, s, created.ID, "cancelled")
}

// TestGetUnknownWorkflow404 asserts an unknown id is a 404.
func TestGetUnknownWorkflow404(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/v1/workflows/nope", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}

// TestCreateWorkflowInvalidSpec400 asserts a spec with an invalid step is a 400.
func TestCreateWorkflowInvalidSpec400(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/workflows", testToken, job.WorkflowSpec{
		Steps: []job.StepSpec{{Name: "bad", ProjectKey: "self", Agent: "exec", Runner: ""}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", resp.StatusCode)
	}
}

// TestCreateWorkflowEmptySteps400 asserts an empty spec is a 400.
func TestCreateWorkflowEmptySteps400(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/workflows", testToken, job.WorkflowSpec{Steps: nil})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", resp.StatusCode)
	}
}

// waitWorkflowStatus polls GET /v1/workflows/{id} until its status matches want
// or the deadline elapses.
func waitWorkflowStatus(t *testing.T, s *Server, id, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp := do(t, s, http.MethodGet, "/v1/workflows/"+id, testToken, nil)
		var d struct {
			Status string `json:"status"`
		}
		decode(t, resp, &d)
		if d.Status == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("workflow %s did not reach status %q in time", id, want)
}

// listWorkflows GETs /v1/workflows with an optional status filter and returns ids.
func listWorkflows(t *testing.T, s *Server, status string) []string {
	t.Helper()
	path := "/v1/workflows"
	if status != "" {
		path += "?status=" + status
	}
	resp := do(t, s, http.MethodGet, path, testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Workflows []struct {
			ID string `json:"id"`
		} `json:"workflows"`
	}
	decode(t, resp, &body)
	ids := make([]string, 0, len(body.Workflows))
	for _, w := range body.Workflows {
		ids = append(ids, w.ID)
	}
	return ids
}

func containsWorkflow(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

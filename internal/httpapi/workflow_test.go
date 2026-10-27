package httpapi

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// echoStep builds a fast exec step for the "self" project (matches newTestServer).
func echoStep(name string) workflow.StepSpec {
	return workflow.StepSpec{
		Name: name, ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "echo " + name}, Cwd: ".", TimeoutSec: 30,
	}
}

// TestCreateWorkflow asserts POST /v1/workflows starts a workflow (200 + running
// header with step 1 active), and GET /v1/workflows/{id} carries the step list.
func TestCreateWorkflow(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)

	resp := do(t, s, http.MethodPost, "/v1/workflows", testToken, workflow.Spec{
		Title: "chain",
		Steps: []workflow.StepSpec{echoStep("a"), echoStep("b"), echoStep("c")},
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
	t.Parallel()
	s := newTestServer(t, testToken, false)

	// Start a workflow and let it run to completion.
	resp := do(t, s, http.MethodPost, "/v1/workflows", testToken, workflow.Spec{
		Steps: []workflow.StepSpec{echoStep("one"), echoStep("two")},
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
	t.Parallel()
	s := newTestServer(t, testToken, false)

	// Step 1 sleeps so the workflow stays running when we cancel.
	sleepStep := workflow.StepSpec{
		Name: "sleep1", ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{testcmd.Path(t), "sleep", "10s"}, Cwd: ".", TimeoutSec: 30,
	}
	resp := do(t, s, http.MethodPost, "/v1/workflows", testToken, workflow.Spec{
		Steps: []workflow.StepSpec{sleepStep, echoStep("two")},
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
	// The workflow reads cancelled at once (the cancel is synchronous); its step-1 job
	// is still unwinding. Wait for that job itself, not for the workflow status.
	resp = do(t, s, http.MethodGet, "/v1/workflows/"+created.ID, testToken, nil)
	var detail struct {
		Steps []struct {
			JobID string `json:"job_id"`
		} `json:"steps"`
	}
	decode(t, resp, &detail)
	if len(detail.Steps) < 1 || detail.Steps[0].JobID == "" {
		t.Fatalf("detail steps = %+v, want step 1 with a job id", detail.Steps)
	}
	if final, ok := s.jobs.Wait(detail.Steps[0].JobID); !ok || final.Status != "cancelled" {
		t.Fatalf("step-1 job after the workflow cancel = %+v (ok=%v), want cancelled", final, ok)
	}
}

// TestGetUnknownWorkflow404 asserts an unknown id is a 404.
func TestGetUnknownWorkflow404(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/v1/workflows/nope", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}

// TestCreateWorkflowInvalidSpec400 asserts a spec with an invalid step is a 400.
func TestCreateWorkflowInvalidSpec400(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/workflows", testToken, workflow.Spec{
		Steps: []workflow.StepSpec{{Name: "bad", ProjectKey: "self", Agent: "exec", Runner: ""}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", resp.StatusCode)
	}
}

// TestCreateWorkflowEmptySteps400 asserts an empty spec is a 400.
func TestCreateWorkflowEmptySteps400(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/workflows", testToken, workflow.Spec{Steps: nil})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", resp.StatusCode)
	}
}

// TestCreateWorkflowBodyProject asserts the request body's `project` field is the
// workflow's default project (S1): a step that names no project_key is admitted and
// runs there, and an unknown project is rejected.
func TestCreateWorkflowBodyProject(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/workflows", testToken, map[string]any{
		"project": "self",
		"steps": []map[string]any{{
			"name": "a", "agent": "exec", "runner": "local",
			"cmd": testcmd.Cmd(t, "exit", "0"), "cwd": ".", "timeout_sec": 30,
		}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create with project default status=%d, want 200", resp.StatusCode)
	}
	var created struct {
		ID string `json:"id"`
	}
	decode(t, resp, &created)
	waitWorkflowStatus(t, s, created.ID, "done")

	// A project the caller can not reach is refused before anything starts.
	resp = do(t, s, http.MethodPost, "/v1/workflows", testToken, map[string]any{
		"project": "no-such-project",
		"steps":   []map[string]any{{"name": "a", "agent": "exec", "cmd": testcmd.Cmd(t, "exit", "0")}},
	})
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("create with an unknown project status=%d, want a rejection", resp.StatusCode)
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

// workflowPage GETs /v1/workflows with a raw query string and returns that page's ids plus
// the response's total.
func workflowPage(t *testing.T, s *Server, query string) ([]string, int) {
	t.Helper()
	return workflowPageToken(t, s, testToken, query)
}

// workflowPageToken is workflowPage for a fixture caller's token.
func workflowPageToken(t *testing.T, s *Server, token, query string) ([]string, int) {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/workflows"+query, token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list %q as %s status=%d, want 200", query, token, resp.StatusCode)
	}
	var body struct {
		Workflows []struct {
			ID string `json:"id"`
		} `json:"workflows"`
		Total int `json:"total"`
	}
	decode(t, resp, &body)
	ids := make([]string, 0, len(body.Workflows))
	for _, w := range body.Workflows {
		ids = append(ids, w.ID)
	}
	return ids, body.Total
}

// TestListWorkflowsPagination asserts GET /v1/workflows pages with limit / offset and reports
// total (the pager's denominator), while a request without limit still returns the whole list
// like before paging existed.
func TestListWorkflowsPagination(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	meta := s.jobs.Meta()
	now := time.Now().Unix()
	// Seeded oldest first, so the list order is wf-4 … wf-0 (created_at DESC).
	for i := 0; i < 5; i++ {
		wf := jobstore.Workflow{
			ID: fmt.Sprintf("wf-%d", i), Status: jobstore.WorkflowDone, CallerID: "default",
			CurrentStep: 1, TotalSteps: 1, SpecJSON: "{}",
			CreatedAt: now + int64(i), UpdatedAt: now + int64(i),
		}
		if err := meta.InsertWorkflow(wf); err != nil {
			t.Fatalf("seed workflow %s: %v", wf.ID, err)
		}
	}

	// Page 1 is full and total counts every matching row, not just the page.
	page1, total := workflowPage(t, s, "?limit=2")
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
	if len(page1) != 2 || page1[0] != "wf-4" || page1[1] != "wf-3" {
		t.Fatalf("page 1 = %v, want [wf-4 wf-3]", page1)
	}

	// The last (short) page continues where the previous one ended.
	page3, total := workflowPage(t, s, "?limit=2&offset=4")
	if total != 5 || len(page3) != 1 || page3[0] != "wf-0" {
		t.Fatalf("page 3 = %v (total %d), want [wf-0] (total 5)", page3, total)
	}

	// An offset past the end is an empty page with the same total.
	past, total := workflowPage(t, s, "?limit=2&offset=10")
	if total != 5 || len(past) != 0 {
		t.Fatalf("offset past the end = %v (total %d), want an empty page with total 5", past, total)
	}

	// Without limit the whole list still comes back, as before the paging params existed.
	all, total := workflowPage(t, s, "")
	if total != 5 || len(all) != 5 {
		t.Fatalf("unpaged list = %v (total %d), want all 5", all, total)
	}

	// The status filter narrows the page and the total together.
	done, total := workflowPage(t, s, "?status=done&limit=10")
	if total != 5 || len(done) != 5 {
		t.Fatalf("status=done = %v (total %d), want 5/5", done, total)
	}
	running, total := workflowPage(t, s, "?status=running&limit=10")
	if total != 0 || len(running) != 0 {
		t.Fatalf("status=running = %v (total %d), want an empty page with total 0", running, total)
	}
}

// TestListWorkflowsCallerFilter asserts the list's FILTER permission: a user-class caller
// without job.read.others sees only its own workflows, and the page is cut AFTER that filter
// (it does not fetch everyone's newest rows and drop the foreign ones afterwards).
func TestListWorkflowsCallerFilter(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "bob", Token: "tok-bob"},
		},
	})
	meta := s.jobs.Meta()
	// A plain user caller holds no permission, so job.read.others is absent and the list is
	// narrowed to its own rows (the FILTER form, not a 403).
	s.setDBGrants(grantsFor(nil))
	now := time.Now().Unix()
	seed := func(id, caller string, at int64) {
		wf := jobstore.Workflow{
			ID: id, Status: jobstore.WorkflowDone, CallerID: caller,
			CurrentStep: 1, TotalSteps: 1, SpecJSON: "{}", CreatedAt: at, UpdatedAt: at,
		}
		if err := meta.InsertWorkflow(wf); err != nil {
			t.Fatalf("seed workflow %s: %v", id, err)
		}
	}
	// bob's rows are the two newest overall, so an unrestricted first page would show them.
	seed("wf-bob-2", "bob", now+3)
	seed("wf-bob-1", "bob", now+2)
	seed("wf-alice-2", "alice", now+1)
	seed("wf-alice-1", "alice", now)

	page1, total := workflowPageToken(t, s, "tok-alice", "?limit=1")
	if total != 2 || len(page1) != 1 || page1[0] != "wf-alice-2" {
		t.Fatalf("alice page 1 = %v (total %d), want [wf-alice-2] with total 2 (her own only)", page1, total)
	}
	page2, total := workflowPageToken(t, s, "tok-alice", "?limit=1&offset=1")
	if total != 2 || len(page2) != 1 || page2[0] != "wf-alice-1" {
		t.Fatalf("alice page 2 = %v (total %d), want [wf-alice-1]", page2, total)
	}
	own, total := workflowPageToken(t, s, "tok-bob", "?limit=10")
	if total != 2 || len(own) != 2 || own[0] != "wf-bob-2" {
		t.Fatalf("bob's list = %v (total %d), want his two rows, newest first", own, total)
	}
}

// TestListWorkflowsCeilingProjectFilter asserts project ceilings are pushed into both the page
// query and COUNT query; rows from another project must not consume page slots or total.
func TestListWorkflowsCeilingProjectFilter(t *testing.T) {
	t.Parallel()
	s := newCeilingServer(t)
	grantAll(s)
	meta := s.jobs.Meta()
	now := time.Now().Unix()
	seed := func(id, project string, at int64) {
		if err := meta.InsertWorkflow(jobstore.Workflow{
			ID: id, Status: jobstore.WorkflowDone, CallerID: "bob", CurrentStep: 1, TotalSteps: 1,
			SpecJSON:  fmt.Sprintf(`{"steps":[{"project_key":%q,"agent":"exec"}]}`, project),
			CreatedAt: at, UpdatedAt: at,
		}); err != nil {
			t.Fatalf("seed workflow %s: %v", id, err)
		}
	}
	seed("wf-p1-2", "p1", now+1)
	seed("wf-p1-1", "p1", now)
	seed("wf-p2-3", "p2", now+4)
	seed("wf-p2-2", "p2", now+3)
	seed("wf-p2-1", "p2", now+2)

	page, total := workflowPageToken(t, s, ceilTokLim, "?limit=1")
	if total != 2 || len(page) != 1 || page[0] != "wf-p1-2" {
		t.Fatalf("limited workflow page = %v (total %d), want [wf-p1-2] (total 2)", page, total)
	}
	page, total = workflowPageToken(t, s, ceilTokLim, "?limit=1&offset=1")
	if total != 2 || len(page) != 1 || page[0] != "wf-p1-1" {
		t.Fatalf("limited workflow page 2 = %v (total %d), want [wf-p1-1] (total 2)", page, total)
	}
}

// TestRenderWorkflowTemplate asserts POST /v1/workflow-templates/{name}/render
// returns the executable spec without submitting anything, and rejects bad vars.
func TestRenderWorkflowTemplate(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)

	resp := do(t, s, http.MethodPost, "/v1/workflow-templates/compare/render", testToken, map[string]any{
		"vars": map[string]string{"project": "self", "task": "fix it", "agent_a": "exec", "agent_b": "exec"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("render status=%d, want 200", resp.StatusCode)
	}
	var spec workflow.Spec
	decode(t, resp, &spec)
	if len(spec.Steps) != 1 || spec.Steps[0].ProjectKey != "self" || spec.Steps[0].Prompt != "fix it" || len(spec.Steps[0].Agents) != 2 {
		t.Fatalf("rendered spec = %+v", spec)
	}
	// The preview carries the same default project the submit would use.
	if spec.Project != "self" {
		t.Fatalf("rendered spec project = %q, want self", spec.Project)
	}

	// Missing required variable -> 400.
	resp = do(t, s, http.MethodPost, "/v1/workflow-templates/compare/render", testToken, map[string]any{
		"vars": map[string]string{"project": "self"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing var status=%d, want 400", resp.StatusCode)
	}

	// Unknown template -> 400 (same sentinel as run).
	resp = do(t, s, http.MethodPost, "/v1/workflow-templates/nope/render", testToken, map[string]any{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown template status=%d, want 400", resp.StatusCode)
	}

	// Nothing was submitted.
	resp = do(t, s, http.MethodGet, "/v1/workflows", testToken, nil)
	var list struct {
		Workflows []any `json:"workflows"`
	}
	decode(t, resp, &list)
	if len(list.Workflows) != 0 {
		t.Fatalf("render submitted %d workflows", len(list.Workflows))
	}
}

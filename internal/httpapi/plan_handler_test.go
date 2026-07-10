package httpapi

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestCreateListGetPlanAndAttachJob(t *testing.T) {
	s := newTestServer(t, testToken, false)

	resp := do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]string{
		"plan_id": "plan-http", "title": "HTTP plan", "description": "desc",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create plan status=%d, want 200", resp.StatusCode)
	}
	var created struct {
		PlanID      string `json:"plan_id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Status      string `json:"status"`
		Owner       string `json:"owner"`
	}
	decode(t, resp, &created)
	if created.PlanID != "plan-http" || created.Title != "HTTP plan" || created.Description != "desc" {
		t.Fatalf("created plan mismatch: %+v", created)
	}
	if created.Status != "open" || created.Owner != "default" {
		t.Fatalf("created status/owner mismatch: %+v", created)
	}

	resp = do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]string{"title": "generated"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create generated plan status=%d, want 200", resp.StatusCode)
	}
	var generated struct {
		PlanID string `json:"plan_id"`
	}
	decode(t, resp, &generated)
	if generated.PlanID == "" || len(generated.PlanID) < len("plan-") || generated.PlanID[:5] != "plan-" {
		t.Fatalf("generated plan id = %q, want plan-*", generated.PlanID)
	}
	resp = do(t, s, http.MethodGet, "/v1/plans/"+generated.PlanID, testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get empty plan status=%d, want 200", resp.StatusCode)
	}
	var emptyDetail struct {
		Counts struct {
			Total   int `json:"total"`
			Queued  int `json:"queued"`
			Running int `json:"running"`
			Done    int `json:"done"`
			Failed  int `json:"failed"`
		} `json:"counts"`
		Jobs  []job.JobResult `json:"jobs"`
		Todos []struct {
			TodoID string `json:"todo_id"`
		} `json:"todos"`
	}
	decode(t, resp, &emptyDetail)
	if emptyDetail.Counts.Total != 0 || emptyDetail.Counts.Queued != 0 ||
		emptyDetail.Counts.Running != 0 || emptyDetail.Counts.Done != 0 || emptyDetail.Counts.Failed != 0 {
		t.Fatalf("empty plan counts mismatch: %+v", emptyDetail.Counts)
	}
	if emptyDetail.Todos == nil || len(emptyDetail.Todos) != 0 {
		t.Fatalf("empty plan todos should be non-nil empty array: %+v", emptyDetail.Todos)
	}

	jobID := createExec(t, s, []string{"go", "version"})
	waitDone(t, s, jobID)

	resp = do(t, s, http.MethodPost, "/v1/plans/plan-http/jobs", testToken, map[string]string{"job_id": jobID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("attach status=%d, want 200", resp.StatusCode)
	}

	resp = do(t, s, http.MethodGet, "/v1/plans/plan-http", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get plan status=%d, want 200", resp.StatusCode)
	}
	var detail struct {
		PlanID string `json:"plan_id"`
		Counts struct {
			Total   int `json:"total"`
			Queued  int `json:"queued"`
			Running int `json:"running"`
			Done    int `json:"done"`
			Failed  int `json:"failed"`
		} `json:"counts"`
		Jobs []job.JobResult `json:"jobs"`
	}
	decode(t, resp, &detail)
	if detail.PlanID != "plan-http" {
		t.Fatalf("detail plan id = %q", detail.PlanID)
	}
	if detail.Counts.Total != 1 || detail.Counts.Done != 1 || detail.Counts.Failed != 0 {
		t.Fatalf("detail counts mismatch: %+v", detail.Counts)
	}
	if len(detail.Jobs) != 1 || detail.Jobs[0].ID != jobID || detail.Jobs[0].PlanID != "plan-http" {
		t.Fatalf("detail jobs mismatch: %+v", detail.Jobs)
	}

	resp = do(t, s, http.MethodGet, "/v1/plans?status=open", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list plans status=%d, want 200", resp.StatusCode)
	}
	var list struct {
		Plans []struct {
			PlanID string `json:"plan_id"`
		} `json:"plans"`
	}
	decode(t, resp, &list)
	found := false
	for _, p := range list.Plans {
		if p.PlanID == "plan-http" {
			found = true
		}
	}
	if !found {
		t.Fatalf("plan-http missing from list: %+v", list.Plans)
	}
}

func TestPlanAPIErrorCases(t *testing.T) {
	s := newTestServer(t, testToken, false)

	resp := do(t, s, http.MethodGet, "/v1/plans/missing", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown plan status=%d, want 404", resp.StatusCode)
	}

	resp = do(t, s, http.MethodPost, "/v1/plans/ghost/jobs", testToken, map[string]string{"job_id": "job-1"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("attach unknown plan status=%d, want 404", resp.StatusCode)
	}

	resp = do(t, s, http.MethodPost, "/v1/plans/ghost/todos", testToken, map[string]string{"title": "todo"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("add todo unknown plan status=%d, want 404", resp.StatusCode)
	}

	resp = do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]string{"plan_id": "plan-errors"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &struct{}{})

	resp = do(t, s, http.MethodPost, "/v1/plans/plan-errors/jobs", testToken, map[string]string{"job_id": "missing"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("attach unknown job status=%d, want 404", resp.StatusCode)
	}

	resp = do(t, s, http.MethodPost, "/v1/plans/plan-errors/jobs", testToken, map[string]string{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("attach missing job_id status=%d, want 400", resp.StatusCode)
	}

	resp = do(t, s, http.MethodPost, "/v1/plans/plan-errors/todos", testToken, map[string]string{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("add todo missing title status=%d, want 400", resp.StatusCode)
	}

	resp = do(t, s, http.MethodPatch, "/v1/todos/missing", testToken, map[string]bool{"done": true})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("update unknown todo status=%d, want 404", resp.StatusCode)
	}
}

func TestPlanTodoAPI(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]string{"plan_id": "plan-todos"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create plan status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &struct{}{})

	resp = do(t, s, http.MethodPost, "/v1/plans/plan-todos/todos", testToken, map[string]any{
		"title": "plain todo", "sort": 20,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add plain todo status=%d, want 200", resp.StatusCode)
	}
	var plain struct {
		TodoID string `json:"todo_id"`
		PlanID string `json:"plan_id"`
		JobID  string `json:"job_id"`
		Title  string `json:"title"`
		Done   bool   `json:"done"`
		Sort   int    `json:"sort"`
	}
	decode(t, resp, &plain)
	if plain.TodoID == "" || plain.PlanID != "plan-todos" || plain.JobID != "" ||
		plain.Title != "plain todo" || plain.Done || plain.Sort != 20 {
		t.Fatalf("plain todo mismatch: %+v", plain)
	}

	resp = do(t, s, http.MethodPost, "/v1/plans/plan-todos/todos", testToken, map[string]any{
		"title": "bound todo", "job_id": "job-bound", "sort": 10,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add bound todo status=%d, want 200", resp.StatusCode)
	}
	var bound struct {
		TodoID string `json:"todo_id"`
		JobID  string `json:"job_id"`
	}
	decode(t, resp, &bound)
	if bound.TodoID == "" || bound.JobID != "job-bound" {
		t.Fatalf("bound todo mismatch: %+v", bound)
	}

	resp = do(t, s, http.MethodPatch, "/v1/todos/"+plain.TodoID, testToken, map[string]bool{"done": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set todo done status=%d, want 200", resp.StatusCode)
	}
	var updated struct {
		TodoID string `json:"todo_id"`
		Done   bool   `json:"done"`
	}
	decode(t, resp, &updated)
	if updated.TodoID != plain.TodoID || !updated.Done {
		t.Fatalf("updated todo mismatch: %+v", updated)
	}

	resp = do(t, s, http.MethodPatch, "/v1/todos/"+plain.TodoID, testToken, map[string]bool{"done": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unset todo done status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &updated)
	if updated.Done {
		t.Fatalf("updated todo done should be false: %+v", updated)
	}

	resp = do(t, s, http.MethodGet, "/v1/plans/plan-todos", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get plan status=%d, want 200", resp.StatusCode)
	}
	var detail struct {
		Todos []struct {
			TodoID string `json:"todo_id"`
			JobID  string `json:"job_id"`
			Title  string `json:"title"`
			Done   bool   `json:"done"`
		} `json:"todos"`
	}
	decode(t, resp, &detail)
	if len(detail.Todos) != 2 || detail.Todos[0].TodoID != bound.TodoID || detail.Todos[1].TodoID != plain.TodoID {
		t.Fatalf("detail todos order mismatch: %+v", detail.Todos)
	}
	if detail.Todos[0].JobID != "job-bound" || detail.Todos[1].Done {
		t.Fatalf("detail todos fields mismatch: %+v", detail.Todos)
	}
}

func TestGetPlanCountsUseFullAggregateNotVisibleJobsLimit(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]string{"plan_id": "plan-many"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create plan status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &struct{}{})

	const total = 1005
	for i := 0; i < total; i++ {
		rec := jobstore.JobRecord{
			ID:         "many-job-" + strconv.Itoa(i),
			ProjectKey: "self",
			Agent:      "exec",
			Runner:     "local",
			Status:     job.StatusDone,
			ResultDir:  "/tmp/results/many-job-" + strconv.Itoa(i),
			StartedAt:  int64(i + 1),
			UpdatedAt:  int64(i + 1),
			EndedAt:    int64(i + 1),
			PlanID:     "plan-many",
		}
		if err := s.jobs.Meta().UpsertJob(rec); err != nil {
			t.Fatalf("upsert job %d: %v", i, err)
		}
	}

	resp = do(t, s, http.MethodGet, "/v1/plans/plan-many", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get plan status=%d, want 200", resp.StatusCode)
	}
	var detail struct {
		Counts struct {
			Total int `json:"total"`
			Done  int `json:"done"`
		} `json:"counts"`
		Jobs []job.JobResult `json:"jobs"`
	}
	decode(t, resp, &detail)
	if len(detail.Jobs) != 1000 {
		t.Fatalf("visible jobs len=%d, want 1000", len(detail.Jobs))
	}
	if detail.Counts.Total != total || detail.Counts.Done != total {
		t.Fatalf("counts should be full aggregate despite jobs limit: %+v", detail.Counts)
	}
}

func TestJobListPlanQueryAndSubmitPlanID(t *testing.T) {
	s := newTestServer(t, testToken, false)

	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		PlanID: "plan-query", Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create job status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	if created.PlanID != "plan-query" {
		t.Fatalf("created PlanID = %q, want plan-query", created.PlanID)
	}
	waitDone(t, s, created.ID)

	jobs := listJobs(t, s, "?plan=plan-query")
	if len(jobs) != 1 || jobs[0].ID != created.ID || jobs[0].PlanID != "plan-query" {
		t.Fatalf("plan query jobs mismatch: %+v", jobs)
	}
	none := listJobs(t, s, "?plan=plan-other")
	if len(none) != 0 {
		t.Fatalf("plan-other expected 0, got %+v", none)
	}
}

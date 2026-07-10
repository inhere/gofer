package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
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
		PlanID string          `json:"plan_id"`
		Jobs   []job.JobResult `json:"jobs"`
	}
	decode(t, resp, &detail)
	if detail.PlanID != "plan-http" {
		t.Fatalf("detail plan id = %q", detail.PlanID)
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

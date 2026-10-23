package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// planCompletionBody is the part of the plan list/detail payload these tests read.
type planCompletionBody struct {
	PlanID     string `json:"plan_id"`
	TodoCounts struct {
		Total, Done, Doing, Skipped, Pending int
	} `json:"todo_counts"`
	Completion struct {
		Basis   string `json:"basis"`
		Done    int    `json:"done"`
		Total   int    `json:"total"`
		Percent *int   `json:"percent"`
	} `json:"completion"`
}

// TestPlanListReviewAndFailureCounts: the plan list carries the needs-review and failed
// job figures of each plan (gofer-rj5e), so the overview can draw its 待验收 segment and
// blocker column without fetching every plan's detail.
func TestPlanListReviewAndFailureCounts(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	const planID = "plan-review-counts"
	resp := do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]string{"plan_id": planID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create plan status=%d", resp.StatusCode)
	}
	resp.Body.Close()
	now := time.Now().Unix()
	seed := func(id, status string) {
		t.Helper()
		if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
			ID: id, ProjectKey: "default", PlanID: planID, Agent: "claude", Runner: "local",
			Status: status, Cwd: ".", ResultDir: t.TempDir(), StartedAt: now, EndedAt: now,
			UpdatedAt: now, CallerID: "default",
		}); err != nil {
			t.Fatalf("seed job %s: %v", id, err)
		}
	}
	seed("job-review-1", "needs_review")
	seed("job-review-2", "needs_review")
	seed("job-failed-1", "failed")
	seed("job-timeout-1", "timeout")
	seed("job-done-1", "done")

	resp = do(t, s, http.MethodGet, "/v1/plans", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list plans status=%d", resp.StatusCode)
	}
	var list struct {
		Plans []struct {
			PlanID      string `json:"plan_id"`
			NeedsReview int    `json:"needs_review"`
			FailedJobs  int    `json:"failed_jobs"`
			Counts      struct {
				Total, Done, Failed int
			} `json:"counts"`
		} `json:"plans"`
	}
	decode(t, resp, &list)
	if len(list.Plans) != 1 {
		t.Fatalf("list plans = %d rows, want 1", len(list.Plans))
	}
	got := list.Plans[0]
	if got.PlanID != planID || got.NeedsReview != 2 || got.FailedJobs != 2 {
		t.Fatalf("list row = %+v, want plan %s with needs_review 2 and failed_jobs 2", got, planID)
	}
	if got.Counts.Total != 5 || got.Counts.Done != 1 || got.Counts.Failed != 2 {
		t.Fatalf("list counts = %+v, want total 5 / done 1 / failed 2", got.Counts)
	}

	// The detail carries the same two figures.
	resp = do(t, s, http.MethodGet, "/v1/plans/"+planID, testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get plan status=%d", resp.StatusCode)
	}
	var detail struct {
		NeedsReview int `json:"needs_review"`
		FailedJobs  int `json:"failed_jobs"`
	}
	decode(t, resp, &detail)
	if detail.NeedsReview != 2 || detail.FailedJobs != 2 {
		t.Fatalf("detail = %+v, want needs_review 2 and failed_jobs 2", detail)
	}
}

func TestPlanCompletionAPI(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	mustOK := func(resp *http.Response, what string) {
		t.Helper()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d", what, resp.StatusCode)
		}
	}
	const todosPlan, emptyPlan = "plan-progress-todos", "plan-progress-empty"
	for _, id := range []string{todosPlan, emptyPlan} {
		resp := do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]string{"plan_id": id})
		mustOK(resp, "create "+id)
		resp.Body.Close()
	}
	// A todo-only plan (no jobs at all): 3 todos, one done, one skipped.
	var todoIDs []string
	for _, title := range []string{"a", "b", "c"} {
		resp := do(t, s, http.MethodPost, "/v1/plans/"+todosPlan+"/todos", testToken, map[string]string{"title": title})
		mustOK(resp, "add todo")
		var td struct {
			TodoID string `json:"todo_id"`
		}
		decode(t, resp, &td)
		todoIDs = append(todoIDs, td.TodoID)
	}
	for i, st := range []string{"done", "skipped"} {
		resp := do(t, s, http.MethodPatch, "/v1/todos/"+todoIDs[i], testToken, map[string]string{"status": st})
		mustOK(resp, "set todo "+st)
		resp.Body.Close()
	}

	resp := do(t, s, http.MethodGet, "/v1/plans/"+todosPlan, testToken, nil)
	mustOK(resp, "get plan")
	var detail planCompletionBody
	decode(t, resp, &detail)
	if c := detail.Completion; c.Basis != "todos" || c.Done != 2 || c.Total != 3 || c.Percent == nil || *c.Percent != 67 {
		t.Fatalf("detail completion = %+v, want todos 2/3 (67%%)", c)
	}
	if tc := detail.TodoCounts; tc.Total != 3 || tc.Done != 1 || tc.Skipped != 1 || tc.Pending != 1 {
		t.Fatalf("detail todo_counts = %+v", tc)
	}

	resp = do(t, s, http.MethodGet, "/v1/plans", testToken, nil)
	mustOK(resp, "list plans")
	var list struct {
		Plans []planCompletionBody `json:"plans"`
	}
	decode(t, resp, &list)
	found := map[string]planCompletionBody{}
	for _, p := range list.Plans {
		found[p.PlanID] = p
	}
	if c := found[todosPlan].Completion; c.Basis != "todos" || c.Done != 2 || c.Total != 3 {
		t.Fatalf("list completion for todo-only plan = %+v, want todos 2/3", c)
	}
	if c := found[emptyPlan].Completion; c.Basis != "none" || c.Percent != nil {
		t.Fatalf("empty plan completion = %+v, want basis none with no percent", c)
	}
}

package httpapi

import (
	"net/http"
	"testing"
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

func TestPlanCompletionAPI(t *testing.T) {
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

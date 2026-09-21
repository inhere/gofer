package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// planActionView is the response of the PLAN-03 plan actions: the plan header, which is
// where status/paused/blocked_todo live.
type planActionView struct {
	PlanID      string `json:"plan_id"`
	Status      string `json:"status"`
	Paused      bool   `json:"paused"`
	BlockedTodo string `json:"blocked_todo"`
}

// TestPlanRunPauseResumeEndpoints (PLAN-03): the three chain controls are HTTP
// endpoints — run starts the ready work and releases a pause/block, pause holds the
// automatic advance, resume releases it — and an unknown plan is a 404 on all three.
func TestPlanRunPauseResumeEndpoints(t *testing.T) {
	s := newTestServer(t, testToken, false)
	bin := testcmd.Path(t)

	resp := do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]string{
		"plan_id": "plan-run-http", "title": "chain", "project": "self",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create plan status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// A root and a dependent item, both exec steps (the project allows exec), so the
	// chain really runs without needing an agent CLI.
	add := func(title string, after []string) string {
		body := map[string]any{
			"title": title, "assignee": "exec",
			"cmd": []string{bin, "argv", title},
		}
		if len(after) > 0 {
			body["after"] = after
		}
		r := do(t, s, http.MethodPost, "/v1/plans/plan-run-http/todos", testToken, body)
		if r.StatusCode != http.StatusOK {
			t.Fatalf("add todo %q status=%d, want 200", title, r.StatusCode)
		}
		var tv todoView
		decode(t, r, &tv)
		if tv.Auto != true {
			t.Fatalf("todo %q auto = %v, want true by default", title, tv.Auto)
		}
		return tv.TodoID
	}
	first := add("first", nil)
	second := add("second", []string{first})

	// Pause first: `run` releases it, so the order proves both halves.
	r := do(t, s, http.MethodPost, "/v1/plans/plan-run-http/pause", testToken, nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("pause status=%d, want 200", r.StatusCode)
	}
	var paused planActionView
	decode(t, r, &paused)
	if !paused.Paused || paused.Status != jobstore.PlanOpen {
		t.Fatalf("paused plan = %+v, want paused+open", paused)
	}

	r = do(t, s, http.MethodPost, "/v1/plans/plan-run-http/run", testToken, nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("run status=%d, want 200", r.StatusCode)
	}
	var ran planActionView
	decode(t, r, &ran)
	if ran.Paused {
		t.Fatalf("run left the plan paused: %+v", ran)
	}
	if ran.Status != jobstore.PlanOpen {
		t.Fatalf("run status = %q, want open (the chain is running, not done)", ran.Status)
	}

	// The chain walks itself: the dependent item only starts after the root is done.
	deadline := time.Now().Add(30 * time.Second)
	var detail struct {
		Status string `json:"status"`
		Todos  []struct {
			TodoID string `json:"todo_id"`
			Status string `json:"status"`
		} `json:"todos"`
	}
	for time.Now().Before(deadline) {
		got := do(t, s, http.MethodGet, "/v1/plans/plan-run-http", testToken, nil)
		if got.StatusCode != http.StatusOK {
			t.Fatalf("get plan status=%d, want 200", got.StatusCode)
		}
		decode(t, got, &detail)
		if detail.Status == jobstore.PlanDone {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if detail.Status != jobstore.PlanDone {
		t.Fatalf("plan status = %q, want done (todos=%+v)", detail.Status, detail.Todos)
	}
	for _, td := range detail.Todos {
		if td.Status != jobstore.TodoDone {
			t.Fatalf("todo %s status = %q, want done", td.TodoID, td.Status)
		}
	}
	if len(detail.Todos) != 2 || detail.Todos[1].TodoID != second {
		t.Fatalf("plan todos = %+v, want the two seeded items", detail.Todos)
	}

	// resume is accepted on an un-paused plan too (idempotent) and answers with the
	// header.
	r = do(t, s, http.MethodPost, "/v1/plans/plan-run-http/resume", testToken, nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("resume status=%d, want 200", r.StatusCode)
	}
	var resumed planActionView
	decode(t, r, &resumed)
	if resumed.Paused || resumed.BlockedTodo != "" {
		t.Fatalf("resumed plan = %+v, want unpaused and unblocked", resumed)
	}

	// Unknown plans are 404 on every action.
	for _, action := range []string{"run", "pause", "resume"} {
		miss := do(t, s, http.MethodPost, "/v1/plans/plan-does-not-exist/"+action, testToken, nil)
		if miss.StatusCode != http.StatusNotFound {
			t.Fatalf("%s on an unknown plan status=%d, want 404", action, miss.StatusCode)
		}
		miss.Body.Close()
	}
}

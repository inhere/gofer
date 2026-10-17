package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

type continueTodoBody struct {
	TodoID       string   `json:"todo_id"`
	After        []string `json:"after"`
	ContinueFrom string   `json:"continue_from"`
	ContinuedBy  string   `json:"continued_by"`
	ContinueNote string   `json:"continue_note"`
}

func mkContinuePlan(t *testing.T, s *Server, id string) {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]string{"plan_id": id})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create plan %s status=%d", id, resp.StatusCode)
	}
	resp.Body.Close()
}

func addContinueTodo(t *testing.T, s *Server, plan, title, from string) continueTodoBody {
	t.Helper()
	body := map[string]any{"title": title}
	if from != "" {
		body["continue_from"] = from
	}
	resp := do(t, s, http.MethodPost, "/v1/plans/"+plan+"/todos", testToken, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add todo %q status=%d, want 200", title, resp.StatusCode)
	}
	var out continueTodoBody
	decode(t, resp, &out)
	return out
}

func errorText(t *testing.T, resp *http.Response) string {
	t.Helper()
	var e map[string]any
	decode(t, resp, &e)
	var sb strings.Builder
	for _, v := range e {
		if s, ok := v.(string); ok {
			sb.WriteString(s + " ")
		}
	}
	return sb.String()
}

// TestTodoContinueFromHTTP: continue_from is written on create and patch, the store adds
// the implied `after`, and the todo views (create answer and plan show) carry
// continue_from / continued_by / continue_note.
func TestTodoContinueFromHTTP(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	mkContinuePlan(t, s, "plan-cont")
	a := addContinueTodo(t, s, "plan-cont", "first", "")
	b := addContinueTodo(t, s, "plan-cont", "second", a.TodoID)
	if b.ContinueFrom != a.TodoID {
		t.Fatalf("create view continue_from = %q, want %q", b.ContinueFrom, a.TodoID)
	}
	if len(b.After) != 1 || b.After[0] != a.TodoID {
		t.Fatalf("create view after = %v, want [%s] (implied by continue_from)", b.After, a.TodoID)
	}

	meta := s.jobs.Meta()
	if ok, err := meta.ClaimTodoContinuation(a.TodoID, b.TodoID); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := meta.SetTodoContinueNote(b.TodoID, "predecessor had no session"); err != nil {
		t.Fatalf("note: %v", err)
	}
	resp := do(t, s, http.MethodGet, "/v1/plans/plan-cont", testToken, nil)
	var detail struct {
		Todos []continueTodoBody `json:"todos"`
	}
	decode(t, resp, &detail)
	got := map[string]continueTodoBody{}
	for _, td := range detail.Todos {
		got[td.TodoID] = td
	}
	if got[a.TodoID].ContinuedBy != b.TodoID {
		t.Fatalf("plan show: first.continued_by = %q, want %q", got[a.TodoID].ContinuedBy, b.TodoID)
	}
	if got[b.TodoID].ContinueFrom != a.TodoID || got[b.TodoID].ContinueNote != "predecessor had no session" {
		t.Fatalf("plan show: second = %+v", got[b.TodoID])
	}

	// PATCH clears it with an empty string.
	resp = do(t, s, http.MethodPatch, "/v1/todos/"+b.TodoID, testToken, map[string]any{"continue_from": ""})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	td, _, _ := meta.GetTodo(b.TodoID)
	if td.ContinueFrom != "" {
		t.Fatalf("continue_from after clear = %q", td.ContinueFrom)
	}
}

// TestTodoContinueFromValidationHTTP: self, cycle and cross-plan references are 400s
// with a message naming the broken rule, on both create and patch.
func TestTodoContinueFromValidationHTTP(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	mkContinuePlan(t, s, "plan-vone")
	mkContinuePlan(t, s, "plan-vtwo")
	a := addContinueTodo(t, s, "plan-vone", "a", "")
	b := addContinueTodo(t, s, "plan-vone", "b", a.TodoID)
	other := addContinueTodo(t, s, "plan-vtwo", "other", "")

	patch := func(id, from string) *http.Response {
		return do(t, s, http.MethodPatch, "/v1/todos/"+id, testToken, map[string]any{"continue_from": from})
	}
	cases := []struct {
		name string
		resp *http.Response
		want string
	}{
		{"self", patch(a.TodoID, a.TodoID), "itself"},
		{"cycle", patch(a.TodoID, b.TodoID), "cycle"},
		{"cross-plan patch", patch(b.TodoID, other.TodoID), "same plan"},
		{"unknown", patch(b.TodoID, "todo-nope"), "same plan"},
		{"cross-plan create", do(t, s, http.MethodPost, "/v1/plans/plan-vone/todos", testToken,
			map[string]any{"title": "x", "continue_from": other.TodoID}), "same plan"},
	}
	for _, tc := range cases {
		if tc.resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status=%d, want 400", tc.name, tc.resp.StatusCode)
			tc.resp.Body.Close()
			continue
		}
		if msg := errorText(t, tc.resp); !strings.Contains(msg, tc.want) {
			t.Errorf("%s: error %q should mention %q", tc.name, msg, tc.want)
		}
	}
	// The failed create left no stray todo in plan-vone.
	todos, _ := s.jobs.Meta().ListTodosByPlan("plan-vone")
	if len(todos) != 2 {
		t.Errorf("plan-vone has %d todos, want 2", len(todos))
	}
}

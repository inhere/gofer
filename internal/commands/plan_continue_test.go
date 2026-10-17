package commands

import (
	"encoding/json"
	"github.com/gookit/gcli/v3"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// continueStub serves a plan with the given todos and records the last todo write body.
func continueStub(t *testing.T, todos []client.Todo, created *map[string]any) *httptest.Server {
	t.Helper()
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/plans/plan-c":
			_ = json.NewEncoder(w).Encode(client.Plan{PlanID: "plan-c", Status: "open", Todos: todos})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/plans/plan-c/todos",
			r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v1/todos/"):
			if err := json.NewDecoder(r.Body).Decode(created); err != nil {
				t.Errorf("decode body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(client.Todo{TodoID: "todo-new", Status: "pending"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestPlanAddTodoContinuePrev(t *testing.T) {
	var body map[string]any
	ts := continueStub(t, []client.Todo{{TodoID: "todo-1"}, {TodoID: "todo-2"}}, &body)
	app := NewApp("test")
	captureOutput(t, func() {
		if code := app.Run([]string{"plan", "add-todo", "--server", ts.URL, "plan-c", "third", "--continue", "prev"}); code != 0 {
			t.Errorf("exit code=%d, want 0", code)
		}
	})
	if body["continue_from"] != "todo-2" {
		t.Fatalf("continue_from = %v, want the plan's last todo todo-2 (body %v)", body["continue_from"], body)
	}
}

func TestPlanAddTodoContinueExplicitID(t *testing.T) {
	var body map[string]any
	ts := continueStub(t, []client.Todo{{TodoID: "todo-1"}, {TodoID: "todo-2"}}, &body)
	app := NewApp("test")
	captureOutput(t, func() {
		if code := app.Run([]string{"plan", "add-todo", "--server", ts.URL, "plan-c", "third", "--continue", "todo-1"}); code != 0 {
			t.Errorf("exit code=%d, want 0", code)
		}
	})
	if body["continue_from"] != "todo-1" {
		t.Fatalf("continue_from = %v, want todo-1", body["continue_from"])
	}
}

func TestPlanAddTodoContinuePrevEmptyPlan(t *testing.T) {
	var body map[string]any
	ts := continueStub(t, nil, &body)
	app := NewApp("test")
	out := captureOutput(t, func() {
		if code := app.Run([]string{"plan", "add-todo", "--server", ts.URL, "plan-c", "first", "--continue", "prev"}); code == 0 {
			t.Errorf("exit code=0, want a failure for --continue prev on an empty plan")
		}
	})
	if body != nil {
		t.Fatalf("a todo was created despite the error: %v", body)
	}
	if !strings.Contains(out, "continue needs a previous todo") {
		t.Fatalf("output should explain the failure:\n%s", out)
	}
}

func TestPlanSetTodoContinueRejectsPrev(t *testing.T) {
	var body map[string]any
	ts := continueStub(t, nil, &body)
	app := NewApp("test")
	out := captureOutput(t, func() {
		if code := app.Run([]string{"plan", "set-todo", "--server", ts.URL, "todo-9", "--continue", "prev"}); code == 0 {
			t.Errorf("exit code=0, want a failure for set-todo --continue prev")
		}
	})
	if body != nil {
		t.Fatalf("a patch was sent despite the error: %v", body)
	}
	if !strings.Contains(out, "only meaningful when ADDING") {
		t.Fatalf("output should explain the failure:\n%s", out)
	}
}

func TestPlanSetTodoContinue(t *testing.T) {
	var body map[string]any
	ts := continueStub(t, nil, &body)
	app := NewApp("test")
	captureOutput(t, func() {
		if code := app.Run([]string{"plan", "set-todo", "--server", ts.URL, "todo-9", "--continue", "todo-1"}); code != 0 {
			t.Errorf("--continue exit code=%d, want 0", code)
		}
	})
	if body["continue_from"] != "todo-1" {
		t.Fatalf("continue_from = %v, want todo-1", body["continue_from"])
	}

}

// TestPlanSetTodoNoContinue: --no-continue sends an empty continue_from, which clears it.
func TestPlanSetTodoNoContinue(t *testing.T) {
	var body map[string]any
	ts := continueStub(t, nil, &body)
	app := NewApp("test")
	captureOutput(t, func() {
		if code := app.Run([]string{"plan", "set-todo", "--server", ts.URL, "todo-9", "--no-continue"}); code != 0 {
			t.Errorf("--no-continue exit code=%d, want 0", code)
		}
	})
	if v, ok := body["continue_from"]; !ok || v != "" {
		t.Fatalf("--no-continue must send continue_from \"\" (clear); body = %v", body)
	}
}

func TestPlanSetTodoContinueConflict(t *testing.T) {
	var body map[string]any
	ts := continueStub(t, nil, &body)
	app := NewApp("test")
	out := captureOutput(t, func() {
		if code := app.Run([]string{"plan", "set-todo", "--server", ts.URL, "todo-9", "--continue", "todo-1", "--no-continue"}); code == 0 {
			t.Errorf("exit code=0, want a failure for the conflicting flags")
		}
	})
	if !strings.Contains(out, "mutually exclusive") {
		t.Fatalf("output should explain the conflict:\n%s", out)
	}
}

func TestPlanShowTodoContinueFields(t *testing.T) {
	out := captureOutput(t, func() {
		printPlanTodos(gcli.NewCommand("show", "", nil), []client.Todo{
			{TodoID: "todo-a", Title: "first", Status: "done", ContinuedBy: "todo-b"},
			{TodoID: "todo-b", Title: "second", Status: "ready", ContinueFrom: "todo-a", ContinueNote: "no session to continue"},
		})
	})
	for _, want := range []string{"continued_by=todo-b", "continue=todo-a", "continue_note: no session to continue"} {
		if !strings.Contains(out, want) {
			t.Fatalf("plan show output missing %q:\n%s", want, out)
		}
	}
}

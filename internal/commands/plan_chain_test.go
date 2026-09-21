package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// TestPlanAddTodoAfterPrev (PLAN-03): `plan add-todo --after prev` resolves against the
// plan's CURRENT items (the last one by sort) before the request is sent, and the chain
// flags travel in the same create body as the dispatch fields.
func TestPlanAddTodoAfterPrev(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""

	var gotBody map[string]any
	var gotCreatePath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/plans/plan-1":
			_ = json.NewEncoder(w).Encode(client.Plan{
				PlanID: "plan-1", Status: "open",
				Todos: []client.Todo{
					{TodoID: "todo-1", Sort: 10},
					{TodoID: "todo-2", Sort: 20},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/plans/plan-1/todos":
			gotCreatePath = r.URL.Path
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode todo body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(client.Todo{TodoID: "todo-3", Status: "pending"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	app := NewApp("test")
	code := app.Run([]string{
		"plan", "add-todo", "--server", ts.URL, "plan-1", "step three",
		"--after", "prev", "--assign", "omp", "--no-auto", "--cmd", "go test ./...",
	})
	if code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if gotCreatePath != "/v1/plans/plan-1/todos" {
		t.Fatalf("create path = %q", gotCreatePath)
	}
	after, ok := gotBody["after"].([]any)
	if !ok || len(after) != 1 || after[0] != "todo-2" {
		t.Fatalf("after = %v, want the plan's last item (todo-2)", gotBody["after"])
	}
	if auto, ok := gotBody["auto"].(bool); !ok || auto {
		t.Fatalf("auto = %v, want false from --no-auto", gotBody["auto"])
	}
	cmd, ok := gotBody["cmd"].([]any)
	if !ok || len(cmd) != 3 || cmd[0] != "go" || cmd[2] != "./..." {
		t.Fatalf("cmd = %v, want the shell-split argv", gotBody["cmd"])
	}
	if gotBody["assignee"] != "omp" {
		t.Fatalf("assignee = %v, want omp", gotBody["assignee"])
	}

	// An explicit id list is sent as given (no plan lookup needed).
	gotBody, gotCreatePath = nil, ""
	if code := app.Run([]string{
		"plan", "add-todo", "--server", ts.URL, "plan-1", "step four", "--after", "todo-1,todo-2",
	}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	after, _ = gotBody["after"].([]any)
	if len(after) != 2 || after[0] != "todo-1" || after[1] != "todo-2" {
		t.Fatalf("after = %v, want both ids", gotBody["after"])
	}
}

// TestPlanRunPauseResumeCommands (PLAN-03): the three chain controls are `plan
// run|pause|resume <plan-id>` and each reports the plan state the server answered with
// (a blocked plan prints the item to release).
func TestPlanRunPauseResumeCommands(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""

	var actions []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/v1/plans/plan-9/") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		action := strings.TrimPrefix(r.URL.Path, "/v1/plans/plan-9/")
		actions = append(actions, action)
		out := client.Plan{PlanID: "plan-9", Status: "open"}
		if action == "pause" {
			out.Paused = true
		}
		if action == "run" {
			out.BlockedTodo = "todo-b"
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer ts.Close()

	app := NewApp("test")
	out := captureOutput(t, func() {
		for _, action := range []string{"run", "pause", "resume"} {
			if code := app.Run([]string{"plan", action, "--server", ts.URL, "plan-9"}); code != 0 {
				t.Fatalf("plan %s exit code=%d", action, code)
			}
		}
	})
	if len(actions) != 3 || actions[0] != "run" || actions[1] != "pause" || actions[2] != "resume" {
		t.Fatalf("actions = %v, want run/pause/resume", actions)
	}
	for _, want := range []string{"plan plan-9 run", "status=open", "paused=true", "blocked on todo-b"} {
		if !strings.Contains(out, want) {
			t.Fatalf("plan action output missing %q:\n%s", want, out)
		}
	}

	// A missing plan id is refused before any request.
	before := len(actions)
	if code := app.Run([]string{"plan", "run", "--server", ts.URL}); code == 0 {
		t.Fatal("plan run without a plan id must fail")
	}
	if len(actions) != before {
		t.Fatal("a refused call must not reach the server")
	}
}

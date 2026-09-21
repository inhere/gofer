package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// isolateTodoCLI prepares the package-global CLI state for a plan todo command test
// and returns the captured request of a stub server.
func isolateTodoCLI(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

// TestPlanSetTodoAssignFlags (PLAN-02 P2): `plan set-todo` carries the whole dispatch
// request in ONE update — assignee, project, task book + vars, verify argv, review,
// runner, cwd and timeout — plus the new `ready` status, so
// `plan set-todo <id> --assign omp --status ready` is a single round trip that both
// describes and starts the work.
func TestPlanSetTodoAssignFlags(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	ts := isolateTodoCLI(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode todo body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.Todo{TodoID: "todo-1", Status: "doing"})
	})

	app := NewApp("test")
	code := app.Run([]string{
		"plan", "set-todo", "--server", ts.URL, "todo-1",
		"--status", "ready",
		"--assign", "omp",
		"--project", "shop-floor",
		"--template", "impl-batch",
		"--var", "k=v", "--var", "j=w",
		"--verify", "go test ./...",
		"--review",
		"--runner", "local",
		"--cwd", "sub/dir",
		"--timeout", "1800",
	})
	if code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if gotMethod != http.MethodPatch || gotPath != "/v1/todos/todo-1" {
		t.Fatalf("unexpected request: %s %s", gotMethod, gotPath)
	}
	if gotBody["status"] != "ready" || gotBody["assignee"] != "omp" {
		t.Fatalf("status/assignee = %v/%v, want ready/omp", gotBody["status"], gotBody["assignee"])
	}
	if gotBody["project"] != "shop-floor" || gotBody["template"] != "impl-batch" {
		t.Fatalf("project/template = %v/%v", gotBody["project"], gotBody["template"])
	}
	vars, _ := gotBody["vars"].(map[string]any)
	if vars["k"] != "v" || vars["j"] != "w" {
		t.Fatalf("vars = %v, want both --var pairs", gotBody["vars"])
	}
	verify, _ := gotBody["verify"].([]any)
	if len(verify) != 3 || verify[0] != "go" || verify[2] != "./..." {
		t.Fatalf("verify = %v, want the split argv", gotBody["verify"])
	}
	if gotBody["review"] != true || gotBody["runner"] != "local" || gotBody["cwd"] != "sub/dir" {
		t.Fatalf("review/runner/cwd = %v/%v/%v", gotBody["review"], gotBody["runner"], gotBody["cwd"])
	}
	if gotBody["timeout_sec"] != float64(1800) {
		t.Fatalf("timeout_sec = %v, want 1800", gotBody["timeout_sec"])
	}
	if _, ok := gotBody["note"]; ok {
		t.Fatalf("no --note was given, so the body must not carry one: %v", gotBody)
	}
	// The new lifecycle value is accepted (not rejected as an unknown status).
	if _, ok := gotBody["append_note"]; ok {
		t.Fatalf("unexpected append_note in %v", gotBody)
	}
}

// TestPlanDispatchCommand (PLAN-02 P2): `plan dispatch <todo>` is the explicit
// fallback — it POSTs the dispatch endpoint and reports BOTH outcomes: the job it
// started, or why nothing was started.
func TestPlanDispatchCommand(t *testing.T) {
	var gotMethod, gotPath string
	dispatched := true
	var reason string
	ts := isolateTodoCLI(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		out := client.TodoDispatch{
			Todo:       client.Todo{TodoID: "todo-1", Status: "doing"},
			Dispatched: dispatched,
			Reason:     reason,
		}
		if dispatched {
			out.Job = &job.JobResult{ID: "job-9", Agent: "omp", Status: job.StatusRunning}
		}
		_ = json.NewEncoder(w).Encode(out)
	})

	var out string
	code := NewApp("test").Run([]string{"plan", "dispatch", "--server", ts.URL, "todo-1"})
	if code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/todos/todo-1/dispatch" {
		t.Fatalf("unexpected request: %s %s", gotMethod, gotPath)
	}

	out = captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"plan", "dispatch", "--server", ts.URL, "todo-1"}); code != 0 {
			t.Fatalf("app.Run exit code=%d", code)
		}
	})
	if !strings.Contains(out, "job-9") || !strings.Contains(out, "omp") {
		t.Fatalf("dispatch output missing the job it started:\n%s", out)
	}

	// A skipped dispatch says why (nothing was started), and still succeeds: the
	// command reported the state, it did not fail.
	dispatched, reason = false, "todo already has an active job job-1"
	out = captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"plan", "dispatch", "--server", ts.URL, "todo-1"}); code != 0 {
			t.Fatalf("app.Run exit code=%d", code)
		}
	})
	if !strings.Contains(out, "job-1") {
		t.Fatalf("dispatch output must name the blocking job:\n%s", out)
	}

	// A todo nobody can dispatch (unknown id) is a failure, not a silent success.
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"unknown todo","detail":"no todo with id todo-1"}`))
	})
	if code := NewApp("test").Run([]string{"plan", "dispatch", "--server", ts.URL, "todo-1"}); code == 0 {
		t.Fatal("dispatching an unknown todo must fail")
	}
}

// TestPlanCreateProjectFlag (PLAN-02 P2): `plan create --project` is what makes a
// plan's todos dispatchable without repeating the project on every item.
func TestPlanCreateProjectFlag(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]string
	ts := isolateTodoCLI(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode plan body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.Plan{PlanID: "plan-cli-proj", Status: "open"})
	})

	if code := NewApp("test").Run([]string{
		"plan", "create", "--server", ts.URL,
		"--plan-id", "plan-cli-proj", "--title", "CLI plan", "--project", "shop-floor",
	}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/plans" {
		t.Fatalf("unexpected request: %s %s", gotMethod, gotPath)
	}
	if gotBody["project"] != "shop-floor" || gotBody["title"] != "CLI plan" {
		t.Fatalf("create body = %v, want the project and title", gotBody)
	}
}

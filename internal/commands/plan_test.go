package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// TestPlanSetTodoAppendNote: `plan set-todo <id> --append-note "<line>"` PATCHes
// the todo with the appended line and NO status (appending must not flip a todo's
// state — it is how a job's outcome is recorded on the checklist, SUP-01 C), and
// --note with --append-note is refused before any request is sent.
func TestPlanSetTodoAppendNote(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""

	var gotPath, gotMethod string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode todo body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.Todo{TodoID: "todo-1", Status: "doing"})
	}))
	defer ts.Close()

	app := NewApp("test")
	if code := app.Run([]string{"plan", "set-todo", "--server", ts.URL, "todo-1", "--append-note", "job-1 ✓ no commits"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if gotMethod != http.MethodPatch || gotPath != "/v1/todos/todo-1" {
		t.Fatalf("unexpected request: %s %s", gotMethod, gotPath)
	}
	if gotBody["append_note"] != "job-1 ✓ no commits" {
		t.Fatalf("append_note = %v, want the line", gotBody["append_note"])
	}
	if status, ok := gotBody["status"]; ok && status != "" {
		t.Fatalf("append must not carry a status, got %v", status)
	}

	// The two note forms are mutually exclusive: refuse without a request.
	gotPath, gotBody = "", nil
	if code := app.Run([]string{"plan", "set-todo", "--server", ts.URL, "todo-1", "--note", "x", "--append-note", "y"}); code == 0 {
		t.Fatal("--note with --append-note must fail")
	}
	if gotPath != "" {
		t.Fatalf("a rejected update must not reach the server: %s", gotPath)
	}
}

func TestPlanSubcommandsRegistered(t *testing.T) {
	app := NewApp("test")

	planCmd := app.GetCommand("plan")
	if planCmd == nil {
		t.Fatal("plan command not registered")
	}
	for _, sub := range []string{"create", "list", "show", "attach", "set-status", "archive", "add-todo", "set-todo", "ask", "decisions", "answer"} {
		if planCmd.GetCommand(sub) == nil {
			t.Fatalf("plan subcommand %q not registered", sub)
		}
	}
	if !planCmd.IsAlias("ls") || planCmd.ResolveAlias("ls") != "list" {
		t.Fatalf("`ls` should be an alias for plan list, got %q", planCmd.ResolveAlias("ls"))
	}
	if !planCmd.IsAlias("todo-add") || planCmd.ResolveAlias("todo-add") != "add-todo" {
		t.Fatalf("`todo-add` should be an alias for plan add-todo, got %q", planCmd.ResolveAlias("todo-add"))
	}
	if !planCmd.IsAlias("todo-done") || planCmd.ResolveAlias("todo-done") != "set-todo" {
		t.Fatalf("`todo-done` should be an alias for plan set-todo, got %q", planCmd.ResolveAlias("todo-done"))
	}
}

func TestPrintPlanTodos(t *testing.T) {
	out := captureOutput(t, func() {
		printPlanTodos(gcli.NewCommand("show", "", nil), []client.Todo{
			{TodoID: "todo-1", Title: "done todo", Done: true, JobID: "job-1"},
			{TodoID: "todo-2", Title: "plain todo"},
		})
	})
	for _, want := range []string{"todos:", "[x]", "todo-1", "done todo", "(job=job-1)", "[ ]", "todo-2", "plain todo"} {
		if !strings.Contains(out, want) {
			t.Fatalf("printPlanTodos output missing %q:\n%s", want, out)
		}
	}
}

package commands

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/tracker"
)

// TestPlanTodoAcceptanceFlags (gofer-3nxa.4): --acceptance rides the add/patch body,
// `--acceptance ""` clears it on set-todo, and --acceptance-from-issue copies the
// current repo's issue criteria client-side (an issue without any is an error).
func TestPlanTodoAcceptanceFlags(t *testing.T) {
	var gotBody map[string]any
	var requests int
	ts := isolateTodoCLI(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		gotBody = nil
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode todo body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.Todo{TodoID: "todo-1", Status: "pending"})
	})
	// The flag values live in package globals: leave none behind for later tests.
	t.Cleanup(resetPlanFlagGlobals)

	if code := NewApp("test").Run([]string{"plan", "add-todo", "--server", ts.URL, "plan-1", "T", "--acceptance", "- builds"}); code != 0 {
		t.Fatalf("add-todo exit code=%d", code)
	}
	if gotBody["acceptance"] != "- builds" {
		t.Fatalf("add body acceptance = %v", gotBody["acceptance"])
	}

	resetPlanFlagGlobals()
	if code := NewApp("test").Run([]string{"plan", "set-todo", "--server", ts.URL, "todo-1", "--acceptance", ""}); code != 0 {
		t.Fatalf("set-todo exit code=%d", code)
	}
	if v, ok := gotBody["acceptance"]; !ok || v != "" {
		t.Fatalf("set-todo --acceptance \"\" must send an empty value, got %v (present=%v)", v, ok)
	}
	if _, ok := gotBody["status"]; ok {
		t.Fatalf("an acceptance-only update must not change the status: %v", gotBody)
	}

	repo := t.TempDir()
	store, _, err := tracker.Init(repo, "acc", true)
	if err != nil {
		t.Fatalf("tracker init: %v", err)
	}
	with, err := store.CreateIssue(tracker.Issue{Title: "with", AcceptanceCriteria: "- vet clean\n"})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	without, err := store.CreateIssue(tracker.Issue{Title: "without"})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Chdir(repo)

	resetPlanFlagGlobals()
	if code := NewApp("test").Run([]string{"plan", "add-todo", "--server", ts.URL, "plan-1", "T", "--acceptance-from-issue", with.ID}); code != 0 {
		t.Fatalf("add-todo --acceptance-from-issue exit code=%d", code)
	}
	if gotBody["acceptance"] != "- vet clean" {
		t.Fatalf("acceptance from issue = %v", gotBody["acceptance"])
	}

	before := requests
	resetPlanFlagGlobals()
	if code := NewApp("test").Run([]string{"plan", "add-todo", "--server", ts.URL, "plan-1", "T", "--acceptance-from-issue", without.ID}); code == 0 {
		t.Fatal("an issue without acceptance_criteria must fail")
	}
	resetPlanFlagGlobals()
	if code := NewApp("test").Run([]string{"plan", "add-todo", "--server", ts.URL, "plan-1", "T", "--acceptance-from-issue", "acc-nope"}); code == 0 {
		t.Fatal("an unknown issue must fail")
	}
	if requests != before {
		t.Fatalf("a failed lookup must not reach the server (%d requests)", requests-before)
	}
}

// TestJobRunAcceptanceFlag: --acceptance reaches JobRequest.Acceptance (trimmed).
func TestJobRunAcceptanceFlag(t *testing.T) {
	jobRunOpts = jobRunFlags{}
	t.Cleanup(func() { jobRunOpts = jobRunFlags{} })

	app := NewApp("test")
	var got job.JobRequest
	var gotErr error
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, _ []string) error {
		got, gotErr = buildJobRunRequest(c, nil)
		return nil
	}
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "claude", "--prompt", "x", "--acceptance", " - a\n- b\n"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if gotErr != nil || got.Acceptance != "- a\n- b" {
		t.Fatalf("acceptance = %q err=%v", got.Acceptance, gotErr)
	}
}

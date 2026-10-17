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

// TestPlanAddTodoAfterPrevEmptyPlan: `--after prev` on a plan with no items yet means
// "no dependency" — it succeeds, sends no `after`, and says so in the output.
func TestPlanAddTodoAfterPrevEmptyPlan(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""

	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/plans/plan-e":
			_ = json.NewEncoder(w).Encode(client.Plan{PlanID: "plan-e", Status: "open"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/plans/plan-e/todos":
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode todo body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(client.Todo{TodoID: "todo-1", Status: "pending"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer ts.Close()

	app := NewApp("test")
	out := captureOutput(t, func() {
		if code := app.Run([]string{"plan", "add-todo", "--server", ts.URL, "plan-e", "first", "--after", "prev"}); code != 0 {
			t.Errorf("exit code=%d, want 0", code)
		}
	})
	if gotBody == nil {
		t.Fatal("todo was not created")
	}
	if a, ok := gotBody["after"]; ok && a != nil {
		if l, _ := a.([]any); len(l) != 0 {
			t.Fatalf("after = %v, want no dependency", a)
		}
	}
	if !strings.Contains(out, "added to plan plan-e") || !strings.Contains(out, "without a dependency") {
		t.Fatalf("output should report success and the no-dependency note:\n%s", out)
	}
}

package commands

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/client"
)

// TestPlanTodoCheckPromptFlag: --check-prompt rides the add/patch body as an explicit
// delivery-check requirement, and `--check-prompt ""` clears it (with its source) on
// set-todo.
func TestPlanTodoCheckPromptFlag(t *testing.T) {
	var gotBody map[string]any
	ts := isolateTodoCLI(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody = nil
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode todo body: %v", err)
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.Todo{TodoID: "todo-1", Status: "pending"})
	})
	t.Cleanup(resetPlanFlagGlobals)

	if code := NewApp("test").Run([]string{"plan", "add-todo", "--server", ts.URL, "plan-1", "T", "--check-prompt", " at least two commits "}); code != 0 {
		t.Fatalf("add-todo exit code=%d", code)
	}
	if gotBody["check_prompt"] != "at least two commits" || gotBody["check_prompt_source"] != "explicit" {
		t.Fatalf("add body = %v", gotBody)
	}

	resetPlanFlagGlobals()
	if code := NewApp("test").Run([]string{"plan", "set-todo", "--server", ts.URL, "todo-1", "--check-prompt", ""}); code != 0 {
		t.Fatalf("set-todo exit code=%d", code)
	}
	if v, ok := gotBody["check_prompt"]; !ok || v != "" {
		t.Fatalf("set-todo --check-prompt \"\" must send an empty value, got %v (present=%v)", v, ok)
	}
	if _, ok := gotBody["status"]; ok {
		t.Fatalf("a check-prompt-only update must not change the status: %v", gotBody)
	}

	resetPlanFlagGlobals()
	if code := NewApp("test").Run([]string{"plan", "add-todo", "--server", ts.URL, "plan-1", "T"}); code != 0 {
		t.Fatalf("add-todo exit code=%d", code)
	}
	if _, ok := gotBody["check_prompt"]; ok {
		t.Fatalf("no flag, no field: %v", gotBody)
	}
}

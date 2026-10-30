package commands

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestWorkflowListTemplateFilter pins `wf ls --template <name>`: the flag exists on the list
// command and its value reaches the server as ?template=.
func TestWorkflowListTemplateFilter(t *testing.T) {
	var gotQuery string
	stubAgentServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workflows" {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{"workflows": []map[string]any{
			{"id": "wf-1", "status": "done", "current_step": 1, "total_steps": 1, "template": "demo-tpl", "created_at": 100, "updated_at": 110},
		}, "total": 1})
	}))
	t.Cleanup(func() { wfListOpts.status, wfListOpts.template = "", "" })

	c := bindCmd(findSub(t, NewWorkflowCmd(), "list"))
	if c.Opt("template") == nil {
		t.Fatal("wf list has no --template option")
	}
	wfListOpts.template = "demo-tpl"
	out := captureOutput(t, func() {
		if err := runWorkflowList(c, nil); err != nil {
			t.Fatalf("wf list: %v", err)
		}
	})
	if gotQuery != "template=demo-tpl" {
		t.Fatalf("query = %q, want template=demo-tpl", gotQuery)
	}
	if !strings.Contains(out, "wf-1") {
		t.Fatalf("wf list output missing the run:\n%s", out)
	}
}

// TestWorkflowTemplateListShowsRuns pins that `wf template ls` prints each template's
// execution count and the status of its newest run ("0 runs" when it never ran).
func TestWorkflowTemplateListShowsRuns(t *testing.T) {
	stubAgentServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workflow-templates" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"templates": []map[string]any{
			{"name": "ran-tpl", "source": "global", "spec": map[string]any{"steps": []any{}}, "runs": 3,
				"last_run": map[string]any{"id": "wf-9", "status": "failed", "started_at": 100, "ended_at": 120}},
			{"name": "idle-tpl", "source": "builtin", "spec": map[string]any{"steps": []any{}}, "runs": 0},
		}})
	}))

	c := bindCmd(findSub(t, findSub(t, NewWorkflowCmd(), "template"), "ls"))
	out := captureOutput(t, func() {
		if err := runWorkflowTemplateList(c, nil); err != nil {
			t.Fatalf("wf template ls: %v", err)
		}
	})
	for _, want := range []string{"ran-tpl", "3 runs, last failed", "idle-tpl", "0 runs"} {
		if !strings.Contains(out, want) {
			t.Fatalf("template list missing %q:\n%s", want, out)
		}
	}
}

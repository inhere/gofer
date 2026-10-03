package workflow

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/store"
)

func TestWorkflowAgentsFanOutUsesEachAgent(t *testing.T) {
	step := StepSpec{
		Name: "compare", ProjectKey: "p", Agent: "legacy", Runner: "local",
		Agents: []string{"claude", "codex"},
	}
	a := stepToRequestForFan(step, "wf", 1, 1, 1, "caller")
	b := stepToRequestForFan(step, "wf", 1, 1, 2, "caller")
	if a.Agent != "claude" || b.Agent != "codex" {
		t.Fatalf("agents = %q/%q, want claude/codex", a.Agent, b.Agent)
	}
	if fanWant(step) != 2 {
		t.Fatalf("fanWant = %d, want 2", fanWant(step))
	}
}

func TestWorkflowStepWorktreePassThrough(t *testing.T) {
	step := StepSpec{
		ProjectKey: "p", Agent: "exec", Runner: "local", Worktree: true,
		WorktreeBase: "main", Template: "compare", Vars: map[string]string{"task": "x"},
		ReadOnly: true, Verify: []string{"go", "test", "./..."},
	}
	req := stepToRequest(step, "wf", 1, 1, 0, "caller")
	if !req.Worktree || req.WorktreeBase != "main" || req.Template != "compare" {
		t.Fatalf("worktree/template not passed through: %+v", req)
	}
	if req.TemplateVars["task"] != "x" || !req.ReadOnly || len(req.Verify) != 3 {
		t.Fatalf("step options not passed through: %+v", req)
	}
}

func TestStepRefByNameAndAll(t *testing.T) {
	root := t.TempDir()
	e := newTestEngine(t, root)
	makeFan := func(id, out string, fan int) jobstore.JobRecord {
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, store.StdoutFile), []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
		r := jobstore.JobRecord{ID: id, ProjectKey: "self", Agent: "exec", Runner: "local", Status: job.StatusDone, ResultDir: dir, WorkflowID: "wf", StepIndex: 1, FanIndex: fan, Attempt: 1}
		if err := e.meta.UpsertJob(r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	f1 := makeFan("f1", "claude output\n", 1)
	f2 := makeFan("f2", "codex output\n", 2)
	spec := Spec{Steps: []StepSpec{{Name: "compare", FanOut: 2}, {Name: "summarize", Prompt: "${steps.compare.all.stdout}"}}}
	step := spec.Steps[1]
	if err := e.resolveRefs(&step, []jobstore.JobRecord{f1, f2}, spec); err != nil {
		t.Fatalf("resolveRefs: %v", err)
	}
	if step.Prompt != "claude output\ncodex output\n" {
		t.Fatalf("named aggregate = %q", step.Prompt)
	}
}

func TestJoinPickWaitsForSelection(t *testing.T) {
	fans := []*jobstore.JobRecord{{Status: job.StatusDone}, {Status: job.StatusDone}}
	if fanTerminal(fans, 2, joinPick) {
		t.Fatal("join=pick became terminal before selection")
	}
}

func TestWorkflowTemplateVars(t *testing.T) {
	spec, err := ResolveBuiltinWorkflowTemplate("compare", map[string]string{"project": "repo", "task": "fix it"})
	if err != nil {
		t.Fatalf("resolve template: %v", err)
	}
	if spec.Steps[0].ProjectKey != "repo" || spec.Steps[0].Prompt != "fix it" {
		t.Fatalf("vars not rendered: %+v", spec.Steps[0])
	}
	if _, err := ResolveBuiltinWorkflowTemplate("compare", map[string]string{"project": "repo"}); err == nil {
		t.Fatal("missing required task variable accepted")
	}
}

func TestJoinPickAdvancesAfterSelection(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	step := fanEchoStep("pick", 2, joinPick)
	wf, err := e.SubmitWorkflow(Spec{Steps: []StepSpec{step, echoStep("after")}}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		jobs, _ := e.meta.ListWorkflowJobs(wf.ID)
		fans := stepFanJobs(jobs, 1, 1)
		if len(fans) == 2 && job.IsTerminal(fans[0].Status) && job.IsTerminal(fans[1].Status) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, _, _ := e.meta.GetWorkflow(wf.ID)
	if current.Status != jobstore.WorkflowRunning {
		t.Fatalf("join=pick status=%s before selection, want running", current.Status)
	}
	if _, err := e.PickWorkflowFan(wf.ID, 1, 1); err != nil {
		t.Fatalf("PickWorkflowFan: %v", err)
	}
	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("picked workflow status=%s err=%s, want done", final.Status, final.Error)
	}
}

func TestBuiltinWorkflowTemplatesValidate(t *testing.T) {
	for _, tpl := range BuiltinWorkflowTemplates() {
		spec, err := ResolveBuiltinWorkflowTemplate(tpl.Name, map[string]string{"project": "self", "task": "demo"})
		if err != nil {
			t.Fatalf("%s resolve: %v", tpl.Name, err)
		}
		if err := validateRefs(spec); err != nil {
			t.Fatalf("%s refs: %v", tpl.Name, err)
		}
		if err := validateRetry(spec); err != nil {
			t.Fatalf("%s retry: %v", tpl.Name, err)
		}
		if err := validateFanout(spec); err != nil {
			t.Fatalf("%s fanout: %v", tpl.Name, err)
		}
	}
}

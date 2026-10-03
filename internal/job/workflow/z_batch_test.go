package workflow

import (
	gotemplate "github.com/inhere/gofer/internal/template"
	"os"
	"path/filepath"
	"strings"
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

func TestWorkflowTemplateVarsCoverRoutingFields(t *testing.T) {
	spec, err := ResolveBuiltinWorkflowTemplate("compare", map[string]string{
		"project": "repo", "task": "t", "agent_a": "fake-a", "agent_b": "fake-b", "runner": "w1",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	st := spec.Steps[0]
	if st.ProjectKey != "repo" || st.Runner != "w1" || len(st.Agents) != 2 || st.Agents[0] != "fake-a" || st.Agents[1] != "fake-b" {
		t.Fatalf("routing vars not rendered: %+v", st)
	}
	// optional runner without default renders empty (project default), not a literal.
	spec, err = ResolveBuiltinWorkflowTemplate("compare", map[string]string{"project": "repo", "task": "t"})
	if err != nil {
		t.Fatalf("resolve defaults: %v", err)
	}
	if st := spec.Steps[0]; st.Runner != "" || st.Agents[0] != "claude" || st.Agents[1] != "codex" {
		t.Fatalf("defaults wrong: %+v", st)
	}
	// a variable value cannot smuggle routing syntax; undeclared vars are rejected.
	if _, err := ResolveBuiltinWorkflowTemplate("compare", map[string]string{"project": "repo", "task": "t", "agent_a": "a b;rm"}); err == nil {
		t.Fatal("invalid agent key accepted")
	}
	if _, err := ResolveBuiltinWorkflowTemplate("compare", map[string]string{"project": "repo", "task": "t", "nope": "x"}); err == nil {
		t.Fatal("undeclared variable accepted")
	}
	// fan[] / nested step vars / leftover placeholders.
	in := Spec{Vars: map[string]gotemplate.Var{"a": {Default: "agx"}}, Steps: []StepSpec{{ProjectKey: "p", Fan: []FanSpec{{Agent: "${vars.a}", Runner: "${vars.a}"}}, Vars: map[string]string{"k": "${vars.a}"}}}}
	out, err := renderWorkflowTemplate(in, nil)
	if err != nil {
		t.Fatalf("render fan: %v", err)
	}
	if out.Steps[0].Fan[0].Agent != "agx" || out.Steps[0].Fan[0].Runner != "agx" || out.Steps[0].Vars["k"] != "agx" {
		t.Fatalf("fan/vars not rendered: %+v", out.Steps[0])
	}
	if _, err := renderWorkflowTemplate(Spec{Steps: []StepSpec{{ProjectKey: "p", Prompt: "${vars.missing}"}}}, nil); err == nil {
		t.Fatal("leftover ${vars.missing} accepted")
	}
}

// Step outputs must reach a command only as paths/agent prompts, never spliced into
// an exec cmd (shell quoting / injection) — the committee verifier is an agent step.
func TestBuiltinTemplatesNeverInterpolateStepsIntoCmd(t *testing.T) {
	for _, tpl := range BuiltinWorkflowTemplates() {
		for _, st := range tpl.Spec.Steps {
			for _, a := range st.Cmd {
				if strings.Contains(a, "${steps.") {
					t.Fatalf("%s step %q interpolates a step output into cmd: %q", tpl.Name, st.Name, a)
				}
			}
		}
	}
	spec, err := ResolveBuiltinWorkflowTemplate("review-committee", map[string]string{"project": "p", "task": "x", "verifier": "ver"})
	if err != nil {
		t.Fatal(err)
	}
	sum := spec.Steps[1]
	if sum.Agent != "ver" || !sum.ReadOnly || len(sum.Cmd) != 0 || !strings.Contains(sum.Prompt, "${steps.reviews.result_dir}") {
		t.Fatalf("summary must be a read-only agent step reading result dirs: %+v", sum)
	}
}

func TestStepDiffRefIsPath(t *testing.T) {
	root := t.TempDir()
	e := newTestEngine(t, root)
	dir := filepath.Join(root, "d1")
	_ = os.MkdirAll(dir, 0o755)
	r := jobstore.JobRecord{ID: "d1", ProjectKey: "self", Agent: "exec", Runner: "local", Status: job.StatusDone, ResultDir: dir, WorkflowID: "wf", StepIndex: 1, FanIndex: 1, Attempt: 1}
	if err := e.meta.UpsertJob(r); err != nil {
		t.Fatal(err)
	}
	got, err := e.resolveRef(1, 1, "diff", []jobstore.JobRecord{r})
	if err != nil || got != filepath.Join(dir, "changes.diff") {
		t.Fatalf("diff ref = %q, %v", got, err)
	}
}

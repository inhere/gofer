package workflow

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/workflowstep"
)

// ---------------------------------------------------------------------------
// S3: built-in steps (`uses: gofer/<name>` + `with:`).
// ---------------------------------------------------------------------------

// TestUsesStepBecomesInternalExecJob proves a built-in step turns into gofer's own
// `gofer tool step <name>` job: the with block travels as JSON on stdin and the job is the
// workflow-step internal exec kind (design §一·5).
func TestUsesStepBecomesInternalExecJob(t *testing.T) {
	t.Parallel()
	step := StepSpec{
		Name: "push", ProjectKey: "self", Agent: "exec", Runner: "local",
		Uses: "gofer/push", With: map[string]any{"refs": []any{"main", "v1.0.0"}},
	}
	req := stepToRequestForFan(step, "wf-1", 1, 1, 0, "alice")
	if len(req.Cmd) != 4 || req.Cmd[0] != "gofer" || req.Cmd[1] != "tool" || req.Cmd[2] != "step" || req.Cmd[3] != "push" {
		t.Fatalf("cmd = %v, want [gofer tool step push]", req.Cmd)
	}
	if req.InternalExec != job.InternalExecWorkflowStep {
		t.Fatalf("internal exec = %q, want %q", req.InternalExec, job.InternalExecWorkflowStep)
	}
	if req.Agent != config.BuiltinExecAgentKey {
		t.Fatalf("agent = %q, want the built-in exec agent", req.Agent)
	}
	var with map[string]any
	if err := json.Unmarshal([]byte(req.Stdin), &with); err != nil {
		t.Fatalf("stdin is not JSON: %v (%q)", err, req.Stdin)
	}
	if refs, _ := with["refs"].([]any); len(refs) != 2 {
		t.Fatalf("stdin = %q, want the refs of the with block", req.Stdin)
	}
	if !slices.Contains(req.Tags, job.WorkflowStepJobTag) {
		t.Fatalf("tags = %v, want the %q tag", req.Tags, job.WorkflowStepJobTag)
	}

	plain := stepToRequestForFan(StepSpec{Name: "x", Agent: "exec", Runner: "local", Cmd: []string{"echo"}}, "wf-1", 1, 1, 0, "alice")
	if plain.InternalExec != "" || plain.Stdin != "" {
		t.Fatalf("a plain exec step must not be an internal exec job: %+v", plain)
	}
}

// TestValidateUsesStep covers the submit-time gate: a known name plus a strictly decoded
// with block, no cmd / prompt / fan alongside, and the project's delivery.allow_push for
// a push step.
func TestValidateUsesStep(t *testing.T) {
	t.Parallel()
	pushOK := &config.Config{Projects: map[string]config.ProjectConfig{
		"self": {AllowExec: true, Delivery: &config.DeliveryConfig{AllowPush: true}},
	}}
	noPush := &config.Config{Projects: map[string]config.ProjectConfig{"self": {AllowExec: true}}}
	cases := []struct {
		name string
		cfg  *config.Config
		step StepSpec
		ok   bool
	}{
		{"push allowed", pushOK, StepSpec{ProjectKey: "self", Uses: "gofer/push", With: map[string]any{"refs": []any{"main"}}}, true},
		{"push not allowed", noPush, StepSpec{ProjectKey: "self", Uses: "gofer/push", With: map[string]any{"refs": []any{"main"}}}, false},
		{"wait-ci", noPush, StepSpec{ProjectKey: "self", Uses: "gofer/wait-ci", With: map[string]any{"ref": "v1"}}, true},
		{"unknown step", pushOK, StepSpec{ProjectKey: "self", Uses: "gofer/nope"}, false},
		{"not gofer", pushOK, StepSpec{ProjectKey: "self", Uses: "actions/checkout@v4"}, false},
		{"unknown with key", pushOK, StepSpec{ProjectKey: "self", Uses: "gofer/push", With: map[string]any{"refs": []any{"main"}, "branch": "main"}}, false},
		{"with without uses", pushOK, StepSpec{ProjectKey: "self", With: map[string]any{"refs": []any{"main"}}}, false},
		{"uses with cmd", pushOK, StepSpec{ProjectKey: "self", Uses: "gofer/push", Cmd: []string{"git", "push"}}, false},
		{"uses with fan", pushOK, StepSpec{ProjectKey: "self", Uses: "gofer/push", Fan: []FanSpec{{Cmd: []string{"x"}}}}, false},
	}
	for _, c := range cases {
		err := validateUsesStep(c.cfg, c.step)
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: should be refused", c.name)
		}
	}
	// The refusals must name what is wrong, not just fail.
	if err := validateUsesStep(pushOK, StepSpec{ProjectKey: "self", Uses: "gofer/push", With: map[string]any{"refs": []any{"main"}}}); err != nil {
		t.Fatalf("push allowed by config: %v", err)
	}
	if err := validateUsesStep(noPush, StepSpec{ProjectKey: "self", Uses: "gofer/push", With: map[string]any{"refs": []any{"main"}}}); err == nil ||
		!strings.Contains(err.Error(), "allow_push") {
		t.Fatalf("err = %v, want it to name delivery.allow_push", err)
	}
}

// TestSpecUsesPush finds a push step anywhere in the tree, so the HTTP entry point can
// demand repo.push for nested sub-workflows too.
func TestSpecUsesPush(t *testing.T) {
	t.Parallel()
	if SpecUsesPush(Spec{Steps: []StepSpec{{Uses: "gofer/wait-ci", With: map[string]any{"ref": "v1"}}}}) {
		t.Fatal("wait-ci is not a push")
	}
	nested := Spec{Steps: []StepSpec{{Type: stepTypeWorkflow, SubWorkflow: &Spec{
		Steps: []StepSpec{{Uses: "gofer/push", With: map[string]any{"refs": []any{"main"}}}},
	}}}}
	if !SpecUsesPush(nested) {
		t.Fatal("a nested gofer/push step must be found")
	}
}

// TestSubmitRejectsBadUsesStep proves the gate runs at submit, before any job exists: an
// unknown built-in step / a misspelled argument is a submit error.
func TestSubmitRejectsBadUsesStep(t *testing.T) {
	t.Parallel()
	e := newTestEngine(t, t.TempDir())
	for _, spec := range []Spec{
		{Project: "self", Steps: []StepSpec{{Name: "x", Uses: "gofer/nope"}}},
		{Project: "self", Steps: []StepSpec{{Name: "x", Uses: "gofer/wait-ci", With: map[string]any{"refer": "v1"}}}},
		{Project: "self", Steps: []StepSpec{{Name: "x", Uses: "gofer/push", With: map[string]any{"refs": []any{"main"}}}}},
	} {
		if _, err := e.SubmitWorkflow(spec, "alice"); err == nil {
			t.Errorf("spec %+v should be rejected at submit", spec)
		}
	}
	if names := workflowstep.Names(); len(names) == 0 {
		t.Fatal("the registry is empty")
	}
}

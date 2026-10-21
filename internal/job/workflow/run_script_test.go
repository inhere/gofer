package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// TestShellCommand pins the `shell:` → argv mapping (design 2026-10-21, `run:` a la
// GitHub Actions): bash is the default because a piped workflow script must fail on the
// first failing command, and `shell: cmd` is refused on purpose.
func TestShellCommand(t *testing.T) {
	t.Parallel()
	const script = "set -e\necho one\necho two"
	cases := []struct {
		name   string
		shell  string
		script string
		want   []string
		wantIn string
	}{
		{name: "default bash", shell: "", script: script,
			want: []string{"bash", "-eo", "pipefail", "-c", script}},
		{name: "explicit bash", shell: "bash", script: script,
			want: []string{"bash", "-eo", "pipefail", "-c", script}},
		{name: "sh", shell: "sh", script: script, want: []string{"sh", "-ec", script}},
		{name: "pwsh", shell: "pwsh", script: script,
			want: []string{"pwsh", "-NoProfile", "-Command", script}},
		{name: "python", shell: "python", script: script, want: []string{"python", "-c", script}},
		{name: "case insensitive shell", shell: "PWSH", script: script,
			want: []string{"pwsh", "-NoProfile", "-Command", script}},
		{name: "empty script", shell: "bash", script: "  \n\t", wantIn: "run: is empty"},
		{name: "shell cmd", shell: "cmd", script: script, wantIn: "shell: cmd is not supported"},
		{name: "unknown shell", shell: "fish", script: script, wantIn: "unknown shell"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := shellCommand(tc.shell, tc.script)
			if tc.wantIn != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantIn) {
					t.Fatalf("shellCommand(%q) = %v, %v; want an error naming %q", tc.shell, got, err, tc.wantIn)
				}
				return
			}
			if err != nil {
				t.Fatalf("shellCommand(%q) = %v", tc.shell, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("shellCommand(%q) = %q, want %q", tc.shell, got, tc.want)
			}
		})
	}
}

// TestNormalizeRunScripts converts `run:`/`shell:` into argv cmd, at the step, the fan
// and inside an inline sub-workflow, clearing the script fields so the stored spec is
// argv-only like a hand-written `cmd:` step.
func TestNormalizeRunScripts(t *testing.T) {
	t.Parallel()
	spec := Spec{Steps: []StepSpec{
		{
			Name: "checks", ProjectKey: "self", Agent: "exec", Runner: "local",
			Run: "echo step", Shell: shellSh,
			Fan: []FanSpec{
				{Name: "own", Agent: "exec", Run: "echo fan"},                  // no shell: bash default
				{Name: "kept", Agent: "exec", Cmd: []string{"echo", "keeper"}}, // untouched
				{Name: "inherit", Agent: "exec"},                               // inherits the step's cmd
			},
		},
		{
			Name: "sub", ProjectKey: "self", Agent: "exec", Runner: "local", Type: stepTypeWorkflow,
			SubWorkflow: &Spec{Steps: []StepSpec{{Name: "inner", ProjectKey: "self", Agent: "exec", Runner: "local", Run: "echo inner"}}},
		},
	}}
	got, err := normalizeRunScripts(spec)
	if err != nil {
		t.Fatalf("normalizeRunScripts: %v", err)
	}
	first := got.Steps[0]
	if want := []string{"sh", "-ec", "echo step"}; !reflect.DeepEqual(first.Cmd, want) {
		t.Fatalf("step cmd = %q, want %q", first.Cmd, want)
	}
	if first.Run != "" || first.Shell != "" {
		t.Fatalf("step still carries run/shell: %q / %q", first.Run, first.Shell)
	}
	if want := []string{"bash", "-eo", "pipefail", "-c", "echo fan"}; !reflect.DeepEqual(first.Fan[0].Cmd, want) {
		t.Fatalf("fan run cmd = %q, want %q", first.Fan[0].Cmd, want)
	}
	if want := []string{"echo", "keeper"}; !reflect.DeepEqual(first.Fan[1].Cmd, want) {
		t.Fatalf("fan cmd = %q, want the fan's own %q", first.Fan[1].Cmd, want)
	}
	if len(first.Fan[2].Cmd) != 0 {
		t.Fatalf("fan without cmd/run = %q, want it to inherit the step's cmd", first.Fan[2].Cmd)
	}
	// A fan that sets neither cmd nor run runs the step's command (the existing fallback).
	req := stepToRequestForFan(first, "", 1, 1, 3, "alice")
	if !reflect.DeepEqual(req.Cmd, first.Cmd) {
		t.Fatalf("fan job cmd = %q, want the step's %q", req.Cmd, first.Cmd)
	}
	inner := got.Steps[1].SubWorkflow.Steps[0]
	if want := []string{"bash", "-eo", "pipefail", "-c", "echo inner"}; !reflect.DeepEqual(inner.Cmd, want) {
		t.Fatalf("sub-workflow step cmd = %q, want %q", inner.Cmd, want)
	}
}

// TestNormalizeRunScriptsErrors pins the submit-time rejections of the new fields: cmd
// and run are mutually exclusive (a step and a fan), an unknown shell and `shell: cmd`
// are refused with a forward hint, and `shell:` alone is a misconfiguration.
func TestNormalizeRunScriptsErrors(t *testing.T) {
	t.Parallel()
	mk := func(mut func(*StepSpec)) Spec {
		st := StepSpec{Name: "s", ProjectKey: "self", Agent: "exec", Runner: "local", Cwd: "."}
		mut(&st)
		return Spec{Steps: []StepSpec{st}}
	}
	cases := []struct {
		name    string
		spec    Spec
		wantSub string
	}{
		{"step cmd and run", mk(func(s *StepSpec) {
			s.Cmd = []string{"echo", "argv"}
			s.Run = "echo script"
		}), "both cmd and run"},
		{"fan cmd and run", mk(func(s *StepSpec) {
			s.Run = "echo step"
			s.Fan = []FanSpec{{Name: "a", Agent: "exec", Cmd: []string{"echo", "argv"}, Run: "echo script"}}
		}), "both cmd and run"},
		{"unknown shell", mk(func(s *StepSpec) { s.Run = "echo x"; s.Shell = "fish" }), "unknown shell"},
		{"shell cmd", mk(func(s *StepSpec) { s.Run = "echo x"; s.Shell = "cmd" }), "shell: cmd is not supported"},
		{"shell without run", mk(func(s *StepSpec) { s.Shell = "sh"; s.Cmd = []string{"echo", "x"} }), "without run"},
		{"fan run always is a condition", mk(func(s *StepSpec) {
			s.Fan = []FanSpec{{Name: "a", Agent: "exec", Run: "always"}}
		}), "a fan can not use it"},
		{"uses with run", mk(func(s *StepSpec) {
			s.Uses = "gofer/push"
			s.With = map[string]any{"refs": []any{"main"}}
			s.Run = "echo x"
		}), "may not also set run"},
		{"legacy run always with cmd", mk(func(s *StepSpec) {
			s.Cmd = []string{"echo", "x"}
			s.Run = "always"
		}), "deprecated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeRunScripts(tc.spec)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("normalizeRunScripts = %v, want an error naming %q", err, tc.wantSub)
			}
		})
	}
}

// TestLegacyRunAlwaysCondition: the old condition spelling `run: always` (no shell, no
// cmd) is still read as `if: always`, and both the spec warning and the CLI-facing
// helper say so. Once converted the helper has nothing left to warn about.
//
// DEPRECATED(v0.182): remove in v0.185.
func TestLegacyRunAlwaysCondition(t *testing.T) {
	t.Parallel()
	spec := Spec{Steps: []StepSpec{
		{Name: "cleanup", ProjectKey: "self", Agent: "exec", Runner: "local", Run: "always",
			Fan: []FanSpec{{Name: "a", Agent: "exec", Cmd: []string{"true"}}}},
		{Name: "notify", ProjectKey: "self", Agent: "exec", Runner: "local", If: ifAlways, Cmd: []string{"true"}},
	}}
	if warn := LegacyRunAlwaysWarning(spec); !strings.Contains(warn, "step 1") || !strings.Contains(warn, "if: always") {
		t.Fatalf("LegacyRunAlwaysWarning = %q, want it to name step 1 and the new spelling", warn)
	}
	got, err := normalizeRunScripts(spec)
	if err != nil {
		t.Fatalf("normalizeRunScripts: %v", err)
	}
	if got.Steps[0].If != ifAlways || got.Steps[0].Run != "" {
		t.Fatalf("legacy step = if %q run %q, want if always and an emptied run", got.Steps[0].If, got.Steps[0].Run)
	}
	if got.Steps[1].If != ifAlways {
		t.Fatalf("if: always step = %q, want always", got.Steps[1].If)
	}
	if warn := LegacyRunAlwaysWarning(got); warn != "" {
		t.Fatalf("LegacyRunAlwaysWarning(after conversion) = %q, want none", warn)
	}
}

// TestLegacyRunAlwaysStepRunsAfterFailure submits the old spelling end to end: the
// condition still applies (the cleanup step runs after the failed one, the workflow
// ends failed). The step takes its command from its fans, because the legacy condition
// and a step-level cmd: may not share a layer (`if: always` is the way to write both).
func TestLegacyRunAlwaysStepRunsAfterFailure(t *testing.T) {
	t.Parallel()
	e := newTestEngine(t, t.TempDir())
	cleanup := StepSpec{Name: "cleanup", ProjectKey: "self", Agent: "exec", Runner: "local", Cwd: ".", TimeoutSec: 30,
		Run: "always", Fan: []FanSpec{
			{Name: "one", Agent: "exec", Cmd: []string{"sh", "-c", "echo one"}},
			{Name: "two", Agent: "exec", Cmd: []string{"sh", "-c", "echo two"}},
		}}
	wf, err := e.SubmitWorkflow(Spec{Steps: []StepSpec{failStep("boom"), cleanup}}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	if final := waitWorkflow(t, e, wf.ID); final.Status != jobstore.WorkflowFailed {
		t.Fatalf("workflow status = %s, want failed (the legacy condition must not change the outcome)", final.Status)
	}
	steps, err := e.WorkflowSteps(wf.ID)
	if err != nil {
		t.Fatalf("WorkflowSteps: %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("steps = %+v, want the failed step and both legacy run: always fans to have run", steps)
	}
	for _, st := range steps[1:] {
		if st.Status != "done" {
			t.Fatalf("step row = %+v, want done", st)
		}
	}
}

// TestRunScriptSubmitConvertsToCmd submits a `run:` step and checks the stored spec holds
// argv (the worker never sees a script) and that the script really executed.
func TestRunScriptSubmitConvertsToCmd(t *testing.T) {
	t.Parallel()
	e := newTestEngine(t, t.TempDir())
	step := echoStep("script")
	step.Cmd = nil
	step.Run = "echo first\necho second"
	step.Shell = shellSh
	wf, err := e.SubmitWorkflow(Spec{Steps: []StepSpec{step}}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	if final := waitWorkflow(t, e, wf.ID); final.Status != jobstore.WorkflowDone {
		t.Fatalf("workflow status = %s, want done", final.Status)
	}
	var stored Spec
	if err := json.Unmarshal([]byte(wf.SpecJSON), &stored); err != nil {
		t.Fatalf("decode stored spec: %v", err)
	}
	if want := []string{"sh", "-ec", "echo first\necho second"}; !reflect.DeepEqual(stored.Steps[0].Cmd, want) {
		t.Fatalf("stored cmd = %q, want %q", stored.Steps[0].Cmd, want)
	}
	if stored.Steps[0].Run != "" || stored.Steps[0].Shell != "" {
		t.Fatalf("stored step still carries run/shell: %+v", stored.Steps[0])
	}
}

// TestParseRunBlockYAML: a YAML block scalar (`run: |`) keeps its lines and `if: always`
// decodes into the condition field, while `shell:` names the interpreter.
func TestParseRunBlockYAML(t *testing.T) {
	t.Parallel()
	body := `title: block
steps:
  - name: checks
    project_key: self
    agent: exec
    runner: local
    run: |
      echo one
      echo two
    shell: sh
  - name: cleanup
    project_key: self
    agent: exec
    runner: local
    if: always
    run: "echo done"
`
	path := filepath.Join(t.TempDir(), "wf.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write workflow file: %v", err)
	}
	spec, err := ParseWorkflowFile(path)
	if err != nil {
		t.Fatalf("ParseWorkflowFile: %v", err)
	}
	if got := spec.Steps[0].Run; !strings.Contains(got, "echo one\necho two") {
		t.Fatalf("step run = %q, want both block lines", got)
	}
	if spec.Steps[0].Shell != shellSh {
		t.Fatalf("step shell = %q, want sh", spec.Steps[0].Shell)
	}
	if spec.Steps[1].If != ifAlways {
		t.Fatalf("step if = %q, want always", spec.Steps[1].If)
	}
	if warn := LegacyRunAlwaysWarning(spec); warn != "" {
		t.Fatalf("LegacyRunAlwaysWarning = %q, want none for if: always", warn)
	}
	got, err := normalizeRunScripts(spec)
	if err != nil {
		t.Fatalf("normalizeRunScripts: %v", err)
	}
	if want := []string{"sh", "-ec", "echo one\necho two\n"}; !reflect.DeepEqual(got.Steps[0].Cmd, want) {
		t.Fatalf("normalized cmd = %q, want %q", got.Steps[0].Cmd, want)
	}
	if want := []string{"bash", "-eo", "pipefail", "-c", "echo done"}; !reflect.DeepEqual(got.Steps[1].Cmd, want) {
		t.Fatalf("cleanup cmd = %q, want %q", got.Steps[1].Cmd, want)
	}
}

// TestSubmitRejectsCmdAndRun checks the conflict surfaces as a single-job 400 (the
// admission error kind), not as a silent override.
func TestSubmitRejectsCmdAndRun(t *testing.T) {
	t.Parallel()
	e := newTestEngine(t, t.TempDir())
	step := echoStep("both")
	step.Run = "echo script"
	if _, err := e.SubmitWorkflow(Spec{Steps: []StepSpec{step}}, "alice"); err == nil {
		t.Fatal("SubmitWorkflow = nil, want the cmd/run conflict to be rejected")
	} else if !strings.Contains(err.Error(), "both cmd and run") {
		t.Fatalf("SubmitWorkflow error = %v, want it to name the cmd/run conflict", err)
	}
}

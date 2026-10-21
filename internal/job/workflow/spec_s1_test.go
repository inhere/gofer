package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/store"
	gotemplate "github.com/inhere/gofer/internal/template"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// ---------------------------------------------------------------------------
// S1: Spec.Project default, per-fan cmd/env/cwd/timeout, named fan references,
// the parsed-but-not-yet-executed step fields and the rendering rules.
// ---------------------------------------------------------------------------

// fanJobOf returns the step job of a 1-based (step, fan) pair.
func fanJobOf(t *testing.T, jobs []jobstore.JobRecord, step, fan int) jobstore.JobRecord {
	t.Helper()
	for _, j := range jobs {
		if j.StepIndex == step && j.FanIndex == fan {
			return j
		}
	}
	t.Fatalf("no job for step %d fan %d in %d jobs", step, fan, len(jobs))
	return jobstore.JobRecord{}
}

// jobStdout reads a job's stdout tail.
func jobStdout(t *testing.T, e *Engine, jobID string) string {
	t.Helper()
	data, err := e.ops.TailLog(jobID, store.StreamStdout, 64*1024)
	if err != nil {
		t.Fatalf("read stdout of %s: %v", jobID, err)
	}
	return string(data)
}

// TestStepToRequestPerFanOverrides proves a fan's cmd / env / cwd / timeout_sec
// override the step's values, and that every unset one falls back to the step.
func TestStepToRequestPerFanOverrides(t *testing.T) {
	t.Parallel()
	step := StepSpec{
		Name: "checks", ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"base"}, Env: map[string]string{"A": "1"}, Cwd: ".", TimeoutSec: 30,
		Fan: []FanSpec{
			{Name: "unit", Cmd: []string{"one"}, Env: map[string]string{"GOOS": "darwin"}},
			{Name: "web", Cwd: "web", Env: map[string]string{"C": "3"}, TimeoutSec: 60},
		},
	}
	first := stepToRequestForFan(step, "wf-1", 1, 1, 1, "alice")
	if len(first.Cmd) != 1 || first.Cmd[0] != "one" {
		t.Fatalf("fan 1 cmd = %v, want [one]", first.Cmd)
	}
	if first.Env["A"] != "1" || first.Env["GOOS"] != "darwin" {
		t.Fatalf("fan 1 env = %v, want the step env layered with the fan env", first.Env)
	}
	if first.Cwd != "." || first.TimeoutSec != 30 || first.Agent != "exec" {
		t.Fatalf("fan 1 cwd/timeout/agent = %q/%d/%q, want ./30/exec (step fallback)", first.Cwd, first.TimeoutSec, first.Agent)
	}

	second := stepToRequestForFan(step, "wf-1", 1, 1, 2, "alice")
	if len(second.Cmd) != 1 || second.Cmd[0] != "base" {
		t.Fatalf("fan 2 cmd = %v, want the step cmd [base]", second.Cmd)
	}
	if second.Cwd != "web" || second.TimeoutSec != 60 {
		t.Fatalf("fan 2 cwd/timeout = %q/%d, want web/60", second.Cwd, second.TimeoutSec)
	}
	if second.Env["A"] != "1" || second.Env["C"] != "3" {
		t.Fatalf("fan 2 env = %v, want the step env layered with the fan env", second.Env)
	}
}

// TestSpecProjectDefaultsStepProject proves a step without project_key runs in
// Spec.Project and still gets its runner, and that an exec fan without agent but with
// its own cmd defaults to the built-in exec agent (design §一·1, §一·2).
func TestSpecProjectDefaultsStepProject(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{
		"self": {HostPath: t.TempDir(), AllowExec: true},
	}}
	spec := Spec{Project: "self", Steps: []StepSpec{
		{Name: "checks", Fan: []FanSpec{
			{Name: "unit", Cmd: []string{"go", "test"}},
			{Name: "vet", Cmd: []string{"go", "vet"}},
		}},
	}}
	got, err := defaultStepRunners(spec, cfg)
	if err != nil {
		t.Fatalf("defaultStepRunners: %v", err)
	}
	st := got.Steps[0]
	if st.ProjectKey != "self" {
		t.Fatalf("step project_key = %q, want the spec default self", st.ProjectKey)
	}
	if st.Runner != config.BuiltinLocalRunner {
		t.Fatalf("step runner = %q, want %q", st.Runner, config.BuiltinLocalRunner)
	}
	for _, f := range st.Fan {
		if f.Agent != "exec" {
			t.Fatalf("fan %q agent = %q, want exec (a fan with its own cmd may omit it)", f.Name, f.Agent)
		}
	}
}

// TestSubmitFanPerFanCmdEnvCwd submits a step whose fans differ in cmd / env / cwd and
// then resolves one fan's stdout by NAME from the next step, with join=all making the
// whole step succeed (design §一·2, §一·9).
func TestSubmitFanPerFanCmdEnvCwd(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "webdir"), 0o755); err != nil {
		t.Fatalf("mkdir webdir: %v", err)
	}
	e := newTestEngine(t, root)
	wf, err := e.SubmitWorkflow(Spec{
		Project: "self",
		Steps: []StepSpec{
			{
				Name: "checks", Agent: "exec", Runner: "local", Join: joinAll,
				Fan: []FanSpec{
					{Name: "env", Cmd: testcmd.Cmd(t, "env-print", "CHECK_FAN"), Env: map[string]string{"CHECK_FAN": "unit-ok"}},
					{Name: "cwd", Cmd: testcmd.Cmd(t, "env-cwd"), Env: map[string]string{"DAB_TEST_VAR": "cwd-ok"}, Cwd: "webdir", TimeoutSec: 60},
				},
			},
			{
				Name: "report", Agent: "exec", Runner: "local", Cwd: ".",
				Cmd:        append(testcmd.Cmd(t, "argv"), "${steps.checks.env.stdout}"),
				TimeoutSec: 60,
			},
		},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("workflow = %s (err=%s), want done", final.Status, final.Error)
	}
	jobs, err := e.meta.ListWorkflowJobs(wf.ID)
	if err != nil {
		t.Fatalf("list workflow jobs: %v", err)
	}
	// Fan 1 ran with its own env; fan 2 with its own cwd (the env-cwd helper prints the
	// variable, then the working directory it really got).
	if out := jobStdout(t, e, fanJobOf(t, jobs, 1, 1).ID); !strings.Contains(out, "CHECK_FAN=unit-ok") {
		t.Fatalf("fan env stdout = %q, want CHECK_FAN=unit-ok", out)
	}
	cwdOut := jobStdout(t, e, fanJobOf(t, jobs, 1, 2).ID)
	if !strings.Contains(cwdOut, "cwd-ok") || !strings.Contains(filepath.ToSlash(cwdOut), "webdir") {
		t.Fatalf("fan cwd stdout = %q, want cwd-ok and a .../webdir working directory", cwdOut)
	}
	// Step 2 named the fan by NAME (${steps.checks.env.stdout}) and got its output.
	if out := jobStdout(t, e, fanJobOf(t, jobs, 2, 0).ID); !strings.Contains(out, "CHECK_FAN=unit-ok") {
		t.Fatalf("step 2 stdout = %q, want the named fan's stdout as its argv", out)
	}
}

// TestJoinAllOneFanFails proves join=all still fails the step (and the workflow) when
// only ONE of several per-fan commands fails, and that the next step never runs.
func TestJoinAllOneFanFails(t *testing.T) {
	t.Parallel()
	e := newTestEngine(t, t.TempDir())
	wf, err := e.SubmitWorkflow(Spec{
		Project: "self",
		Steps: []StepSpec{
			{
				Name: "checks", Agent: "exec", Runner: "local", Join: joinAll,
				Fan: []FanSpec{
					{Name: "ok", Cmd: testcmd.Cmd(t, "exit", "0")},
					{Name: "bad", Cmd: testcmd.Cmd(t, "exit", "3")},
				},
			},
			{Name: "never", Agent: "exec", Runner: "local", Cmd: testcmd.Cmd(t, "exit", "0")},
		},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowFailed {
		t.Fatalf("workflow = %s, want failed (one fan exited 3)", final.Status)
	}
	jobs, _ := e.meta.ListWorkflowJobs(wf.ID)
	if n := len(fanJobsAtStep(jobs, 2)); n != 0 {
		t.Fatalf("step 2 started %d job(s) after a failed join=all step", n)
	}
}

// TestValidateStepFeatures: the spec fields whose execution lands even later (S3/S4) are
// rejected with a clear "not supported yet" instead of being silently ignored, while the
// ones S2 implemented (ref, env — on any runner, since the v26 dispatch carries both) pass.
func TestValidateStepFeatures(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Runners: map[string]config.RunnerConfig{"w1": {Type: "worker"}},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: ".", AllowExec: true},
		},
	}
	mk := func(mut func(*StepSpec)) Spec {
		st := StepSpec{Name: "s", ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"true"}}
		mut(&st)
		return Spec{Steps: []StepSpec{st}}
	}
	cases := []struct {
		name    string
		spec    Spec
		wantErr string
	}{
		// S3: uses / with execute now — the built-in step is validated (name + strictly
		// decoded with block) instead of being refused as unsupported.
		{"uses unknown step", mk(func(s *StepSpec) { s.Cmd = nil; s.Uses = "gofer/nope" }), "unknown built-in step"},
		{"uses not gofer", mk(func(s *StepSpec) { s.Cmd = nil; s.Uses = "actions/checkout@v4" }), "not a built-in step"},
		{"uses bad with", mk(func(s *StepSpec) {
			s.Cmd = nil
			s.Uses = "gofer/wait-ci"
			s.With = map[string]any{"refer": "v1"}
		}), "with"},
		{"uses push without allow_push", mk(func(s *StepSpec) {
			s.Cmd = nil
			s.Uses = "gofer/push"
			s.With = map[string]any{"refs": []any{"main"}}
		}), "allow_push"},
		{"with without uses", mk(func(s *StepSpec) { s.With = map[string]any{"refs": []any{"main"}} }), "with"},
		{"uses wait-ci", mk(func(s *StepSpec) {
			s.Cmd = nil
			s.Uses = "gofer/wait-ci"
			s.With = map[string]any{"ref": "v1"}
		}), ""},
		// S4: `if: always` executes (a fail-fast jump to the always steps), so it passes;
		// only an unknown value is still rejected (`run`/`shell` are normalized before this).
		{"if always", mk(func(s *StepSpec) { s.If = "always" }), ""},
		{"unknown if", mk(func(s *StepSpec) { s.If = "sometimes" }), "unknown if"},
		// S2: ref and env execute now, on a worker runner too (dispatch v26).
		{"ref", mk(func(s *StepSpec) { s.Ref = "HEAD~1" }), ""},
		{"env on worker", mk(func(s *StepSpec) { s.Runner = "w1"; s.Env = map[string]string{"GOOS": "darwin"} }), ""},
		{"fan env on worker", mk(func(s *StepSpec) {
			s.Fan = []FanSpec{{Name: "a", Runner: "w1", Cmd: []string{"true"}, Env: map[string]string{"GOOS": "darwin"}}}
		}), ""},
		{"env on local runner", mk(func(s *StepSpec) { s.Env = map[string]string{"GOOS": "darwin"} }), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateStepFeatures(tc.spec, cfg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateStepFeatures = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateStepFeatures = nil, want an error naming %q", tc.wantErr)
			}
			assertInvalidRequest(t, err)
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to name %q", err, tc.wantErr)
			}
		})
	}
}

// TestValidateFanNames covers the fan-name rules: identifier shape, no reserved
// selector spelling, unique within the step, and agent-or-cmd present.
func TestValidateFanNames(t *testing.T) {
	t.Parallel()
	mk := func(fans ...FanSpec) Spec {
		return Spec{Steps: []StepSpec{{
			Name: "checks", ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"true"}, Join: joinAll, Fan: fans,
		}}}
	}
	cases := []struct {
		name    string
		spec    Spec
		wantErr bool
	}{
		{"named fans", mk(FanSpec{Name: "unit", Cmd: []string{"a"}}, FanSpec{Name: "vet-darwin", Cmd: []string{"b"}}), false},
		{"unnamed fans", mk(FanSpec{Cmd: []string{"a"}}, FanSpec{Agent: "exec", Cmd: []string{"b"}}), false},
		{"duplicate name", mk(FanSpec{Name: "unit", Cmd: []string{"a"}}, FanSpec{Name: "unit", Cmd: []string{"b"}}), true},
		{"reserved name", mk(FanSpec{Name: "all", Cmd: []string{"a"}}), true},
		{"positional name", mk(FanSpec{Name: "f2", Cmd: []string{"a"}}), true},
		{"bad identifier", mk(FanSpec{Name: "has space", Cmd: []string{"a"}}), true},
		{"neither agent nor cmd", mk(FanSpec{Name: "unit"}), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFanout(tc.spec)
			if tc.wantErr && err == nil {
				t.Fatalf("validateFanout(%s) = nil, want an error", tc.name)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateFanout(%s) = %v, want nil", tc.name, err)
			}
		})
	}
}

// TestValidateRefsNewFields proves the static reference check covers the new fields
// (env values, per-fan cmd / cwd / env, ref) and resolves a fan by NAME.
func TestValidateRefsNewFields(t *testing.T) {
	t.Parallel()
	checks := StepSpec{
		Name: "checks", ProjectKey: "self", Agent: "exec", Runner: "local", Join: joinAll,
		Fan: []FanSpec{{Name: "unit", Cmd: []string{"go", "test"}}},
	}
	next := func(mut func(*StepSpec)) Spec {
		st := StepSpec{Name: "report", ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"true"}}
		mut(&st)
		return Spec{Steps: []StepSpec{checks, st}}
	}
	cases := []struct {
		name    string
		spec    Spec
		wantErr string
	}{
		{"env value refs a named fan", next(func(s *StepSpec) {
			s.Env = map[string]string{"OUT": "${steps.checks.unit.stdout}"}
		}), ""},
		{"fan cmd refs a named fan", next(func(s *StepSpec) {
			s.Fan = []FanSpec{{Name: "sub", Cmd: []string{"cat", "${steps.checks.unit.result_dir}"}}}
			s.Join = joinAll
		}), ""},
		{"unknown fan name", next(func(s *StepSpec) {
			s.Cmd = []string{"cat", "${steps.checks.web.stdout}"}
		}), "no fan named"},
		{"forward ref in env", Spec{Steps: []StepSpec{{
			Name: "first", ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"true"}, Env: map[string]string{"OUT": "${steps.second.stdout}"},
		}, {
			Name: "second", ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"true"},
		}}}, "not a prior step"},
		{"unknown field in fan cwd", next(func(s *StepSpec) {
			s.Fan = []FanSpec{{Name: "sub", Cmd: []string{"true"}, Cwd: "${steps.checks.unit.nope}"}}
			s.Join = joinAll
		}), "unknown field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRefs(tc.spec)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateRefs = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateRefs = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// TestRenderSpecNewFields: the new fields (project, env, uses, with, fan name / cmd /
// env / cwd) are rendered, and a placeholder left in one of them is rejected.
func TestRenderSpecNewFields(t *testing.T) {
	t.Parallel()
	spec := Spec{
		Title:   "verify ${vars.commit}",
		Project: "${vars.project}",
		Vars: map[string]gotemplate.Var{
			"project": {Required: true, Desc: "project key"},
			"commit":  {Required: true, Desc: "commit to verify"},
			"tool":    {Default: "gofer/push", Desc: "built-in step"},
		},
		Steps: []StepSpec{{
			Name: "checks", Agent: "exec", Runner: "local", Join: joinAll,
			Uses: "${vars.tool}",
			Env:  map[string]string{"TARGET": "${vars.commit}"},
			With: map[string]any{"refs": []any{"main", "v${vars.commit}"}, "deep": map[string]any{"ref": "${vars.commit}"}},
			Fan: []FanSpec{
				{Name: "unit", Cmd: []string{"go", "test", "-run", "${vars.commit}"}},
				{Name: "web", Cwd: "web", Env: map[string]string{"TARGET": "${vars.commit}"}},
			},
		}},
	}
	got, err := renderWorkflowTemplate(spec, map[string]string{"project": "self", "commit": "abc123"})
	if err != nil {
		t.Fatalf("renderWorkflowTemplate: %v", err)
	}
	if got.Project != "self" || got.Title != "verify abc123" {
		t.Fatalf("rendered project/title = %q/%q, want self/verify abc123", got.Project, got.Title)
	}
	st := got.Steps[0]
	if st.Uses != "gofer/push" {
		t.Fatalf("rendered uses = %q, want gofer/push", st.Uses)
	}
	if st.Env["TARGET"] != "abc123" {
		t.Fatalf("rendered env = %v, want TARGET=abc123", st.Env)
	}
	refs, _ := st.With["refs"].([]any)
	if len(refs) != 2 || refs[1] != "vabc123" {
		t.Fatalf("rendered with.refs = %v, want [main vabc123]", st.With["refs"])
	}
	deep, _ := st.With["deep"].(map[string]any)
	if deep["ref"] != "abc123" {
		t.Fatalf("rendered with.deep = %v, want ref=abc123", st.With["deep"])
	}
	if st.Fan[0].Cmd[3] != "abc123" || st.Fan[1].Env["TARGET"] != "abc123" {
		t.Fatalf("rendered fan fields = %+v / %+v, want the commit substituted", st.Fan[0], st.Fan[1])
	}

	// A placeholder naming an undeclared variable survives rendering and is rejected.
	broken := Spec{
		Vars:  map[string]gotemplate.Var{"project": {Required: true}},
		Steps: []StepSpec{{Name: "s", ProjectKey: "${vars.project}", Agent: "exec", Cmd: []string{"true"}, Env: map[string]string{"X": "${vars.gone}"}}},
	}
	if _, err := renderWorkflowTemplate(broken, map[string]string{"project": "self"}); err == nil || !strings.Contains(err.Error(), "${vars.gone}") {
		t.Fatalf("renderWorkflowTemplate with a leftover placeholder = %v, want an undefined variable error", err)
	}
}

// TestParseWorkflowFileNewFields: a yaml file with a top-level project and per-fan
// cmd / env / cwd / timeout_sec decodes into the spec.
func TestParseWorkflowFileNewFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "verify.yaml")
	body := "title: verify\n" +
		"project: self\n" +
		"steps:\n" +
		"  - name: checks\n" +
		"    agent: exec\n" +
		"    runner: local\n" +
		"    join: all\n" +
		"    ref: HEAD~1\n" +
		"    env:\n" +
		"      GOFLAGS: -count=1\n" +
		"    fan:\n" +
		"      - {name: unit, cmd: [go, test, ./...]}\n" +
		"      - {name: web, cwd: web, timeout_sec: 120, cmd: [sh, -c, \"npx vitest run\"]}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write workflow file: %v", err)
	}
	spec, err := ParseWorkflowFile(path)
	if err != nil {
		t.Fatalf("ParseWorkflowFile: %v", err)
	}
	if spec.Project != "self" {
		t.Fatalf("spec project = %q, want self", spec.Project)
	}
	st := spec.Steps[0]
	if st.Ref != "HEAD~1" || st.Env["GOFLAGS"] != "-count=1" {
		t.Fatalf("step ref/env = %q/%v", st.Ref, st.Env)
	}
	if len(st.Fan) != 2 || st.Fan[0].Name != "unit" || st.Fan[1].Name != "web" {
		t.Fatalf("step fan = %+v, want the two named fans", st.Fan)
	}
	if len(st.Fan[0].Cmd) != 3 || st.Fan[1].Cwd != "web" || st.Fan[1].TimeoutSec != 120 {
		t.Fatalf("fan fields = %+v, want unit cmd and web cwd/timeout", st.Fan)
	}
}

// TestMergeStepFromMarkdownNewFields: the md-per-step frontmatter fills the new step
// fields when the inline step left them empty, and the inline step still wins.
func TestMergeStepFromMarkdownNewFields(t *testing.T) {
	t.Parallel()
	fromMD := StepSpec{
		Name: "from-md", Env: map[string]string{"A": "1"}, Ref: "main",
		Uses: "gofer/push", If: "always", Run: "echo hi", Shell: "sh", With: map[string]any{"refs": []any{"main"}},
	}
	var empty StepSpec
	mergeStepFromMarkdown(&empty, fromMD, "body prompt")
	if empty.Env["A"] != "1" || empty.Ref != "main" || empty.Uses != "gofer/push" || empty.If != "always" ||
		empty.Run != "echo hi" || empty.Shell != "sh" || len(empty.With) != 1 {
		t.Fatalf("merged step = %+v, want every md field applied", empty)
	}
	inline := StepSpec{Env: map[string]string{"B": "2"}, Ref: "dev", Uses: "gofer/other", With: map[string]any{"tag": "v1"}}
	mergeStepFromMarkdown(&inline, fromMD, "body prompt")
	if inline.Env["B"] != "2" || inline.Env["A"] != "" || inline.Ref != "dev" || inline.Uses != "gofer/other" || inline.With["tag"] != "v1" {
		t.Fatalf("merged step = %+v, want the inline fields to win", inline)
	}
}

// TestFanJobTitleNamesTheFan: each fan's job is titled "<step> · <fan name>", so the
// job list tells the fans of one step apart; a fan without a name keeps the step name.
func TestFanJobTitleNamesTheFan(t *testing.T) {
	step := StepSpec{
		Name: "checks",
		Cmd:  []string{"go", "version"},
		Fan:  []FanSpec{{Name: "linux-race"}, {}},
	}
	if got := stepToRequestForFan(step, "wf-x", 1, 1, 1, "").Title; got != "checks · linux-race" {
		t.Fatalf("fan 1 title = %q, want %q", got, "checks · linux-race")
	}
	if got := stepToRequestForFan(step, "wf-x", 1, 1, 2, "").Title; got != "checks" {
		t.Fatalf("unnamed fan title = %q, want the step name", got)
	}
}

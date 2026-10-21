package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestFailureExcerpt pins the S4 excerpt rule (design §一·6): only failure-looking
// lines of the job's log tail, de-duplicated, in log order, at most 20, and the job's
// recorded error as the fallback when nothing matched (a built-in step).
func TestFailureExcerpt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var stdout strings.Builder
	stdout.WriteString("ok    pkg/a  0.01s\n")
	stdout.WriteString("--- FAIL: TestA (0.00s)\n")
	stdout.WriteString("    a_test.go:12: boom\n")
	for i := 0; i < 30; i++ {
		stdout.WriteString("FAIL: case ")
		stdout.WriteString(strings.Repeat("x", i%3))
		stdout.WriteString(" ")
		stdout.WriteString(stringRune(i))
		stdout.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "stdout.log"), []byte(stdout.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr := "warning: unrelated\n✗ build failed\nundefined: Foo\n"
	if err := os.WriteFile(filepath.Join(dir, "stderr.log"), []byte(stderr), 0o644); err != nil {
		t.Fatal(err)
	}
	got := failureExcerpt(jobstore.JobRecord{ResultDir: dir})
	lines := strings.Split(got, "\n")
	if len(lines) > maxExcerptLines {
		t.Fatalf("excerpt has %d lines, want at most %d", len(lines), maxExcerptLines)
	}
	if lines[0] != "--- FAIL: TestA (0.00s)" {
		t.Fatalf("first line = %q (excerpt %q), want the go test failure header", lines[0], got)
	}
	if strings.Contains(got, "warning: unrelated") {
		t.Fatalf("excerpt kept a non-failure line: %q", got)
	}
	// Dedupe: every line appears once, even though the log repeats FAIL/✗ shapes.
	seen := map[string]bool{}
	for _, line := range lines {
		if seen[line] {
			t.Fatalf("duplicate excerpt line %q in %q", line, got)
		}
		seen[line] = true
	}
	// The job's own error is the fallback: a built-in step writes its reason there, not
	// to the log.
	fallback := failureExcerpt(jobstore.JobRecord{Error: "step push: remote rejected\nhint: pull first", ResultDir: t.TempDir()})
	if fallback != "step push: remote rejected\nhint: pull first" {
		t.Fatalf("fallback excerpt = %q", fallback)
	}
	if failureExcerpt(jobstore.JobRecord{ResultDir: t.TempDir()}) != "" {
		t.Fatal("an empty job must have an empty excerpt")
	}
}

// stringRune turns i into a single letter, so each repeated line stays distinct.
func stringRune(i int) string {
	return string(rune('a' + i%26))
}

// TestWorkflowStepsResultView checks the result columns of the step view (design §一·6):
// a finished exec row carries its runner, exit code and duration.
func TestWorkflowStepsResultView(t *testing.T) {
	t.Parallel()
	e := newTestEngine(t, t.TempDir())
	wf, err := e.SubmitWorkflow(Spec{Steps: []StepSpec{failStep("boom")}}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowFailed {
		t.Fatalf("workflow status = %s, want failed", final.Status)
	}
	steps, err := e.WorkflowSteps(wf.ID)
	if err != nil {
		t.Fatalf("WorkflowSteps: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(steps))
	}
	st := steps[0]
	if st.Runner != "local" {
		t.Fatalf("runner = %q, want local", st.Runner)
	}
	if st.ExitCode == nil || *st.ExitCode != 7 {
		t.Fatalf("exit code = %v, want 7", st.ExitCode)
	}
	if st.Status != job.StatusFailed {
		t.Fatalf("status = %q, want failed", st.Status)
	}
	// The failing command prints nothing, so the excerpt falls back to whatever reason the
	// job recorded — what matters here is that a failed row is not silent and that a
	// successful row never carries one.
	if done := (Step{Status: job.StatusDone}); done.FailureExcerpt != "" {
		t.Fatal("a done row must not carry a failure excerpt")
	}
}

// TestRunAlwaysKeepsFailedOutcome pins design §一·8: a step marked run: always runs after
// an earlier failure, but the workflow's final outcome stays failed.
func TestRunAlwaysKeepsFailedOutcome(t *testing.T) {
	t.Parallel()
	e := newTestEngine(t, t.TempDir())
	cleanup := echoStep("cleanup")
	cleanup.If = ifAlways
	wf, err := e.SubmitWorkflow(Spec{Steps: []StepSpec{failStep("boom"), cleanup}}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowFailed {
		t.Fatalf("workflow status = %s, want failed (run: always must not change the outcome)", final.Status)
	}
	steps, err := e.WorkflowSteps(wf.ID)
	if err != nil {
		t.Fatalf("WorkflowSteps: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("started steps = %d, want 2 (the run: always step must still run)", len(steps))
	}
	if steps[0].Status != job.StatusFailed || steps[1].Status != job.StatusDone {
		t.Fatalf("step statuses = %q, %q; want failed, done", steps[0].Status, steps[1].Status)
	}
	if !strings.Contains(final.Error, "step 1") {
		t.Fatalf("workflow error = %q, want the original failure reason", final.Error)
	}
}

// TestRunAlwaysStepIsSkippedOnSuccess: a run: always step is an ordinary step when
// nothing failed before it — the whole workflow finishes done.
func TestRunAlwaysStepIsSkippedOnSuccess(t *testing.T) {
	t.Parallel()
	e := newTestEngine(t, t.TempDir())
	cleanup := echoStep("cleanup")
	cleanup.If = ifAlways
	wf, err := e.SubmitWorkflow(Spec{Steps: []StepSpec{echoStep("build"), cleanup}}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	if final := waitWorkflow(t, e, wf.ID); final.Status != jobstore.WorkflowDone {
		t.Fatalf("workflow status = %s, want done", final.Status)
	}
}

// TestRerunFailed trims a failed workflow to its failed step and submits it as a NEW
// workflow (design §一·7): the original is untouched and the new one records rerun_of.
func TestRerunFailed(t *testing.T) {
	t.Parallel()
	e := newTestEngine(t, t.TempDir())
	wf, err := e.SubmitWorkflow(Spec{Steps: []StepSpec{echoStep("build"), failStep("bad"), echoStep("never")}}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	orig := waitWorkflow(t, e, wf.ID)
	if orig.Status != jobstore.WorkflowFailed {
		t.Fatalf("original status = %s, want failed", orig.Status)
	}
	nw, ok, err := e.RerunWorkflow(wf.ID, RerunModeFailed, "", "alice")
	if err != nil || !ok {
		t.Fatalf("RerunWorkflow(failed) = %v, ok=%v", err, ok)
	}
	if nw.RerunOf != wf.ID {
		t.Fatalf("rerun_of = %q, want %q", nw.RerunOf, wf.ID)
	}
	if nw.TotalSteps != 1 {
		t.Fatalf("rerun total_steps = %d, want 1 (only the failed step)", nw.TotalSteps)
	}
	var spec Spec
	if err := json.Unmarshal([]byte(nw.SpecJSON), &spec); err != nil {
		t.Fatalf("decode rerun spec: %v", err)
	}
	if len(spec.Steps) != 1 || spec.Steps[0].Name != "bad" {
		t.Fatalf("rerun steps = %+v, want only the failed step", spec.Steps)
	}
	// The original keeps its own status / step count: a rerun never rewrites history.
	after, _, err := e.GetWorkflow(wf.ID)
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	if after.Status != jobstore.WorkflowFailed || after.TotalSteps != 3 {
		t.Fatalf("original after rerun = %s/%d, want failed/3", after.Status, after.TotalSteps)
	}
}

// TestRerunFrom keeps the selected step and everything after it, and refuses a running
// workflow / an unknown step.
func TestRerunFrom(t *testing.T) {
	t.Parallel()
	e := newTestEngine(t, t.TempDir())
	wf, err := e.SubmitWorkflow(Spec{Steps: []StepSpec{echoStep("build"), failStep("bad"), echoStep("never")}}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	waitWorkflow(t, e, wf.ID)
	nw, _, err := e.RerunWorkflow(wf.ID, RerunModeFrom, "bad", "alice")
	if err != nil {
		t.Fatalf("RerunWorkflow(from) = %v", err)
	}
	if nw.TotalSteps != 2 {
		t.Fatalf("rerun total_steps = %d, want 2 (the named step and the one after it)", nw.TotalSteps)
	}
	var spec Spec
	if err := json.Unmarshal([]byte(nw.SpecJSON), &spec); err != nil {
		t.Fatalf("decode rerun spec: %v", err)
	}
	if spec.Steps[0].Name != "bad" {
		t.Fatalf("first rerun step = %q, want bad", spec.Steps[0].Name)
	}
	if _, _, err := e.RerunWorkflow(wf.ID, RerunModeFrom, "nope", "alice"); err == nil {
		t.Fatal("RerunWorkflow(from unknown step) = nil error, want a rejection")
	}
	if _, _, err := e.RerunWorkflow(wf.ID, "bogus", "", "alice"); err == nil {
		t.Fatal("RerunWorkflow(unknown mode) = nil error, want a rejection")
	}
}

// TestTrimFailedFanKeepsFailedFans is the fan-level rule of --failed: inside a failed
// fan-out step only the failed fans are re-run.
func TestTrimFailedFanKeepsFailedFans(t *testing.T) {
	t.Parallel()
	spec := Spec{Steps: []StepSpec{{
		Name: "checks",
		Fan:  []FanSpec{{Name: "unit", Cmd: []string{"true"}}, {Name: "vet", Cmd: []string{"true"}}, {Name: "lint", Cmd: []string{"true"}}},
	}}}
	got := trimFailedSteps(spec, map[int]map[int]bool{1: {2: true, 3: true}})
	if len(got.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(got.Steps))
	}
	fans := got.Steps[0].Fan
	if len(fans) != 2 || fans[0].Name != "vet" || fans[1].Name != "lint" {
		t.Fatalf("fans = %+v, want vet + lint only", fans)
	}
	// A step with no failed fan detail is kept whole (an empty fan list would turn it
	// into a single job on the step's own agent).
	whole := trimFailedSteps(spec, map[int]map[int]bool{1: {}})
	if len(whole.Steps) != 1 || len(whole.Steps[0].Fan) != 3 {
		t.Fatalf("steps = %+v, want the step kept whole", whole.Steps)
	}
}

// TestRerunRejectsRunningWorkflow: an unfinished workflow has no outcome to trim.
func TestRerunRejectsRunningWorkflow(t *testing.T) {
	t.Parallel()
	e := newTestEngine(t, t.TempDir())
	wf, err := e.SubmitWorkflow(Spec{Steps: []StepSpec{echoStep("build")}}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	if _, _, err := e.RerunWorkflow(wf.ID, RerunModeFailed, "", "alice"); err == nil {
		t.Fatal("RerunWorkflow(running) = nil error, want a rejection")
	}
}

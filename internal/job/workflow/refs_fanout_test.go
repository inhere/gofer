package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// priorFan persists a finished fan job at (stepIndex, fanIndex) with the given status,
// a real result_dir under root, and returns the JobRecord. Mirrors priorStep but sets
// FanIndex so the ref resolver's fan aggregation / fK selector can be exercised.
func priorFan(t *testing.T, e *Engine, root string, stepIndex, fanIndex int, status string) jobstore.JobRecord {
	t.Helper()
	id := "fan-" + status + "-s" + itoa(stepIndex) + "-f" + itoa(fanIndex) + "-" + filepath.Base(t.TempDir())
	resultDir := filepath.Join(root, id)
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		t.Fatalf("mkdir result dir: %v", err)
	}
	rec := jobstore.JobRecord{
		ID: id, ProjectKey: "self", Agent: "exec", Runner: "local",
		Status: status, ResultDir: resultDir, StartedAt: 1,
		WorkflowID: "wf-fan", StepIndex: stepIndex, Attempt: 1, FanIndex: fanIndex,
	}
	if err := e.meta.UpsertJob(rec); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	return rec
}

// TestResolveRefsFanOutResultDirAggregates: ${steps.N.result_dir} on a fan-out step
// returns the newline-joined result_dir of every SUCCESSFUL fan (failed fans excluded).
func TestResolveRefsFanOutResultDirAggregates(t *testing.T) {
	root := t.TempDir()
	e := newTestEngine(t, root)
	f1 := priorFan(t, e, root, 1, 1, job.StatusDone)
	f2 := priorFan(t, e, root, 1, 2, job.StatusDone)
	f3 := priorFan(t, e, root, 1, 3, job.StatusFailed) // failed fan excluded from aggregate
	prior := []jobstore.JobRecord{f1, f2, f3}

	step := &StepSpec{Prompt: "dirs=${steps.1.result_dir}"}
	if err := e.resolveRefs(step, prior); err != nil {
		t.Fatalf("resolveRefs: %v", err)
	}
	want := "dirs=" + f1.ResultDir + "\n" + f2.ResultDir
	if step.Prompt != want {
		t.Fatalf("aggregated result_dir = %q, want %q", step.Prompt, want)
	}
}

// TestResolveRefsFanSelector: ${steps.N.fK.result_dir} resolves the K-th fan's dir.
func TestResolveRefsFanSelector(t *testing.T) {
	root := t.TempDir()
	e := newTestEngine(t, root)
	prior := []jobstore.JobRecord{
		priorFan(t, e, root, 1, 1, job.StatusDone),
		priorFan(t, e, root, 1, 2, job.StatusDone),
		priorFan(t, e, root, 1, 3, job.StatusDone),
	}
	step := &StepSpec{
		Cmd: []string{"run", "--a=${steps.1.f1.result_dir}", "--b=${steps.1.f3.result_dir}"},
	}
	if err := e.resolveRefs(step, prior); err != nil {
		t.Fatalf("resolveRefs: %v", err)
	}
	if step.Cmd[1] != "--a="+prior[0].ResultDir {
		t.Fatalf("f1 = %q, want %q", step.Cmd[1], "--a="+prior[0].ResultDir)
	}
	if step.Cmd[2] != "--b="+prior[2].ResultDir {
		t.Fatalf("f3 = %q, want %q", step.Cmd[2], "--b="+prior[2].ResultDir)
	}
	// The aggregated (no-selector) form returns both done dirs newline-joined.
	step2 := &StepSpec{Prompt: "${steps.1.result_dir}"}
	if err := e.resolveRefs(step2, prior); err != nil {
		t.Fatalf("resolveRefs agg: %v", err)
	}
	if !strings.Contains(step2.Prompt, "\n") {
		t.Fatalf("aggregate should newline-join multiple fans, got %q", step2.Prompt)
	}
}

// TestResolveRefsSingleJobUnchanged: a non-fan step's ${steps.N.result_dir} returns the
// single dir verbatim (NO newline) — the v1 path is preserved (D23).
func TestResolveRefsSingleJobUnchanged(t *testing.T) {
	root := t.TempDir()
	e := newTestEngine(t, root)
	p1 := priorStep(t, e, root, 1, 0, job.StatusDone, "", "")
	step := &StepSpec{Cwd: "${steps.1.result_dir}/work"}
	if err := e.resolveRefs(step, []jobstore.JobRecord{p1}); err != nil {
		t.Fatalf("resolveRefs: %v", err)
	}
	if step.Cwd != p1.ResultDir+"/work" {
		t.Fatalf("single-job result_dir = %q, want %q", step.Cwd, p1.ResultDir+"/work")
	}
}

// TestValidateRefsFanSelector: a fan selector .fK is accepted when K is within the
// referenced step's fan_out, and rejected when K exceeds it.
func TestValidateRefsFanSelector(t *testing.T) {
	// Step 1 has fan_out 3; step 2 references f2 (valid) — accepted.
	ok := Spec{Steps: []StepSpec{
		fanEchoStep("s1", 3, "all"),
		{Name: "s2", Prompt: "${steps.1.f2.result_dir}"},
	}}
	if err := validateRefs(ok); err != nil {
		t.Fatalf("validateRefs rejected a valid fan selector: %v", err)
	}

	// Step 2 references f5 but step 1 has only 3 fans — rejected.
	bad := Spec{Steps: []StepSpec{
		fanEchoStep("s1", 3, "all"),
		{Name: "s2", Prompt: "${steps.1.f5.result_dir}"},
	}}
	err := validateRefs(bad)
	if err == nil {
		t.Fatal("expected rejection of an out-of-range fan selector")
	}
	assertInvalidRequest(t, err)
}

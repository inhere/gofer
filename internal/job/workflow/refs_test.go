package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/store"
)

// priorStep persists a finished step-job for stepIndex with the given outputs and
// returns the JobRecord. ResultDir is a real dir under root so stdout (written to
// <ResultDir>/stdout.log) is readable via TailLog, and e.Get's DB fallback finds
// the row (ResultJSON/ResultDir/ExitCode/Status round-trip from the store).
func priorStep(t *testing.T, e *Engine, root string, stepIndex int, exit int, status, resultJSON, stdout string) jobstore.JobRecord {
	t.Helper()
	id := "prior-" + status + "-" + filepath.Base(t.TempDir()) + "-" + string(rune('0'+stepIndex))
	resultDir := filepath.Join(root, id)
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		t.Fatalf("mkdir result dir: %v", err)
	}
	if stdout != "" {
		if err := os.WriteFile(filepath.Join(resultDir, store.StdoutFile), []byte(stdout), 0o644); err != nil {
			t.Fatalf("write stdout: %v", err)
		}
	}
	rec := jobstore.JobRecord{
		ID: id, ProjectKey: "self", Agent: "exec", Runner: "local",
		Status: status, ExitCode: exit, ResultDir: resultDir, ResultJSON: resultJSON,
		StartedAt: 1, WorkflowID: "wf-x", StepIndex: stepIndex,
	}
	if err := e.meta.UpsertJob(rec); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	return rec
}

// TestResolveRefsScalarsAndPaths covers result_dir/exit_code/status/job_id and a
// field referenced multiple times across prompt+cmd in one resolve.
func TestResolveRefsScalarsAndPaths(t *testing.T) {
	root := t.TempDir()
	e := newTestEngine(t, root)
	p1 := priorStep(t, e, root, 1, 7, job.StatusDone, "", "")
	prior := []jobstore.JobRecord{p1}

	step := &StepSpec{
		Prompt: "dir=${steps.1.result_dir} code=${steps.1.exit_code} again=${steps.1.result_dir}",
		Cmd:    []string{"run", "--status=${steps.1.status}", "--id=${steps.1.job_id}"},
		Cwd:    "${steps.1.result_dir}/work",
	}
	if err := e.resolveRefs(step, prior); err != nil {
		t.Fatalf("resolveRefs: %v", err)
	}
	wantPrompt := "dir=" + p1.ResultDir + " code=7 again=" + p1.ResultDir
	if step.Prompt != wantPrompt {
		t.Fatalf("prompt = %q, want %q", step.Prompt, wantPrompt)
	}
	if step.Cmd[1] != "--status="+job.StatusDone {
		t.Fatalf("cmd status = %q", step.Cmd[1])
	}
	if step.Cmd[2] != "--id="+p1.ID {
		t.Fatalf("cmd job_id = %q", step.Cmd[2])
	}
	if step.Cwd != p1.ResultDir+"/work" {
		t.Fatalf("cwd = %q", step.Cwd)
	}
}

// TestResolveRefsResultJSON resolves ${steps.1.result} to the prior step's
// result.json text verbatim.
func TestResolveRefsResultJSON(t *testing.T) {
	root := t.TempDir()
	e := newTestEngine(t, root)
	body := `{"ok":true,"items":[1,2,3]}`
	p1 := priorStep(t, e, root, 1, 0, job.StatusDone, body, "")

	step := &StepSpec{Prompt: "payload=${steps.1.result}"}
	if err := e.resolveRefs(step, []jobstore.JobRecord{p1}); err != nil {
		t.Fatalf("resolveRefs: %v", err)
	}
	if step.Prompt != "payload="+body {
		t.Fatalf("prompt = %q, want payload=%s", step.Prompt, body)
	}
}

// TestResolveRefsStdout resolves ${steps.2.stdout} to the prior step's stdout tail.
func TestResolveRefsStdout(t *testing.T) {
	root := t.TempDir()
	e := newTestEngine(t, root)
	// step1 is just a placeholder so step2 exists at index 2.
	p1 := priorStep(t, e, root, 1, 0, job.StatusDone, "", "")
	p2 := priorStep(t, e, root, 2, 0, job.StatusDone, "", "captured-stdout-line\n")

	step := &StepSpec{Cmd: []string{"echo", "${steps.2.stdout}"}}
	if err := e.resolveRefs(step, []jobstore.JobRecord{p1, p2}); err != nil {
		t.Fatalf("resolveRefs: %v", err)
	}
	if step.Cmd[1] != "captured-stdout-line\n" {
		t.Fatalf("cmd stdout = %q", step.Cmd[1])
	}
}

// TestResolveRefsResultMissing errors when ${steps.N.result} is used but the prior
// step wrote no result.json.
func TestResolveRefsResultMissing(t *testing.T) {
	root := t.TempDir()
	e := newTestEngine(t, root)
	p1 := priorStep(t, e, root, 1, 0, job.StatusDone, "", "") // no result.json

	step := &StepSpec{Prompt: "${steps.1.result}"}
	err := e.resolveRefs(step, []jobstore.JobRecord{p1})
	if err == nil {
		t.Fatal("expected error for missing result.json")
	}
	if !strings.Contains(err.Error(), "result_dir") {
		t.Fatalf("error should hint result_dir, got: %v", err)
	}
}

// TestResolveRefsResultTooLarge errors when result.json exceeds maxRefInlineBytes.
func TestResolveRefsResultTooLarge(t *testing.T) {
	root := t.TempDir()
	e := newTestEngine(t, root)
	big := strings.Repeat("x", maxRefInlineBytes+1)
	p1 := priorStep(t, e, root, 1, 0, job.StatusDone, big, "")

	step := &StepSpec{Prompt: "${steps.1.result}"}
	err := e.resolveRefs(step, []jobstore.JobRecord{p1})
	if err == nil {
		t.Fatal("expected error for oversize result.json")
	}
	if !strings.Contains(err.Error(), "result_dir") {
		t.Fatalf("error should hint result_dir, got: %v", err)
	}
}

// TestResolveRefsStdoutTooLarge errors when stdout exceeds maxRefInlineBytes.
func TestResolveRefsStdoutTooLarge(t *testing.T) {
	root := t.TempDir()
	e := newTestEngine(t, root)
	big := strings.Repeat("y", maxRefInlineBytes+1)
	p1 := priorStep(t, e, root, 1, 0, job.StatusDone, "", big)

	step := &StepSpec{Prompt: "${steps.1.stdout}"}
	err := e.resolveRefs(step, []jobstore.JobRecord{p1})
	if err == nil {
		t.Fatal("expected error for oversize stdout")
	}
	if !strings.Contains(err.Error(), "result_dir") {
		t.Fatalf("error should hint result_dir, got: %v", err)
	}
}

// TestResolveRefsMissingPriorStep errors when the referenced prior step has no job.
func TestResolveRefsMissingPriorStep(t *testing.T) {
	root := t.TempDir()
	e := newTestEngine(t, root)
	// priorJobs is empty: referencing step 1 must fail (no output produced).
	step := &StepSpec{Prompt: "${steps.1.result_dir}"}
	if err := e.resolveRefs(step, nil); err == nil {
		t.Fatal("expected error for missing prior step")
	}
}

// TestResolveRefsNoRefsUnchanged is a passthrough: a step with no references is
// returned verbatim (no spurious errors / mutation).
func TestResolveRefsNoRefsUnchanged(t *testing.T) {
	root := t.TempDir()
	e := newTestEngine(t, root)
	step := &StepSpec{Prompt: "plain prompt", Cmd: []string{"echo", "hi"}, Cwd: "."}
	if err := e.resolveRefs(step, nil); err != nil {
		t.Fatalf("resolveRefs: %v", err)
	}
	if step.Prompt != "plain prompt" || step.Cmd[1] != "hi" || step.Cwd != "." {
		t.Fatalf("step mutated: %+v", step)
	}
}

// --- P2-b: submit-time validateRefs ---

// TestValidateRefsAccepts passes a spec whose every ref points at an earlier step
// with a valid field.
func TestValidateRefsAccepts(t *testing.T) {
	spec := Spec{Steps: []StepSpec{
		{Name: "s1", Prompt: "no refs here"},
		{Name: "s2", Cmd: []string{"run", "${steps.1.result_dir}"}},
		{Name: "s3", Prompt: "code=${steps.2.exit_code} dir=${steps.1.result_dir}"},
	}}
	if err := validateRefs(spec); err != nil {
		t.Fatalf("validateRefs rejected a valid spec: %v", err)
	}
}

// TestValidateRefsRejectsSelfReference: step 2 referencing ${steps.2.x} is a self
// reference (N must be < this step's index) and is a 400.
func TestValidateRefsRejectsSelfReference(t *testing.T) {
	spec := Spec{Steps: []StepSpec{
		{Name: "s1"},
		{Name: "s2", Prompt: "${steps.2.result_dir}"},
	}}
	err := validateRefs(spec)
	if err == nil {
		t.Fatal("expected rejection of self-reference")
	}
	assertInvalidRequest(t, err)
}

// TestValidateRefsRejectsForwardReference: step 2 referencing ${steps.3.x} names a
// future step and is a 400.
func TestValidateRefsRejectsForwardReference(t *testing.T) {
	spec := Spec{Steps: []StepSpec{
		{Name: "s1"},
		{Name: "s2", Cmd: []string{"x", "${steps.3.status}"}},
		{Name: "s3"},
	}}
	err := validateRefs(spec)
	if err == nil {
		t.Fatal("expected rejection of forward reference")
	}
	assertInvalidRequest(t, err)
}

// TestValidateRefsRejectsUnknownField: ${steps.1.bogus} names a field outside the
// allowed set and is a 400.
func TestValidateRefsRejectsUnknownField(t *testing.T) {
	spec := Spec{Steps: []StepSpec{
		{Name: "s1"},
		{Name: "s2", Prompt: "${steps.1.bogus}"},
	}}
	err := validateRefs(spec)
	if err == nil {
		t.Fatal("expected rejection of unknown field")
	}
	assertInvalidRequest(t, err)
}

// TestValidateRefsRejectsStep1Reference: step 1 has no prior step, so ANY ref in it
// is a 400.
func TestValidateRefsRejectsStep1Reference(t *testing.T) {
	spec := Spec{Steps: []StepSpec{
		{Name: "s1", Prompt: "${steps.1.result_dir}"},
	}}
	err := validateRefs(spec)
	if err == nil {
		t.Fatal("expected rejection of step-1 self/empty reference")
	}
	assertInvalidRequest(t, err)
}

// TestSubmitWorkflowRejectsBadRef proves validateRefs is wired into SubmitWorkflow:
// a forward-reference spec is rejected at submit before any workflow row is created.
func TestSubmitWorkflowRejectsBadRef(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	_, err := e.SubmitWorkflow(Spec{Steps: []StepSpec{
		echoStep("s1"),
		{Name: "s2", ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"sh", "-c", "echo ${steps.3.status}"}, Cwd: ".", TimeoutSec: 30},
		echoStep("s3"),
	}}, "alice")
	if err == nil {
		t.Fatal("expected SubmitWorkflow to reject a forward reference")
	}
	assertInvalidRequest(t, err)
}

func assertInvalidRequest(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, job.ErrInvalidRequest) {
		t.Fatalf("error = %v, want job.ErrInvalidRequest", err)
	}
}

package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/store"
)

// priorStep persists a finished step-job for stepIndex with the given outputs and
// returns the JobRecord. ResultDir is a real dir under root so stdout (written to
// <ResultDir>/stdout.log) is readable via TailLog, and s.Get's DB fallback finds
// the row (ResultJSON/ResultDir/ExitCode/Status round-trip from the store).
func priorStep(t *testing.T, s *Service, root string, stepIndex int, exit int, status, resultJSON, stdout string) jobstore.JobRecord {
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
	if err := s.meta.UpsertJob(rec); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	return rec
}

// TestResolveRefsScalarsAndPaths covers result_dir/exit_code/status/job_id and a
// field referenced multiple times across prompt+cmd in one resolve.
func TestResolveRefsScalarsAndPaths(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	p1 := priorStep(t, s, root, 1, 7, StatusDone, "", "")
	prior := []jobstore.JobRecord{p1}

	step := &StepSpec{
		Prompt: "dir=${steps.1.result_dir} code=${steps.1.exit_code} again=${steps.1.result_dir}",
		Cmd:    []string{"run", "--status=${steps.1.status}", "--id=${steps.1.job_id}"},
		Cwd:    "${steps.1.result_dir}/work",
	}
	if err := s.resolveRefs(step, prior); err != nil {
		t.Fatalf("resolveRefs: %v", err)
	}
	wantPrompt := "dir=" + p1.ResultDir + " code=7 again=" + p1.ResultDir
	if step.Prompt != wantPrompt {
		t.Fatalf("prompt = %q, want %q", step.Prompt, wantPrompt)
	}
	if step.Cmd[1] != "--status="+StatusDone {
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
	s := newTestService(t, root)
	body := `{"ok":true,"items":[1,2,3]}`
	p1 := priorStep(t, s, root, 1, 0, StatusDone, body, "")

	step := &StepSpec{Prompt: "payload=${steps.1.result}"}
	if err := s.resolveRefs(step, []jobstore.JobRecord{p1}); err != nil {
		t.Fatalf("resolveRefs: %v", err)
	}
	if step.Prompt != "payload="+body {
		t.Fatalf("prompt = %q, want payload=%s", step.Prompt, body)
	}
}

// TestResolveRefsStdout resolves ${steps.2.stdout} to the prior step's stdout tail.
func TestResolveRefsStdout(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	// step1 is just a placeholder so step2 exists at index 2.
	p1 := priorStep(t, s, root, 1, 0, StatusDone, "", "")
	p2 := priorStep(t, s, root, 2, 0, StatusDone, "", "captured-stdout-line\n")

	step := &StepSpec{Cmd: []string{"echo", "${steps.2.stdout}"}}
	if err := s.resolveRefs(step, []jobstore.JobRecord{p1, p2}); err != nil {
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
	s := newTestService(t, root)
	p1 := priorStep(t, s, root, 1, 0, StatusDone, "", "") // no result.json

	step := &StepSpec{Prompt: "${steps.1.result}"}
	err := s.resolveRefs(step, []jobstore.JobRecord{p1})
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
	s := newTestService(t, root)
	big := strings.Repeat("x", maxRefInlineBytes+1)
	p1 := priorStep(t, s, root, 1, 0, StatusDone, big, "")

	step := &StepSpec{Prompt: "${steps.1.result}"}
	err := s.resolveRefs(step, []jobstore.JobRecord{p1})
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
	s := newTestService(t, root)
	big := strings.Repeat("y", maxRefInlineBytes+1)
	p1 := priorStep(t, s, root, 1, 0, StatusDone, "", big)

	step := &StepSpec{Prompt: "${steps.1.stdout}"}
	err := s.resolveRefs(step, []jobstore.JobRecord{p1})
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
	s := newTestService(t, root)
	// priorJobs is empty: referencing step 1 must fail (no output produced).
	step := &StepSpec{Prompt: "${steps.1.result_dir}"}
	if err := s.resolveRefs(step, nil); err == nil {
		t.Fatal("expected error for missing prior step")
	}
}

// TestResolveRefsNoRefsUnchanged is a passthrough: a step with no references is
// returned verbatim (no spurious errors / mutation).
func TestResolveRefsNoRefsUnchanged(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	step := &StepSpec{Prompt: "plain prompt", Cmd: []string{"echo", "hi"}, Cwd: "."}
	if err := s.resolveRefs(step, nil); err != nil {
		t.Fatalf("resolveRefs: %v", err)
	}
	if step.Prompt != "plain prompt" || step.Cmd[1] != "hi" || step.Cwd != "." {
		t.Fatalf("step mutated: %+v", step)
	}
}

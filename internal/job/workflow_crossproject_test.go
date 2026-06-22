package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// newCrossProjectService builds a Service with THREE distinct projects (projA/projB/
// projC) each rooted at its own host dir, so a cross-project workflow's ${steps.N.
// result_dir} (an absolute path) is written by one project and read by the next on the
// SAME container filesystem (D20 本地直读). All three allow exec/local.
func newCrossProjectService(t *testing.T, root string) *Service {
	t.Helper()
	mk := func(name string) config.ProjectConfig {
		dir := filepath.Join(root, "host", name)
		return config.ProjectConfig{
			HostPath:       dir,
			AllowedAgents:  []string{"exec"},
			AllowedRunners: []string{"local"},
			AllowExec:      true,
		}
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"projA": mk("projA"),
			"projB": mk("projB"),
			"projC": mk("projC"),
		},
	}
	// The host dirs must exist for cwd "." to resolve.
	for _, p := range cfg.Projects {
		if err := os.MkdirAll(p.HostPath, 0o755); err != nil {
			t.Fatalf("mkdir host dir %s: %v", p.HostPath, err)
		}
	}
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	return NewService(cfg, projReg, agentReg, runners, meta, nil)
}

// TestWorkflowCrossProjectLinearHandoff is the D20 cross-project本地传值 proof: a 3-step
// linear workflow where step1 (projA) writes a result file into ITS result_dir, step2
// (projB) reads ${steps.1.result_dir} (an absolute path on the shared filesystem) and
// asserts the file exists, and step3 (projC) carries step2's exit_code. Each step is a
// DIFFERENT project; the absolute result_dir crosses project boundaries by path on the
// local runner (same container FS), no copy needed.
func TestWorkflowCrossProjectLinearHandoff(t *testing.T) {
	root := t.TempDir()
	s := newCrossProjectService(t, root)

	step1 := StepSpec{
		Name: "genA", ProjectKey: "projA", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", `printf 'A-output' > "$GOFER_RESULT_DIR/payload.txt"`},
		Cwd: ".", TimeoutSec: 30,
	}
	// step2 (projB) reads projA's result_dir BY ABSOLUTE PATH and asserts the file.
	step2 := StepSpec{
		Name: "readB", ProjectKey: "projB", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", `test "$(cat "${steps.1.result_dir}/payload.txt")" = "A-output"`},
		Cwd: ".", TimeoutSec: 30,
	}
	// step3 (projC) carries step2's exit_code in its prompt (persisted into request_json).
	step3 := StepSpec{
		Name: "useC", ProjectKey: "projC", Agent: "exec", Runner: "local",
		Cmd:    []string{"true"},
		Prompt: "projB exit was ${steps.2.exit_code}",
		Cwd:    ".", TimeoutSec: 30,
	}

	wf, err := s.SubmitWorkflow(WorkflowSpec{
		Title: "cross-project",
		Steps: []StepSpec{step1, step2, step3},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}

	final := waitWorkflow(t, s, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("workflow status = %s (err=%s), want done (cross-project handoff)", final.Status, final.Error)
	}

	jobs, err := s.meta.ListWorkflowJobs(wf.ID)
	if err != nil {
		t.Fatalf("ListWorkflowJobs: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("ran %d step jobs, want 3", len(jobs))
	}

	s1 := stepJob(jobs, 1)
	s2 := stepJob(jobs, 2)
	s3 := stepJob(jobs, 3)
	if s1 == nil || s2 == nil || s3 == nil {
		t.Fatalf("missing a step job: %+v", jobs)
	}
	// Each step ran in its own project (distinct result_dir roots).
	if s1.ProjectKey != "projA" || s2.ProjectKey != "projB" || s3.ProjectKey != "projC" {
		t.Fatalf("step projects = %s/%s/%s, want projA/projB/projC", s1.ProjectKey, s2.ProjectKey, s3.ProjectKey)
	}
	// projA's absolute result_dir (under host/projA storage) must appear verbatim in
	// projB's persisted request — proving the cross-project ref resolved to projA's dir.
	if s1.ResultDir == "" {
		t.Fatal("step1 (projA) has no result_dir")
	}
	if !strings.Contains(s2.RequestJSON, s1.ResultDir) {
		t.Fatalf("step2 (projB) request does not contain step1 (projA) result_dir %q:\n%s", s1.ResultDir, s2.RequestJSON)
	}
	// step2 succeeded (exit 0), so step3's prompt carries "projB exit was 0".
	if !strings.Contains(s3.RequestJSON, "projB exit was 0") {
		t.Fatalf("step3 (projC) request missing substituted exit_code:\n%s", s3.RequestJSON)
	}
}

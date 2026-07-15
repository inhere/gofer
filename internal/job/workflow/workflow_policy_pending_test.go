package workflow

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// stubEngineWorkerRunner is a runner.Runner standing in for a live worker: every dispatch
// succeeds (exit 0). It lets a fan-out step "run on a worker" without a hub.
type stubEngineWorkerRunner struct{ name string }

func (r *stubEngineWorkerRunner) Name() string { return r.name }
func (r *stubEngineWorkerRunner) Run(_ context.Context, _ runner.Request) runner.Result {
	return runner.Result{ExitCode: 0}
}

// togglePendingSelector is a job.WorkerSelector whose single candidate is ALWAYS in-caps
// for project "self" + agent "exec", but whose PolicyPending flag can be flipped at
// runtime — so a test can make the worker enter policy_pending AFTER the workflow is
// submitted (modelling a mid-flight reconnect that cleared the rev and re-pends).
type togglePendingSelector struct{ pending atomic.Bool }

func (s *togglePendingSelector) one() job.WorkerCandidate {
	return job.WorkerCandidate{
		WorkerID: "w1", HeartbeatAge: time.Second,
		Projects: []string{"self"}, Agents: []string{"exec"},
		PolicyPending: s.pending.Load(), PolicyRev: 6,
	}
}
func (s *togglePendingSelector) Candidates() []job.WorkerCandidate {
	return []job.WorkerCandidate{s.one()}
}
func (s *togglePendingSelector) Candidate(id string) (job.WorkerCandidate, bool) {
	if id == "w1" {
		return s.one(), true
	}
	return job.WorkerCandidate{}, false
}

// newWorkerEngine builds an Engine over a job.Service that has BOTH a local runner (for a
// gating first step) and a pinned worker runner "remote-w1" → w1 (a stub), plus the
// toggle selector. It mirrors newTestEngine's wiring, adding the worker-runner pieces the
// policy_pending workflow regression needs.
func newWorkerEngine(t *testing.T, root string, sel job.WorkerSelector) *Engine {
	t.Helper()
	cfg := &config.Config{
		Server:  config.ServerConfig{Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}}},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local", "remote-w1"},
				AllowExec:      true,
			},
		},
		Runners: map[string]config.RunnerConfig{
			"remote-w1": {Type: "worker", WorkerID: "w1"},
		},
	}
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{
		localrunner.Name: localrunner.New(),
		"remote-w1":      &stubEngineWorkerRunner{name: "remote-w1"},
	}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	svc := job.NewService(cfg, projReg, agentReg, runners, meta, sel)
	eng := NewEngine(svc)
	svc.SetWorkflow(eng)
	return eng
}

// TestWorkflowFanOutSurvivesPendingWorker is validation 15 at the WORKFLOW level and the
// falsification target for H1 (advance.go:365). A 2-step workflow runs a local first step,
// then a fan-out step onto a worker that entered policy_pending MID-FLIGHT (after submit).
// Because the project is still in the worker's caps, every fan's Submit must be ACCEPTED
// and the workflow must COMPLETE — pending only swaps an error message, never rejects.
//
// Falsification (proven manually): make policy_pending a hard reject in job.validate →
// each fan's e.ops.Submit fails at advance time (submitStepFan `return err`, advance.go:365),
// the fan-out step fails, and the whole workflow ends WorkflowFailed. That is exactly the
// availability regression the plan forbids: a healthy in-flight workflow killed only
// because a worker is briefly re-applying a policy that does not even change its caps.
func TestWorkflowFanOutSurvivesPendingWorker(t *testing.T) {
	root := t.TempDir()
	sel := &togglePendingSelector{}
	e := newWorkerEngine(t, root, sel)

	// Step 1 sleeps briefly so the worker can be flipped into policy_pending AFTER submit-time
	// validation (which sees a healthy worker) but BEFORE the fan-out step is submitted at
	// advance time — pinning the failure, under a hard-reject, to advance.go:365 rather than
	// to the submit-time pre-validate.
	wf, err := e.SubmitWorkflow(Spec{
		Steps: []StepSpec{
			{Name: "gate", ProjectKey: "self", Agent: "exec", Runner: "local",
				Cmd: []string{"sh", "-c", "sleep 0.3"}, Cwd: ".", TimeoutSec: 30},
			{Name: "fan", ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
				Cmd: []string{"echo", "x"}, Cwd: ".", TimeoutSec: 30, FanOut: 2, Join: "all"},
		},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow (worker healthy at submit): %v", err)
	}

	// Worker reconnects and re-enters pending while the workflow is still running.
	sel.pending.Store(true)

	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("fan-out onto a policy_pending (but in-caps) worker must COMPLETE, got %s (err=%s) — pending must not reject the workflow", final.Status, final.Error)
	}
}

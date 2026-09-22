package workflow

import (
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

// fixedClock returns a nowFn pinned to a settable unix time, for deterministic
// backoff / due assertions in the workflow retry tests (copied from the job
// package's delivery_test.go fixedClock, which does not cross the package boundary).
type fixedClock struct{ t atomic.Int64 }

func (f *fixedClock) set(unix int64) { f.t.Store(unix) }
func (f *fixedClock) now() int64     { return f.t.Load() }

// submitAndWait submits a single (non-workflow) job through the engine's host service
// and blocks until it reaches a terminal state, returning the final snapshot. It mirrors
// the job package's service_test.go submitAndWait (which does not cross the package
// boundary); the workflow job-level-retry test drives the host service through e.ops.
func submitAndWait(t *testing.T, e *Engine, req job.JobRequest) job.JobResult {
	t.Helper()
	res, err := e.ops.Submit(req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final, ok := e.ops.Wait(res.ID)
	if !ok {
		t.Fatalf("Wait: job %s not found", res.ID)
	}
	return final
}

// newTestEngine builds a workflow Engine over a job.Service whose result base dir lives
// under root. It mirrors the job package's newTestServiceWithDB setup (two projects:
// "self" with allow_exec=true and "noexec" with allow_exec=false; the metadata db under
// root) and wires the engine back into the service (SetWorkflow) so the integration
// path finish→Advance drives a chain exactly as in production. It returns just the
// Engine; tests reach the host service's read methods (Get/Wait/TailLog/Config) through
// the engine's JobOps (e.ops) and the workflow state through e.meta.
func newTestEngine(t *testing.T, root string) *Engine {
	t.Helper()
	eng, _ := newTestEngineWithService(t, root)
	return eng
}

// newTestEngineWithService is newTestEngine plus the host *job.Service, for the tests
// that must drive a HOST-side sweeper directly — R2/AUTO-03 submits a job-level retry
// from serve's retry loop, which these tests stand in for (the step-level retry needs
// no sweeper: the engine advances it).
func newTestEngineWithService(t *testing.T, root string) (*Engine, *job.Service) {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root, // any existing dir; cwd "." resolves here
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
			"noexec": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      false,
			},
		},
	}
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	svc := job.NewService(cfg, projReg, agentReg, runners, meta, nil)
	eng := NewEngine(svc)
	svc.SetWorkflow(eng)
	// Drain in-flight jobs before the store closes and (later) the test's
	// TempDir is removed. Tests submit workflows and assert on submission-time
	// state without waiting for the chain to finish, so a step job can still be
	// executing at test end — holding its stdout.log/stderr.log open, which
	// blocks t.TempDir's RemoveAll on Windows. Registered after the meta.Close
	// cleanup so it runs first (cleanups are LIFO).
	t.Cleanup(func() {
		// Stop the CHAINS first. A step reaching terminal fires Advance, which starts
		// the NEXT step: a job that can appear right after the drain below has seen
		// "nothing in flight", and then writes into the test's TempDir (observed on
		// macos-latest as `TempDir RemoveAll cleanup: ... directory not empty` in
		// TestSubmitWorkflowStartsFirstStep). CancelWorkflow marks the header
		// cancelled, and Advance's running-status guard then starts nothing further,
		// so from here the job set can only shrink.
		if wfs, err := meta.ListWorkflows(jobstore.WorkflowRunning, drainWorkflowScanLimit); err == nil {
			for _, wf := range wfs {
				_ = eng.CancelWorkflow(wf.ID)
			}
		}
		// Then drain the jobs, requiring a QUIET WINDOW rather than a single-shot
		// "nothing in flight" read: a job submitted concurrently with the previous
		// observation (a racing Advance, a fallback/auto-resume continuation) must be
		// seen and cancelled, not slip past the check that then returns.
		deadline := time.Now().Add(drainBudget)
		seen := map[string]bool{}
		quiet := 0
		for time.Now().Before(deadline) {
			jobs, _ := meta.ListJobs(jobstore.ListQuery{})
			moved, inFlight := false, 0
			for _, j := range jobs {
				if !seen[j.ID] {
					seen[j.ID] = true
					moved = true
				}
				if !job.IsFinished(j.Status) {
					inFlight++
					_ = svc.Cancel(j.ID) // best-effort: speed up the drain
				}
			}
			if inFlight == 0 && !moved {
				if quiet++; quiet >= drainQuietRounds {
					return
				}
			} else {
				quiet = 0
			}
			time.Sleep(drainRoundSleep)
		}
	})
	return eng, svc
}

// Teardown-drain tuning: the budget bounds the whole wait (a job that ignores
// cancellation must not stall the suite), the quiet window is how many consecutive
// observations of "nothing in flight and no new job id" end the drain early.
const (
	drainBudget            = 15 * time.Second
	drainQuietRounds       = 3
	drainRoundSleep        = 10 * time.Millisecond
	drainWorkflowScanLimit = 500
)

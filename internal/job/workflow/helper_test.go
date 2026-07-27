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
		deadline := time.Now().Add(15 * time.Second)
		for {
			jobs, _ := meta.ListJobs(jobstore.ListQuery{})
			inFlight := false
			for _, j := range jobs {
				if !job.IsTerminal(j.Status) {
					inFlight = true
					_ = svc.Cancel(j.ID) // best-effort: speed up the drain
				}
			}
			if !inFlight || time.Now().After(deadline) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	return eng
}

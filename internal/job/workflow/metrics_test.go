package workflow

import (
	"path/filepath"
	"sync"
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

// fakeSink records the job.MetricsSink calls so a workflow test can assert the
// WorkflowTerminal埋点 fired with the expected status + a non-negative duration
// (P4/T4.3). It implements the full job.MetricsSink interface (the job埋点 methods are
// unused no-ops here). It is concurrency-safe (advance runs on a background goroutine).
type fakeSink struct {
	mu         sync.Mutex
	wfTerminal []wfTerminalCall
}

// wfTerminalCall records a WorkflowTerminal埋点 (P4/T4.3): its status + duration.
type wfTerminalCall struct {
	status string
	dur    float64
}

func (f *fakeSink) JobSubmitted(caller, project, agent, runner string)                     {}
func (f *fakeSink) JobTerminal(status, caller, project, agent, runner string, dur float64) {}
func (f *fakeSink) WorkflowTerminal(status string, dur float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wfTerminal = append(f.wfTerminal, wfTerminalCall{status, dur})
}

// wfSnapshot returns a copy of the recorded WorkflowTerminal calls (P4/T4.3).
func (f *fakeSink) wfSnapshot() []wfTerminalCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]wfTerminalCall(nil), f.wfTerminal...)
}

// newMeteredEngine builds an Engine like newTestEngine but injects the metrics sink into
// the service BEFORE the engine is constructed, so the engine caches it (NewEngine reads
// ops.Metrics() once). Same project setup as newTestEngine ("self" allow_exec=true).
func newMeteredEngine(t *testing.T, root string, sink job.MetricsSink) *Engine {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
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
	svc.SetMetrics(sink)
	eng := NewEngine(svc)
	svc.SetWorkflow(eng)
	return eng
}

// TestWorkflowMetricsTerminal asserts a workflow reaching done fires exactly one
// WorkflowTerminal埋点 with status=done and a non-negative duration (P4/T4.3).
func TestWorkflowMetricsTerminal(t *testing.T) {
	sink := &fakeSink{}
	e := newMeteredEngine(t, t.TempDir(), sink)

	wf, err := e.SubmitWorkflow(Spec{
		Title: "metered",
		Steps: []StepSpec{echoStep("a"), echoStep("b")},
	}, "ci-bot")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("workflow status = %s, want done", final.Status)
	}

	// Poll briefly: setWorkflowDone records the metric on the advance goroutine.
	var calls []wfTerminalCall
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		calls = sink.wfSnapshot()
		if len(calls) >= 1 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if len(calls) != 1 {
		t.Fatalf("WorkflowTerminal calls = %d, want exactly 1: %+v", len(calls), calls)
	}
	if calls[0].status != jobstore.WorkflowDone {
		t.Fatalf("WorkflowTerminal status = %q, want done", calls[0].status)
	}
	if calls[0].dur < 0 {
		t.Fatalf("WorkflowTerminal duration must be >= 0, got %v", calls[0].dur)
	}
}

// TestWorkflowMetricsCancelled asserts cancelling a running workflow fires a
// WorkflowTerminal with status=cancelled (P4/T4.3).
func TestWorkflowMetricsCancelled(t *testing.T) {
	sink := &fakeSink{}
	e := newMeteredEngine(t, t.TempDir(), sink)

	// A slow first step keeps the workflow running until we cancel it.
	wf, err := e.SubmitWorkflow(Spec{
		Title: "to-cancel",
		Steps: []StepSpec{
			{
				Name: "slow", ProjectKey: "self", Agent: "exec", Runner: "local",
				Cmd: []string{"sh", "-c", "sleep 30"}, Cwd: ".", TimeoutSec: 60,
			},
		},
	}, "ci-bot")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	if err := e.CancelWorkflow(wf.ID); err != nil {
		t.Fatalf("CancelWorkflow: %v", err)
	}

	var calls []wfTerminalCall
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		calls = sink.wfSnapshot()
		if len(calls) >= 1 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if len(calls) < 1 {
		t.Fatalf("WorkflowTerminal calls = %d, want >= 1", len(calls))
	}
	if calls[0].status != jobstore.WorkflowCancelled {
		t.Fatalf("WorkflowTerminal status = %q, want cancelled", calls[0].status)
	}
}

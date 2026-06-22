package job

import (
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// fakeSink records the MetricsSink calls so a test can assert埋点 fired with the
// expected labels. It is concurrency-safe (finish runs on the execute goroutine).
type fakeSink struct {
	mu         sync.Mutex
	submitted  []submitCall
	terminal   []terminalCall
	wfTerminal []wfTerminalCall
}

type submitCall struct{ caller, project, agent, runner string }
type terminalCall struct {
	status, caller, project, agent, runner string
	dur                                    float64
}

// wfTerminalCall records a WorkflowTerminal埋点 (P4/T4.3) so the workflow metrics
// test can assert the status + a non-negative duration.
type wfTerminalCall struct {
	status string
	dur    float64
}

func (f *fakeSink) JobSubmitted(caller, project, agent, runner string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submitted = append(f.submitted, submitCall{caller, project, agent, runner})
}

func (f *fakeSink) JobTerminal(status, caller, project, agent, runner string, dur float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminal = append(f.terminal, terminalCall{status, caller, project, agent, runner, dur})
}

func (f *fakeSink) WorkflowTerminal(status string, dur float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wfTerminal = append(f.wfTerminal, wfTerminalCall{status, dur})
}

func (f *fakeSink) snapshot() ([]submitCall, []terminalCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]submitCall(nil), f.submitted...), append([]terminalCall(nil), f.terminal...)
}

// wfSnapshot returns a copy of the recorded WorkflowTerminal calls (P4/T4.3).
func (f *fakeSink) wfSnapshot() []wfTerminalCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]wfTerminalCall(nil), f.wfTerminal...)
}

// TestMetricsSinkSubmitAndTerminal asserts Submit fires JobSubmitted and the
// terminal transition fires JobTerminal with the routing labels + a status.
func TestMetricsSinkSubmitAndTerminal(t *testing.T) {
	s := newTestService(t, t.TempDir())
	sink := &fakeSink{}
	s.SetMetrics(sink)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		CallerID: "ci-bot",
	})
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s", final.Status)
	}

	subs, terms := sink.snapshot()
	if len(subs) != 1 {
		t.Fatalf("JobSubmitted calls=%d, want 1: %+v", len(subs), subs)
	}
	if subs[0] != (submitCall{"ci-bot", "self", "exec", "local"}) {
		t.Fatalf("unexpected submit labels: %+v", subs[0])
	}
	if len(terms) != 1 {
		t.Fatalf("JobTerminal calls=%d, want 1: %+v", len(terms), terms)
	}
	got := terms[0]
	if got.status != StatusDone || got.caller != "ci-bot" || got.project != "self" ||
		got.agent != "exec" || got.runner != "local" {
		t.Fatalf("unexpected terminal labels: %+v", got)
	}
	if got.dur < 0 {
		t.Fatalf("terminal duration must be >= 0, got %v", got.dur)
	}
}

// TestWorkflowMetricsTerminal asserts a workflow reaching done fires exactly one
// WorkflowTerminal埋点 with status=done and a non-negative duration (P4/T4.3).
func TestWorkflowMetricsTerminal(t *testing.T) {
	s := newTestService(t, t.TempDir())
	sink := &fakeSink{}
	s.SetMetrics(sink)

	wf, err := s.SubmitWorkflow(WorkflowSpec{
		Title: "metered",
		Steps: []StepSpec{echoStep("a"), echoStep("b")},
	}, "ci-bot")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, s, wf.ID)
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
	s := newTestService(t, t.TempDir())
	sink := &fakeSink{}
	s.SetMetrics(sink)

	// A slow first step keeps the workflow running until we cancel it.
	wf, err := s.SubmitWorkflow(WorkflowSpec{
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
	if err := s.CancelWorkflow(wf.ID); err != nil {
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

// TestStatsTracksInFlight asserts Stats counts only live (non-terminal) entries
// and is 0 once a job is terminal (entries are evicted in finish).
func TestStatsTracksInFlight(t *testing.T) {
	s := newTestService(t, t.TempDir())
	if st := s.Stats(); st.InFlight != 0 {
		t.Fatalf("fresh service InFlight=%d, want 0", st.InFlight)
	}

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s", final.Status)
	}
	// After terminal + eviction the live set is empty again.
	if st := s.Stats(); st.InFlight != 0 || st.Queued != 0 || st.Running != 0 {
		t.Fatalf("after terminal Stats=%+v, want all 0", st)
	}
}

// TestNilSinkSafe asserts a service with no sink wired runs jobs without panic
// (the埋点 sites self-guard on s.metrics != nil).
func TestNilSinkSafe(t *testing.T) {
	s := newTestService(t, t.TempDir())
	// no SetMetrics call.
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s", final.Status)
	}
}

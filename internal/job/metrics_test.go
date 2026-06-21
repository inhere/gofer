package job

import (
	"sync"
	"testing"
)

// fakeSink records the MetricsSink calls so a test can assert埋点 fired with the
// expected labels. It is concurrency-safe (finish runs on the execute goroutine).
type fakeSink struct {
	mu        sync.Mutex
	submitted []submitCall
	terminal  []terminalCall
}

type submitCall struct{ caller, project, agent, runner string }
type terminalCall struct {
	status, caller, project, agent, runner string
	dur                                    float64
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

func (f *fakeSink) snapshot() ([]submitCall, []terminalCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]submitCall(nil), f.submitted...), append([]terminalCall(nil), f.terminal...)
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

package job

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// newAgentHealthService builds a service over ONE agent whose exit code is the job's own
// prompt: `stderr-exit <prompt> "at capacity"`. A prompt of "1" is therefore a transient
// provider failure (the stderr bait matches the configured pattern) and a prompt of "0" is
// a normal delivery — the two outcomes a health transition is made of, driven through the
// real submit → finish path.
func newAgentHealthService(t *testing.T, root string) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"flaky"},
				AllowedRunners: []string{"local"},
			},
		},
		Agents: map[string]config.AgentConfig{
			"flaky": {
				Type:                   agent.TypeCLIAgent,
				Command:                testcmd.Path(t),
				Args:                   []string{"stderr-exit", "{{prompt}}", "at capacity"},
				TransientErrorPatterns: []string{"at capacity"},
			},
		},
	}
	return newServiceFromCfg(t, root, cfg)
}

// agentHealthEventTypes returns the event types recorded on an agent's own scope, in
// order.
func agentHealthEventTypes(t *testing.T, s *Service, agentKey string) []string {
	t.Helper()
	evs, err := s.ListJobEvents(AgentEventScope(agentKey), 0)
	if err != nil {
		t.Fatalf("ListJobEvents(%s): %v", agentKey, err)
	}
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, ev.Type)
	}
	return out
}

// runAgentJob submits one job on the flaky agent whose exit code is prompt ("1" fails
// transiently, "0" delivers) and waits for its terminal snapshot.
func runAgentJob(t *testing.T, s *Service, prompt string) JobResult {
	t.Helper()
	return submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "flaky", Runner: "local",
		Prompt: prompt, Cwd: ".", TimeoutSec: 30,
	})
}

// TestAgentDegradedEventOnTransition (design §六): the health verdict is recomputed when
// a job ends, and only a CHANGE is announced — three transient failures produce exactly
// one agent.degraded (on the one that crossed the threshold), further failures stay
// silent, the first delivery announces agent.recovered, and a second one stays silent.
func TestAgentDegradedEventOnTransition(t *testing.T) {
	s := newAgentHealthService(t, t.TempDir())

	// Two failures are below the default degraded_after (3): the agent is still healthy,
	// and nothing is announced.
	for i := 0; i < 2; i++ {
		if res := runAgentJob(t, s, "1"); res.Status != StatusFailed {
			t.Fatalf("failure %d status = %q, want failed", i+1, res.Status)
		}
	}
	if got := agentHealthEventTypes(t, s, "flaky"); len(got) != 0 {
		t.Fatalf("events after two transient failures = %v, want none", got)
	}

	// The third crosses the threshold: ONE degraded event, carrying the evidence.
	res := runAgentJob(t, s, "1")
	if res.Status != StatusFailed || res.FailureClass != FailureClassTransient {
		t.Fatalf("third failure = status %q class %q, want a transient failure", res.Status, res.FailureClass)
	}
	events := agentHealthEventTypes(t, s, "flaky")
	if len(events) != 1 || events[0] != EventAgentDegraded {
		t.Fatalf("events after the third failure = %v, want exactly one %s", events, EventAgentDegraded)
	}
	raw, err := s.ListJobEvents(AgentEventScope("flaky"), 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(raw[0].Detail), &detail); err != nil {
		t.Fatalf("agent.degraded detail %q: %v", raw[0].Detail, err)
	}
	if detail["agent"] != "flaky" {
		t.Fatalf("agent.degraded agent = %v", detail["agent"])
	}
	if detail["transient_fail"] != float64(3) {
		t.Fatalf("agent.degraded transient_fail = %v, want 3", detail["transient_fail"])
	}
	if detail["window_sec"] != float64(config.DefaultAgentHealthWindowSec) {
		t.Fatalf("agent.degraded window_sec = %v, want %d", detail["window_sec"], config.DefaultAgentHealthWindowSec)
	}
	if detail["last_error"] == "" {
		t.Fatalf("agent.degraded must quote the failing run: %v", detail)
	}

	// A fourth failure is the same verdict: no second announcement.
	if res := runAgentJob(t, s, "1"); res.Status != StatusFailed {
		t.Fatalf("fourth failure status = %q, want failed", res.Status)
	}
	if got := agentHealthEventTypes(t, s, "flaky"); len(got) != 1 {
		t.Fatalf("events after a repeat failure = %v, want still one", got)
	}

	// A delivery clears the window's recovery count: degraded → healthy is announced.
	// The wait is the aggregate's own granularity: ok_since_transient counts successes
	// STRICTLY after the last transient failure's ended_at, and those are unix SECONDS —
	// so a recovery in the same second as the failure it recovers from does not count
	// yet (self-correcting on the next success, and it is how SUP-01 P3 has always read).
	time.Sleep(1100 * time.Millisecond)
	if res := runAgentJob(t, s, "0"); res.Status != StatusDone {
		t.Fatalf("recovering job status = %q (%s), want done", res.Status, res.Error)
	}
	events = agentHealthEventTypes(t, s, "flaky")
	if len(events) != 2 || events[1] != EventAgentRecovered {
		t.Fatalf("events after a delivery = %v, want a trailing %s", events, EventAgentRecovered)
	}
	raw, err = s.ListJobEvents(AgentEventScope("flaky"), 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(raw[1].Detail), &rec); err != nil {
		t.Fatalf("agent.recovered detail %q: %v", raw[1].Detail, err)
	}
	if rec["agent"] != "flaky" {
		t.Fatalf("agent.recovered agent = %v", rec["agent"])
	}

	// Staying healthy is not a transition.
	if res := runAgentJob(t, s, "0"); res.Status != StatusDone {
		t.Fatalf("second delivery status = %q, want done", res.Status)
	}
	if got := agentHealthEventTypes(t, s, "flaky"); len(got) != 2 {
		t.Fatalf("events after a second delivery = %v, want no repeat", got)
	}

	// The event stream is durable and the health of the agent is readable from the same
	// aggregate the badge uses (the transition is an observation OF that aggregate).
	h, err := s.Meta().AgentHealth("flaky", time.Now().Add(-time.Hour).Unix())
	if err != nil {
		t.Fatalf("AgentHealth: %v", err)
	}
	if h.TransientFail != 4 || h.OKSinceTransient != 2 {
		t.Fatalf("aggregate = %+v, want 4 transient failures and 2 successes since", h)
	}
	if got := agent.HealthState(h, config.AgentHealthConfig{}); got != agent.HealthHealthy {
		t.Fatalf("final health = %q, want healthy", got)
	}
}

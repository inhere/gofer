package job

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/config"
)

func sessionEndReason(result JobResult) string {
	b, _ := json.Marshal(result)
	var fields map[string]any
	_ = json.Unmarshal(b, &fields)
	reason, _ := fields["session_end_reason"].(string)
	return reason
}

func waitSessionStatus(t *testing.T, s *Service, id, status string, timeout time.Duration) JobResult {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		current, ok := s.Get(id)
		if ok && current.Status == status {
			return current
		}
		if ok && IsTerminal(current.Status) {
			t.Fatalf("job %s reached %s before %s: %s", id, current.Status, status, current.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	current, _ := s.Get(id)
	t.Fatalf("job %s status=%s, want %s", id, current.Status, status)
	return JobResult{}
}

func TestSessionJobTimeoutAppliesPerTurn(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{})
	first, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", Prompt: "first", Session: true, TimeoutSec: 1, IdleTimeoutSec: 5})
	if err != nil {
		t.Fatal(err)
	}
	waitSessionStatus(t, s, first.ID, StatusAwaitingInput, 5*time.Second)
	time.Sleep(1200 * time.Millisecond)
	current, _ := s.Get(first.ID)
	if current.Status != StatusAwaitingInput {
		t.Fatalf("waiting input consumed per-turn timeout: %s error=%s", current.Status, current.Error)
	}
	if err := s.SaySession(first.ID, "second"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, _ = s.Get(first.ID)
		if current.Status == StatusAwaitingInput && current.TurnNo == 2 {
			if err := s.EndSession(first.ID); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("second turn did not complete: %+v", current)
}

func TestSessionJobIdleTimeoutEndsAwaitingInput(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{})
	first, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", Prompt: "first", Session: true, TimeoutSec: 20, IdleTimeoutSec: 1})
	if err != nil {
		t.Fatal(err)
	}
	waitSessionStatus(t, s, first.ID, StatusAwaitingInput, 5*time.Second)
	final, ok := s.WaitFor(first.ID, 4*time.Second)
	if !ok || final.Status != StatusDone || sessionEndReason(final) != "idle_timeout" {
		t.Fatalf("idle timeout = ok:%v status:%s reason:%s", ok, final.Status, sessionEndReason(final))
	}
}

func TestSessionJobHoldsLockAndAgentSlotWhileAwaitingInput(t *testing.T) {
	s := newACPServiceAgent(t, t.TempDir(), acptest.Options{}, nil, func(ac *config.AgentConfig) { ac.MaxConcurrent = 1 })
	first, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", Prompt: "first", Session: true, TimeoutSec: 20, IdleTimeoutSec: 10})
	if err != nil {
		t.Fatal(err)
	}
	waitSessionStatus(t, s, first.ID, StatusAwaitingInput, 5*time.Second)
	second, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", Prompt: "second", TimeoutSec: 20})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	current, _ := s.Get(second.ID)
	if current.Status != StatusQueued && current.Status != StatusWaitingDir {
		t.Fatalf("second job bypassed held slot/lock: %s", current.Status)
	}
	if err := s.EndSession(first.ID); err != nil {
		t.Fatal(err)
	}
	if final, ok := s.Wait(second.ID); !ok || final.Status != StatusDone {
		t.Fatalf("queued job after release: ok=%v result=%+v", ok, final)
	}
}

func TestSessionJobMaxSessionTimeout(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{})
	first, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", Prompt: "first", Session: true, TimeoutSec: 20, IdleTimeoutSec: 10, MaxSessionSec: 1})
	if err != nil {
		t.Fatal(err)
	}
	waitSessionStatus(t, s, first.ID, StatusAwaitingInput, 5*time.Second)
	final, ok := s.WaitFor(first.ID, 4*time.Second)
	if !ok || final.Status != StatusDone || sessionEndReason(final) != "max_session_timeout" {
		t.Fatalf("max session timeout = ok:%v status:%s reason:%s", ok, final.Status, sessionEndReason(final))
	}
}

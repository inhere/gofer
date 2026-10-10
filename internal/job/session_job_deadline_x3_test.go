package job

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/wait"
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
	deadline := time.Now().Add(wait.Timeout(t, timeout))
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
	// The per-turn budget must cover a whole turn on a loaded machine (1s did not);
	// the wait below then outlasts it while the session merely awaits input.
	first, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", Prompt: "first", Session: true, TimeoutSec: 3, IdleTimeoutSec: 10})
	if err != nil {
		t.Fatal(err)
	}
	waitSessionStatus(t, s, first.ID, StatusAwaitingInput, 5*time.Second)
	time.Sleep(3200 * time.Millisecond)
	current, _ := s.Get(first.ID)
	if current.Status != StatusAwaitingInput {
		t.Fatalf("waiting input consumed per-turn timeout: %s error=%s", current.Status, current.Error)
	}
	if err := s.SaySession(first.ID, "second"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(wait.Timeout(t, 5*time.Second))
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
	final, ok := s.WaitFor(first.ID, wait.Timeout(t, 4*time.Second))
	if !ok || final.Status != StatusDone || sessionEndReason(final) != "idle_timeout" {
		t.Fatalf("idle timeout = ok:%v status:%s reason:%s", ok, final.Status, sessionEndReason(final))
	}
}

func TestSessionJobHoldsLockAndAgentSlotWhileAwaitingInput(t *testing.T) {
	t.Run("agent slot", func(t *testing.T) {
		s := newACPServiceAgent(t, t.TempDir(), acptest.Options{}, nil, func(ac *config.AgentConfig) { ac.MaxConcurrent = 1 })
		assertSessionBlocksNextJob(t, s, StatusQueued)
	})
	t.Run("directory lock", func(t *testing.T) {
		s := newACPService(t, t.TempDir(), acptest.Options{})
		assertSessionBlocksNextJob(t, s, StatusWaitingDir)
	})
}

func assertSessionBlocksNextJob(t *testing.T, s *Service, blockedStatus string) {
	t.Helper()
	first, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", Prompt: "first", Session: true, TimeoutSec: 20, IdleTimeoutSec: 10})
	if err != nil {
		t.Fatal(err)
	}
	waitSessionStatus(t, s, first.ID, StatusAwaitingInput, 5*time.Second)
	second, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", Prompt: "second", TimeoutSec: 20})
	if err != nil {
		t.Fatal(err)
	}
	wait.For(t, 5*time.Second, "second job blocked while the session waits", func() (bool, any) {
		current, _ := s.Get(second.ID)
		return current.Status == blockedStatus, current.Status
	})
	if err := s.EndSession(first.ID); err != nil {
		t.Fatal(err)
	}
	if final, ok := s.Wait(second.ID); !ok || final.Status != StatusDone {
		t.Fatalf("queued job after release: ok=%v result=%+v", ok, final)
	}
}

func TestSessionJobTurnTimeoutFails(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{Slow: true})
	first, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", Prompt: "slow", Session: true, TimeoutSec: 1, IdleTimeoutSec: 10})
	if err != nil {
		t.Fatal(err)
	}
	final, ok := s.WaitFor(first.ID, wait.Timeout(t, 5*time.Second))
	if !ok || final.Status != StatusFailed || final.TurnNo != 1 {
		t.Fatalf("turn timeout: ok=%v result=%+v", ok, final)
	}
}

func TestSessionJobMaxSessionTimeout(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{})
	// No initial prompt: the session opens straight into awaiting input, so the 1s
	// budget is not spent on a first turn (a loaded machine ran out of it there and
	// the job ended before it ever awaited input).
	first, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", Session: true, TimeoutSec: 20, IdleTimeoutSec: 10, MaxSessionSec: 1})
	if err != nil {
		t.Fatal(err)
	}
	final, ok := s.WaitFor(first.ID, wait.Timeout(t, 10*time.Second))
	if !ok || final.Status != StatusDone || sessionEndReason(final) != "max_session_timeout" || final.TurnNo != 0 {
		t.Fatalf("max session timeout = ok:%v status:%s reason:%s", ok, final.Status, sessionEndReason(final))
	}
}

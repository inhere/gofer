package job

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/store"
)

func submitSmokeSession(t *testing.T, s *Service, idle int) JobResult {
	t.Helper()
	created, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
		Prompt: "remember smoke-code-742", Session: true, TimeoutSec: 20, IdleTimeoutSec: idle,
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestInteractiveACPSessionThreeTurns(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{})
	created := submitSmokeSession(t, s, 20)
	waitSessionTurn(t, s, created.ID, 1)
	for turn, message := range []string{"what was the code?", "third turn"} {
		if err := s.SaySession(created.ID, message); err != nil {
			t.Fatal(err)
		}
		waitSessionTurn(t, s, created.ID, turn+2)
	}
	if err := s.EndSession(created.ID); err != nil {
		t.Fatal(err)
	}
	final, ok := s.Wait(created.ID)
	if !ok || final.Status != StatusDone || final.TurnNo != 3 {
		t.Fatalf("three-turn final: ok=%v result=%+v", ok, final)
	}
	if log := readJobLog(t, final, store.StderrFile); strings.Count(log, "acptest: initialize") != 1 || strings.Count(log, "acptest: session/prompt sid=") != 3 {
		t.Fatalf("fake ACP process was restarted between turns: %s", log)
	}
	if out := readJobLog(t, final, store.StdoutFile); strings.Count(out, "--- turn ") != 3 {
		t.Fatalf("stdout lacks three turn boundaries: %q", out)
	}
	events, err := s.meta.ListJobEvents(final.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, event := range events {
		counts[event.Type]++
	}
	if counts[EventJobTurnStarted] != 3 || counts[EventJobTurnEnded] != 3 || counts[EventJobAwaitingInput] != 3 {
		t.Fatalf("turn lifecycle events=%v", counts)
	}
}

func TestInteractiveACPSessionIdleTimeout(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{})
	created := submitSmokeSession(t, s, 1)
	final, ok := s.WaitFor(created.ID, 5*time.Second)
	if !ok || final.Status != StatusDone || final.SessionEndReason != "idle_timeout" {
		t.Fatalf("idle final: ok=%v result=%+v", ok, final)
	}
}

func TestInteractiveACPSessionCancel(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{})
	created := submitSmokeSession(t, s, 20)
	waitSessionTurn(t, s, created.ID, 1)
	if err := s.Cancel(created.ID); err != nil {
		t.Fatal(err)
	}
	final, ok := s.Wait(created.ID)
	if !ok || final.Status != StatusCancelled {
		t.Fatalf("cancel final: ok=%v result=%+v", ok, final)
	}
	if err := s.SaySession(created.ID, "late"); !errors.Is(err, ErrJobNotRunning) {
		t.Fatalf("say after cancel=%v, want ErrJobNotRunning", err)
	}
}

func TestInteractiveACPSessionRecovery(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{})
	seedRecoverableSession(t, s, root, "smoke-recovered", StatusAwaitingInput)
	if _, err := s.ReconcileOrphanJobs(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := s.Get("smoke-recovered")
		if s.entry(current.ID) != nil && current.Status == StatusAwaitingInput {
			if err := s.SaySession(current.ID, "after load"); err != nil {
				t.Fatal(err)
			}
			waitSessionTurn(t, s, current.ID, 2)
			if err := s.EndSession(current.ID); err != nil {
				t.Fatal(err)
			}
			final, _ := s.Wait(current.ID)
			if final.Status != StatusDone || !strings.Contains(readJobLog(t, final, store.StderrFile), "session/load sid="+acptest.SessionID) {
				t.Fatalf("recovered session did not load and finish: %+v", final)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("fake ACP session was not recovered")
}

package job

import (
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/store"
)

type sessionJobOps interface {
	SaySession(string, string) error
	EndSession(string) error
}

func startTwoTurnSession(t *testing.T) (*Service, JobResult) {
	t.Helper()
	s := newACPService(t, t.TempDir(), acptest.Options{})
	job, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
		Prompt: "first message", Session: true, TimeoutSec: 30, IdleTimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("submit session: %v", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		current, ok := s.Get(job.ID)
		if !ok {
			t.Fatalf("session job %s disappeared", job.ID)
		}
		if current.Status == StatusAwaitingInput {
			return s, current
		}
		if IsTerminal(current.Status) {
			t.Fatalf("session ended after first turn: status=%s error=%s", current.Status, current.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session did not await input: %+v", job)
	return nil, JobResult{}
}

func TestACPSessionRunnerKeepsProcessAcrossTurns(t *testing.T) {
	s, first := startTwoTurnSession(t)
	ops, ok := any(s).(sessionJobOps)
	if !ok {
		t.Fatal("job service has no say/end session operations")
	}
	if err := ops.SaySession(first.ID, "second message"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := s.Get(first.ID)
		if current.Status == StatusAwaitingInput && current.TurnNo == 2 {
			if err := ops.EndSession(first.ID); err != nil {
				t.Fatal(err)
			}
			final, _ := s.Wait(first.ID)
			if final.Status != StatusDone {
				t.Fatalf("end status=%s error=%s", final.Status, final.Error)
			}
			agentLog := readJobLog(t, final, store.StderrFile)
			if strings.Count(agentLog, "acptest: initialize") != 1 || strings.Count(agentLog, "acptest: session/new") != 1 || strings.Count(agentLog, "acptest: session/prompt sid=") != 2 {
				t.Fatalf("agent process/session was not reused:\n%s", agentLog)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("second turn did not return to awaiting_input")
}

func TestACPSessionRunnerSeparatesTurnOutput(t *testing.T) {
	s, first := startTwoTurnSession(t)
	if first.TurnNo != 1 {
		t.Fatalf("first turn number=%d", first.TurnNo)
	}
	stdout := readJobLog(t, first, store.StdoutFile)
	if !strings.Contains(stdout, "turn 1") {
		t.Fatalf("stdout lacks first turn separator: %q", stdout)
	}
	_ = s.Cancel(first.ID)
}

func TestSessionJobSayAndEndDriveSameSession(t *testing.T) {
	s, first := startTwoTurnSession(t)
	ops, ok := any(s).(sessionJobOps)
	if !ok {
		t.Fatal("job service has no say/end session operations")
	}
	if err := ops.EndSession(first.ID); err != nil {
		t.Fatal(err)
	}
	final, _ := s.Wait(first.ID)
	if final.Status != StatusDone || final.ID != first.ID || final.SessionID != first.SessionID {
		t.Fatalf("end did not finish the same session job: first=%+v final=%+v", first, final)
	}
}

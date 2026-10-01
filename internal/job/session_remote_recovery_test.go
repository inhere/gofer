package job

import (
	"encoding/json"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestRemoteSessionTurnEventsReplayedOnReconnect(t *testing.T) {
	s := newWorkerTestService(t, t.TempDir(), &stubWorkerRunner{})
	result := JobResult{
		ID: "remote-session-replay", ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
		WorkerID: "w1", Session: true, Status: StatusRecovering, TurnNo: 1,
		IdleTimeoutSec: 30, IdleDeadlineAt: 123, Cwd: ".", ResultDir: t.TempDir(),
	}
	if err := s.persist(result); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.jobs[result.ID] = &jobEntry{result: result, done: make(chan struct{})}
	s.mu.Unlock()
	// The old server had persisted only the first event before it went away.
	s.recordEvent(result.ID, EventJobTurnStarted, map[string]any{"turn_no": 1})
	inflight := []WorkerInflightJob{{JobID: result.ID, Status: StatusAwaitingInput, TurnNo: 1, IdleDeadlineAt: 456}}
	s.ReconcileSessionState("w1", inflight)
	first, err := s.ListJobEvents(result.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	assertReplayedSessionEvent(t, first, EventJobTurnEnded, 1)
	assertReplayedSessionEvent(t, first, EventJobAwaitingInput, 1)
	updated, ok := s.Get(result.ID)
	if !ok || updated.Status != StatusAwaitingInput || updated.TurnNo != 1 || updated.IdleDeadlineAt != 456 {
		t.Fatalf("host session state = %+v, ok=%v", updated, ok)
	}
	// A second reconnect, including one after the persisted store was reused by a
	// new server instance, must not add the same job+turn+state events again.
	s.ReconcileSessionState("w1", inflight)
	second, err := s.ListJobEvents(result.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != len(first) {
		t.Fatalf("event count after replay retry = %d, want %d", len(second), len(first))
	}
}

func assertReplayedSessionEvent(t *testing.T, events []jobstore.JobEvent, eventType string, turn int) {
	t.Helper()
	for _, ev := range events {
		if ev.Type != eventType {
			continue
		}
		var detail map[string]any
		if err := json.Unmarshal([]byte(ev.Detail), &detail); err != nil {
			t.Fatalf("decode %s detail: %v", eventType, err)
		}
		if int(detail["turn_no"].(float64)) == turn && detail["replayed"] == true {
			return
		}
	}
	t.Fatalf("missing replayed %s turn %d in %+v", eventType, turn, events)
}

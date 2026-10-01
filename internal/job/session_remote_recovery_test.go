package job

import (
	"encoding/json"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestRemoteSessionTurnEventsReplayedOnReconnect(t *testing.T) {
	root := t.TempDir()
	s := newWorkerTestService(t, root, &stubWorkerRunner{})
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
	// Re-open the same SQLite job store through a fresh Service to model a server
	// restart, then reconcile the same register.inflight snapshot again.
	s2 := newWorkerTestService(t, root, &stubWorkerRunner{})
	rec, ok, err := s2.Meta().GetJob(result.ID)
	if err != nil || !ok {
		t.Fatalf("re-open persisted job: ok=%v err=%v", ok, err)
	}
	s2.mu.Lock()
	s2.jobs[result.ID] = &jobEntry{result: fromRecord(rec), done: make(chan struct{})}
	s2.mu.Unlock()
	s2.ReconcileSessionState("w1", inflight)
	second, err := s2.ListJobEvents(result.ID, 0)
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

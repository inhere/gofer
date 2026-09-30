package job

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp/acptest"
)

func TestSessionJobStateMachineTransitions(t *testing.T) {
	var req JobRequest
	if err := json.Unmarshal([]byte(`{"project_key":"self","agent":"acpbot","session":true,"idle_timeout_sec":17,"max_session_sec":90}`), &req); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"session":true`, `"idle_timeout_sec":17`, `"max_session_sec":90`} {
		if !strings.Contains(string(encoded), field) {
			t.Errorf("session request lost %s: %s", field, encoded)
		}
	}
	if IsTerminal("awaiting_input") || IsFinished("awaiting_input") {
		t.Fatal("awaiting_input must keep the session job alive")
	}
	s := newACPService(t, t.TempDir(), acptest.Options{})
	entry := &jobEntry{result: JobResult{
		ID: "session-state-test", ProjectKey: "self", Agent: "acpbot", Runner: "local",
		Status: StatusRunning, Session: true, IdleTimeoutSec: 17,
		ResultDir: t.TempDir(), StartedAt: 100,
	}}
	if err := s.beginSessionTurn(entry); err != nil {
		t.Fatal(err)
	}
	if err := s.beginSessionTurn(entry); err == nil {
		t.Fatal("duplicate turn start was accepted")
	}
	if err := s.endSessionTurn(entry, "end_turn"); err != nil {
		t.Fatal(err)
	}
	if err := s.awaitSessionInput(entry); err != nil {
		t.Fatal(err)
	}
	if entry.result.Status != StatusAwaitingInput || entry.result.TurnNo != 1 || entry.result.IdleDeadlineAt == 0 {
		t.Fatalf("after first turn: %+v", entry.result)
	}
	if err := s.beginSessionTurn(entry); err != nil {
		t.Fatal(err)
	}
	if entry.result.Status != StatusRunning || entry.result.TurnNo != 2 || entry.result.IdleDeadlineAt != 0 {
		t.Fatalf("after next turn start: %+v", entry.result)
	}
}

func TestSessionJobPersistsAwaitingInputAndTurnMetadata(t *testing.T) {
	input := `{"status":"awaiting_input","session":true,"turn_no":2,"idle_timeout_sec":17,"max_session_sec":90,"idle_deadline_at":12345}`
	var result JobResult
	if err := json.Unmarshal([]byte(input), &result); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"session":true`, `"turn_no":2`, `"idle_timeout_sec":17`, `"max_session_sec":90`, `"idle_deadline_at":12345`} {
		if !strings.Contains(string(encoded), field) {
			t.Errorf("session result lost %s: %s", field, encoded)
		}
	}
	s := newACPService(t, t.TempDir(), acptest.Options{})
	result.ID = "session-persist-test"
	result.ProjectKey = "self"
	result.Agent = "acpbot"
	result.Runner = "local"
	result.ResultDir = t.TempDir()
	result.StartedAt = 100
	if err := s.persist(result); err != nil {
		t.Fatalf("persist awaiting_input: %v", err)
	}
	rec, ok, err := s.meta.GetJob(result.ID)
	if err != nil || !ok {
		t.Fatalf("read persisted session: ok=%v err=%v", ok, err)
	}
	got := fromRecord(rec)
	if got.Status != "awaiting_input" || !got.Session || got.TurnNo != 2 || got.IdleTimeoutSec != 17 || got.MaxSessionSec != 90 || got.IdleDeadlineAt != 12345 {
		t.Fatalf("session state after DB roundtrip: %+v", got)
	}
}

func TestSessionJobEmitsTurnLifecycleEvents(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{})
	for _, event := range []string{"job.turn_started", "job.turn_ended", "job.awaiting_input"} {
		if !mirroredEventTypes[event] {
			t.Errorf("worker would not mirror session event %q", event)
		}
		s.recordEvent("session-events-test", event, map[string]any{"turn_no": 1})
	}
	events, err := s.meta.ListJobEvents("session-events-test", 0)
	if err != nil || len(events) != 3 {
		t.Fatalf("persisted session events: count=%d err=%v", len(events), err)
	}
}

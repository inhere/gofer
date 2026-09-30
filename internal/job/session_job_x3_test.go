package job

import (
	"encoding/json"
	"strings"
	"testing"
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
}

func TestSessionJobEmitsTurnLifecycleEvents(t *testing.T) {
	for _, event := range []string{"job.turn_started", "job.turn_ended", "job.awaiting_input"} {
		if !mirroredEventTypes[event] {
			t.Errorf("worker would not mirror session event %q", event)
		}
	}
}

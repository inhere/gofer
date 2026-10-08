package wsproto

import (
	"encoding/json"
	"testing"
)

func TestRemoteSessionProtocolV13RoundTrip(t *testing.T) {
	if CurrentProtocolVersion != 19 {
		t.Fatalf("CurrentProtocolVersion = %d, want 19", CurrentProtocolVersion)
	}
	if !SupportsSessionJob(SessionJobMinProtocolVersion) {
		t.Fatal("v13 worker must support session jobs")
	}
	if SupportsSessionJob(SessionJobMinProtocolVersion - 1) {
		t.Fatal("v12 worker must not support session jobs")
	}
	d := Dispatch{
		JobID: "j1", Session: true, IdleTimeoutSec: 30, MaxSessionSec: 300,
	}
	b, err := EncodeFrame(TypeDispatch, d.JobID, d)
	if err != nil {
		t.Fatal(err)
	}
	env, err := DecodeEnvelope(b)
	if err != nil {
		t.Fatal(err)
	}
	got, err := As[Dispatch](env)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Session || got.IdleTimeoutSec != 30 || got.MaxSessionSec != 300 {
		t.Fatalf("dispatch session fields lost: %+v", got)
	}
	cmd := SessionCommand{JobID: "j1", CmdID: "c1", Action: "say", Prompt: "next"}
	raw, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	var round SessionCommand
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatal(err)
	}
	if round != cmd {
		t.Fatalf("session command round trip = %+v, want %+v", round, cmd)
	}
}

func TestRemoteSessionStatusAndInflightRequireTurnMetadata(t *testing.T) {
	status := Status{JobID: "j1", Status: "awaiting_input", TurnNo: 2, IdleDeadlineAt: 123}
	inflight := InflightJob{JobID: "j1", Status: "awaiting_input", TurnNo: 2}
	for name, value := range map[string]any{"status": status, "inflight": inflight} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("%s marshal: %v", name, err)
		}
		if len(raw) == 0 {
			t.Fatalf("%s encoded empty", name)
		}
	}
}

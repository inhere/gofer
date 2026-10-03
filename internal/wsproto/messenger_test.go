package wsproto

import (
	"encoding/json"
	"testing"
)

func TestMessengerDispatchRoundTripsAtProtocolV14(t *testing.T) {
	if CurrentProtocolVersion != 15 {
		t.Fatalf("CurrentProtocolVersion = %d, want 15", CurrentProtocolVersion)
	}
	if SupportsMessenger(MessengerMinProtocolVersion - 1) {
		t.Fatal("v13 must not claim resident messenger support")
	}
	if !SupportsMessenger(MessengerMinProtocolVersion) {
		t.Fatal("v14 must claim resident messenger support")
	}

	d := Dispatch{JobID: "j1", Messenger: &MessengerDispatch{
		SessionName: "claude-main", Command: []string{"claude", "-p", "hello"},
		Cwd: "workspace", TimeoutSec: 30, IdleSec: 600,
	}}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var got Dispatch
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Messenger == nil || got.Messenger.SessionName != "claude-main" || got.Messenger.Cwd != "workspace" {
		t.Fatalf("messenger dispatch = %+v", got.Messenger)
	}
	var old Dispatch
	if err := json.Unmarshal([]byte(`{"job_id":"old"}`), &old); err != nil || old.Messenger != nil {
		t.Fatalf("old dispatch decode = %+v, err=%v", old, err)
	}
}

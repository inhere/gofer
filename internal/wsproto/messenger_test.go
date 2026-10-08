package wsproto

import (
	"encoding/json"
	"testing"
)

func TestMessengerDispatchRoundTripsAtProtocolV14(t *testing.T) {
	if CurrentProtocolVersion != 19 {
		t.Fatalf("CurrentProtocolVersion = %d, want 19", CurrentProtocolVersion)
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

func TestMessengerListGateAndPingStateAreAdditive(t *testing.T) {
	if SupportsMessengerList(MessengerListMinProtocolVersion-1) || !SupportsMessengerList(MessengerListMinProtocolVersion) {
		t.Fatal("list_agents must be gated at MessengerListMinProtocolVersion")
	}
	if MessengerListMinProtocolVersion <= MessengerMinProtocolVersion {
		t.Fatal("list_agents gate must be a newer level than the resident messenger")
	}
	// An old worker's ping (no state) still decodes; a new one carries the state.
	var old Ping
	if err := json.Unmarshal([]byte(`{"ts":5}`), &old); err != nil || old.Messenger != nil || old.Dirs != nil {
		t.Fatalf("old ping = %+v, err=%v", old, err)
	}
	in := Ping{TS: 6, Messenger: &MessengerSnapshot{Status: "idle", Deliveries: []MessengerDelivery{{At: 1, OK: true}}},
		Dirs: &WorkDirs{Workspace: &WorkDir{Path: "/w", Exists: true}, Roots: []WorkRoot{{From: "/a", To: "/b"}}, Projects: []ProjectDir{{Key: "p", Path: "/b/p"}}}}
	b, _ := json.Marshal(in)
	var got Ping
	if err := json.Unmarshal(b, &got); err != nil || got.Messenger.Status != "idle" || got.Dirs.Roots[0].To != "/b" || got.Dirs.Workspace.Path != "/w" {
		t.Fatalf("round trip = %+v, err=%v", got, err)
	}
	var d Dispatch
	if err := json.Unmarshal([]byte(`{"job_id":"j","messenger":{"op":"list_agents"}}`), &d); err != nil || d.Messenger.Op != "list_agents" {
		t.Fatalf("dispatch op = %+v, err=%v", d, err)
	}
}

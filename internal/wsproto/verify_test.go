package wsproto

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDispatchRoundTripsVerify: the verify step rides Dispatch additively (P2) —
// the hub resolved the argv and its independent timeout, and the worker that
// EXECUTES the job must receive both verbatim. A dispatch from a hub that predates
// the fields still decodes, and a plain dispatch never grows the keys.
func TestDispatchRoundTripsVerify(t *testing.T) {
	d := Dispatch{
		JobID: "j1", ProjectKey: "p", Agent: "exec", Runner: "local",
		Verify:           []string{"go", "test", "./..."},
		VerifyTimeoutSec: 120,
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal dispatch: %v", err)
	}
	var back Dispatch
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal dispatch: %v", err)
	}
	if strings.Join(back.Verify, " ") != "go test ./..." {
		t.Fatalf("verify argv round trip = %v, want [go test ./...]", back.Verify)
	}
	if back.VerifyTimeoutSec != 120 {
		t.Fatalf("verify_timeout_sec round trip = %d, want 120", back.VerifyTimeoutSec)
	}

	// Additive: a pre-P2 hub's frame (neither key) decodes to the zero values.
	var old Dispatch
	if err := json.Unmarshal([]byte(`{"job_id":"j2","project_key":"p","agent":"exec","runner":"local"}`), &old); err != nil {
		t.Fatalf("a dispatch without the verify fields must still decode: %v", err)
	}
	if len(old.Verify) != 0 || old.VerifyTimeoutSec != 0 {
		t.Fatalf("absent fields decoded to %v/%d, want empty", old.Verify, old.VerifyTimeoutSec)
	}

	// A plain dispatch stays byte-identical to before (no empty keys on the wire).
	plain, err := json.Marshal(Dispatch{JobID: "j3"})
	if err != nil {
		t.Fatalf("marshal plain dispatch: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(plain, &raw); err != nil {
		t.Fatalf("unmarshal plain dispatch: %v", err)
	}
	for _, k := range []string{"verify", "verify_timeout_sec"} {
		if _, ok := raw[k]; ok {
			t.Fatalf("a dispatch without a verify step must not carry %q: %s", k, plain)
		}
	}
}

// TestSupportsVerify: v8 is where the verify dispatch fields (and the job_event
// frame) enter the protocol. v7 and below must not be told they carry it, and the
// hub's own version must not be below its capability floor — a hub that refused its
// own dispatches would be a self-inflicted outage.
func TestSupportsVerify(t *testing.T) {
	if VerifyMinProtocolVersion != 8 {
		t.Fatalf("VerifyMinProtocolVersion = %d, want 8", VerifyMinProtocolVersion)
	}
	if SupportsVerify(VerifyMinProtocolVersion - 1) {
		t.Errorf("v%d must not claim verify support", VerifyMinProtocolVersion-1)
	}
	if !SupportsVerify(VerifyMinProtocolVersion) {
		t.Errorf("v%d must claim verify support", VerifyMinProtocolVersion)
	}
	if CurrentProtocolVersion < VerifyMinProtocolVersion {
		t.Fatalf("CurrentProtocolVersion = %d, below the verify floor %d", CurrentProtocolVersion, VerifyMinProtocolVersion)
	}
	if !SupportsVerify(CurrentProtocolVersion) {
		t.Fatalf("this build (v%d) must support verify", CurrentProtocolVersion)
	}
}

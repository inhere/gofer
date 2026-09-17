package wsproto

import (
	"encoding/json"
	"testing"
)

// TestDispatchRoundTripsSessionID: the resume plumbing rides Dispatch additively —
// a hub that resolved a continuation sets session_id/resumed_from and the worker
// must receive both verbatim (it needs the session to LOAD and the marker that says
// "this is a resume, not a plain session binding"), while a dispatch from a hub
// that predates the fields still decodes.
func TestDispatchRoundTripsSessionID(t *testing.T) {
	d := Dispatch{
		JobID: "j1", ProjectKey: "p", Agent: "acpbot", Runner: "local",
		SessionID: "sess-1", ResumedFrom: "job-src",
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal dispatch: %v", err)
	}
	var back Dispatch
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal dispatch: %v", err)
	}
	if back.SessionID != d.SessionID || back.ResumedFrom != d.ResumedFrom {
		t.Fatalf("session round trip = %q/%q, want %q/%q", back.SessionID, back.ResumedFrom, d.SessionID, d.ResumedFrom)
	}

	// Additive: a pre-S2 hub's frame (neither key) decodes to the zero values and a
	// plain dispatch never grows the keys on the wire.
	var old Dispatch
	if err := json.Unmarshal([]byte(`{"job_id":"j2","project_key":"p","agent":"codex","runner":"local"}`), &old); err != nil {
		t.Fatalf("a dispatch without the S2 fields must still decode: %v", err)
	}
	if old.SessionID != "" || old.ResumedFrom != "" {
		t.Fatalf("absent fields decoded to %q/%q, want empty", old.SessionID, old.ResumedFrom)
	}
	plain, err := json.Marshal(Dispatch{JobID: "j3"})
	if err != nil {
		t.Fatalf("marshal plain dispatch: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(plain, &raw); err != nil {
		t.Fatalf("unmarshal plain dispatch: %v", err)
	}
	for _, k := range []string{"session_id", "resumed_from"} {
		if _, ok := raw[k]; ok {
			t.Fatalf("an unset %s must not reach the wire: %s", k, plain)
		}
	}
}

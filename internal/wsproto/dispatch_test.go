package wsproto

import (
	"encoding/json"
	"testing"
)

// TestDispatchRoundTripsSessionIDAndReadOnly: the S2 plumbing rides Dispatch
// additively — a hub that resolved a continuation sets session_id/resumed_from (the
// worker needs the session to LOAD and the marker that says "this is a resume, not a
// plain session binding") and read_only, and the worker must receive all three
// verbatim; a dispatch from a hub that predates them still decodes.
func TestDispatchRoundTripsSessionIDAndReadOnly(t *testing.T) {
	d := Dispatch{
		JobID: "j1", ProjectKey: "p", Agent: "acpbot", Runner: "local",
		SessionID: "sess-1", ResumedFrom: "job-src", ReadOnly: true,
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
	if !back.ReadOnly {
		t.Fatalf("read_only did not round-trip: %s", b)
	}

	// Additive: a pre-S2 hub's frame (none of the keys) decodes to the zero values and
	// a plain dispatch never grows the keys on the wire.
	var old Dispatch
	if err := json.Unmarshal([]byte(`{"job_id":"j2","project_key":"p","agent":"codex","runner":"local"}`), &old); err != nil {
		t.Fatalf("a dispatch without the S2 fields must still decode: %v", err)
	}
	if old.SessionID != "" || old.ResumedFrom != "" || old.ReadOnly {
		t.Fatalf("absent fields decoded to %q/%q/%v, want empty/false", old.SessionID, old.ResumedFrom, old.ReadOnly)
	}
	plain, err := json.Marshal(Dispatch{JobID: "j3"})
	if err != nil {
		t.Fatalf("marshal plain dispatch: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(plain, &raw); err != nil {
		t.Fatalf("unmarshal plain dispatch: %v", err)
	}
	for _, k := range []string{"session_id", "resumed_from", "read_only"} {
		if _, ok := raw[k]; ok {
			t.Fatalf("an unset %s must not reach the wire: %s", k, plain)
		}
	}
}

// TestDispatchRoundTripsInitialInput: path B's priming text rides Dispatch
// additively (design §9.1 B) — the hub resolved what to type and how long the
// executing machine must wait for the TUI, and the worker that RUNS the pty must
// receive both verbatim. A dispatch from a hub that predates the fields still
// decodes, and a plain dispatch never grows the keys.
func TestDispatchRoundTripsInitialInput(t *testing.T) {
	d := Dispatch{
		JobID: "j1", ProjectKey: "p", Agent: "exec", Runner: "local",
		Interactive: true, InitialInput: "[gofer web 回复] carry on\r", InitialInputQuietMs: 1500,
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal dispatch: %v", err)
	}
	var back Dispatch
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal dispatch: %v", err)
	}
	if back.InitialInput != d.InitialInput || back.InitialInputQuietMs != d.InitialInputQuietMs {
		t.Fatalf("initial input round trip = %q/%d, want %q/%d",
			back.InitialInput, back.InitialInputQuietMs, d.InitialInput, d.InitialInputQuietMs)
	}

	// Additive: a pre-B hub's frame (neither key) decodes to the zero values.
	var old Dispatch
	if err := json.Unmarshal([]byte(`{"job_id":"j2","project_key":"p","agent":"codex","runner":"local"}`), &old); err != nil {
		t.Fatalf("a dispatch without the initial-input fields must still decode: %v", err)
	}
	if old.InitialInput != "" || old.InitialInputQuietMs != 0 {
		t.Fatalf("absent fields decoded to %q/%d, want empty/0", old.InitialInput, old.InitialInputQuietMs)
	}
	plain, err := json.Marshal(Dispatch{JobID: "j3"})
	if err != nil {
		t.Fatalf("marshal plain dispatch: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(plain, &raw); err != nil {
		t.Fatalf("unmarshal plain dispatch: %v", err)
	}
	for _, k := range []string{"initial_input", "initial_input_quiet_ms"} {
		if _, ok := raw[k]; ok {
			t.Fatalf("an unset %s must not reach the wire: %s", k, plain)
		}
	}
}

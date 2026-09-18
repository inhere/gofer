package wsproto

import (
	"encoding/json"
	"testing"
)

// TestJobEventRoundTrip pins the worker→hub event frame (SUP-01 G): the event type
// and its identity travel verbatim, the detail stays raw JSON (wsproto never learns
// the job package's vocabulary), and an old peer that never sends the frame is
// unaffected — the envelope simply never carries the type.
func TestJobEventRoundTrip(t *testing.T) {
	ev := JobEvent{
		JobID:         "job-1",
		Type:          "job.permission_requested",
		Detail:        json.RawMessage(`{"interaction_id":"i-1","kind":"edit"}`),
		TS:            1700000000,
		InteractionID: "i-1",
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal job event: %v", err)
	}
	var back JobEvent
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal job event: %v", err)
	}
	if back.JobID != ev.JobID || back.Type != ev.Type || back.TS != ev.TS || back.InteractionID != ev.InteractionID {
		t.Fatalf("job event round trip = %+v, want %+v", back, ev)
	}
	var detail map[string]any
	if err := json.Unmarshal(back.Detail, &detail); err != nil {
		t.Fatalf("detail is not JSON after the round trip: %v", err)
	}
	if detail["interaction_id"] != "i-1" || detail["kind"] != "edit" {
		t.Fatalf("detail round trip = %v, want the worker's payload verbatim", detail)
	}

	// The frame travels inside the shared envelope, keyed by the hub-side job id.
	raw, err := EncodeFrame(TypeJobEvent, ev.JobID, ev)
	if err != nil {
		t.Fatalf("encode frame: %v", err)
	}
	env, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Type != TypeJobEvent || env.JobID != "job-1" {
		t.Fatalf("envelope = %+v, want type %s for job-1", env, TypeJobEvent)
	}
	if _, err := As[JobEvent](env); err != nil {
		t.Fatalf("decode job event payload: %v", err)
	}

	// A body-less event (no detail) still decodes, and an event without an
	// interaction id leaves the field out entirely.
	plain, err := json.Marshal(JobEvent{JobID: "job-2", Type: "job.verify_started", TS: 1})
	if err != nil {
		t.Fatalf("marshal plain job event: %v", err)
	}
	var rawMap map[string]any
	if err := json.Unmarshal(plain, &rawMap); err != nil {
		t.Fatalf("unmarshal plain job event: %v", err)
	}
	if _, ok := rawMap["interaction_id"]; ok {
		t.Fatalf("an event without an interaction must not carry the key: %s", plain)
	}
}

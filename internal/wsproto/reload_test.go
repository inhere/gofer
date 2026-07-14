package wsproto

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestReloadFrameRoundTrip: the request frame survives Encode → Decode → As with
// the RequestID (the receipt correlation key) intact, with and without a Reason.
func TestReloadFrameRoundTrip(t *testing.T) {
	for name, want := range map[string]Reload{
		"with reason": {RequestID: "req-1", Reason: "config changed"},
		"no reason":   {RequestID: "req-2"},
	} {
		t.Run(name, func(t *testing.T) {
			b, err := EncodeFrame(TypeReload, "", want)
			if err != nil {
				t.Fatalf("EncodeFrame: %v", err)
			}
			env, err := DecodeEnvelope(b)
			if err != nil {
				t.Fatalf("DecodeEnvelope: %v", err)
			}
			if env.Type != TypeReload {
				t.Fatalf("type = %q, want %q", env.Type, TypeReload)
			}
			got, err := As[Reload](env)
			if err != nil {
				t.Fatalf("As[Reload]: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
			}
		})
	}
}

// TestReloadResultOKRoundTrip: a successful receipt carries the new capabilities
// in the SAME frame, and every Caps field survives the round-trip.
func TestReloadResultOKRoundTrip(t *testing.T) {
	want := ReloadResult{
		RequestID: "req-1",
		OK:        true,
		Caps: &Caps{
			Labels:   []string{"gpu", "linux"},
			Projects: []string{"proj-a", "proj-b"},
			Agents:   []string{"codex", "shell"},
			AgentCaps: []AgentBrief{
				{Key: "codex", Type: "agent", Interactive: true},
				{Key: "shell", Type: "exec"},
			},
			MaxConc: 4,
		},
	}

	b, err := EncodeFrame(TypeReloadResult, "", want)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	env, err := DecodeEnvelope(b)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if env.Type != TypeReloadResult {
		t.Fatalf("type = %q, want %q", env.Type, TypeReloadResult)
	}
	got, err := As[ReloadResult](env)
	if err != nil {
		t.Fatalf("As[ReloadResult]: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
	if got.Caps == nil || got.Caps.MaxConc != 4 || len(got.Caps.AgentCaps) != 2 ||
		!got.Caps.AgentCaps[0].Interactive || got.Caps.AgentCaps[1].Interactive {
		t.Fatalf("caps detail not preserved: %+v", got.Caps)
	}
}

// TestReloadResultFailRoundTrip: OK=false is the load-bearing value of the frame
// (worker refused the new config, kept the old one) — omitempty must not eat it,
// and the failure must arrive with its reason and WITHOUT caps.
func TestReloadResultFailRoundTrip(t *testing.T) {
	want := ReloadResult{RequestID: "req-1", OK: false, Err: "parse worker.yaml: bad yaml"}

	b, err := EncodeFrame(TypeReloadResult, "", want)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	env, err := DecodeEnvelope(b)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	got, err := As[ReloadResult](env)
	if err != nil {
		t.Fatalf("As[ReloadResult]: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
	if got.OK || got.Err == "" || got.Caps != nil {
		t.Fatalf("failure receipt corrupted: %+v", got)
	}

	// Wire check: "ok" must be present (explicit false), "caps" must be absent.
	var body map[string]json.RawMessage
	if err := json.Unmarshal(env.Payload, &body); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if raw, ok := body["ok"]; !ok || string(raw) != "false" {
		t.Fatalf(`payload "ok" = %s (present=%v), want explicit false`, raw, ok)
	}
	if _, ok := body["caps"]; ok {
		t.Fatalf("failure receipt must not carry caps: %s", env.Payload)
	}
}

// TestCapsFrameRoundTrip: the unsolicited capability re-report survives intact.
func TestCapsFrameRoundTrip(t *testing.T) {
	want := Caps{
		Labels:    []string{"gpu"},
		Projects:  []string{"proj-a"},
		Agents:    []string{"codex", "shell"},
		AgentCaps: []AgentBrief{{Key: "codex", Type: "agent", Interactive: true}, {Key: "shell", Type: "exec"}},
		MaxConc:   2,
	}

	b, err := EncodeFrame(TypeCaps, "", want)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	env, err := DecodeEnvelope(b)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if env.Type != TypeCaps {
		t.Fatalf("type = %q, want %q", env.Type, TypeCaps)
	}
	got, err := As[Caps](env)
	if err != nil {
		t.Fatalf("As[Caps]: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

// TestCapsEmptyIsExplicit: a reload that CLEARS a capability (all projects/agents
// removed, concurrency back to 0) must travel as an explicit empty value, not as
// an absent field — otherwise the hub cannot tell "no projects any more" from
// "nothing reported" and would keep routing on stale capabilities.
func TestCapsEmptyIsExplicit(t *testing.T) {
	empty := Caps{Labels: []string{}, Projects: []string{}, Agents: []string{}, AgentCaps: []AgentBrief{}}

	b, err := EncodeFrame(TypeCaps, "", empty)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	env, err := DecodeEnvelope(b)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(env.Payload, &body); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	for _, key := range []string{"labels", "projects", "agents", "agent_caps", "max_concurrent"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("caps key %q dropped from the wire (omitempty?): %s", key, env.Payload)
		}
	}

	got, err := As[Caps](env)
	if err != nil {
		t.Fatalf("As[Caps]: %v", err)
	}
	if !reflect.DeepEqual(got, empty) {
		t.Fatalf("empty caps round-trip mismatch:\n got %+v\nwant %+v", got, empty)
	}
}

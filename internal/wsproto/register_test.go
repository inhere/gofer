package wsproto

import (
	"reflect"
	"testing"
)

// TestRegisterFrameRoundTrip: the federation-extended register frame survives an
// EncodeFrame → DecodeEnvelope → As round-trip with every new field intact
// (protocol_version / arch / gofer_version / started_at / agent_caps), and the
// legacy Agents key list still rides alongside AgentCaps.
func TestRegisterFrameRoundTrip(t *testing.T) {
	want := Register{
		WorkerID:        "w1",
		InstanceID:      "inst-1",
		ProtocolVersion: ProtocolVersion,
		PtyCapable:      true,
		OS:              "linux",
		Arch:            "amd64",
		GoferVersion:    "1.2.3 (abc1234)",
		StartedAt:       1752300000,
		Labels:          []string{"gpu", "linux"},
		Projects:        []string{"proj-a", "proj-b"},
		Agents:          []string{"codex", "shell"},
		AgentCaps: []AgentBrief{
			{Key: "codex", Type: "agent", Interactive: true},
			{Key: "shell", Type: "exec"}, // Interactive false → omitempty on the wire
		},
		MaxConcurrent: 4,
	}

	b, err := EncodeFrame(TypeRegister, "", want)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	env, err := DecodeEnvelope(b)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if env.Type != TypeRegister {
		t.Fatalf("type = %q, want %q", env.Type, TypeRegister)
	}
	got, err := As[Register](env)
	if err != nil {
		t.Fatalf("As[Register]: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
	// The typed caps must survive with their detail (the whole point of AgentCaps).
	if len(got.AgentCaps) != 2 || got.AgentCaps[0].Key != "codex" ||
		got.AgentCaps[0].Type != "agent" || !got.AgentCaps[0].Interactive {
		t.Fatalf("agent_caps not preserved: %+v", got.AgentCaps)
	}
	if got.AgentCaps[1].Interactive {
		t.Fatalf("non-interactive agent decoded as interactive: %+v", got.AgentCaps[1])
	}
}

// TestRegisterOldFrameDecodes proves the extension is ADDITIVE: a pre-federation
// worker's register (no protocol_version / agent_caps) still decodes cleanly, with
// ProtocolVersion 0 and AgentCaps nil. This is what lets the hub answer such a
// worker with an explicit upgrade Reason instead of failing to parse the frame.
func TestRegisterOldFrameDecodes(t *testing.T) {
	raw := []byte(`{"type":"register","payload":{"worker_id":"w","agents":["a"]}}`)

	env, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("DecodeEnvelope(old frame): %v", err)
	}
	got, err := As[Register](env)
	if err != nil {
		t.Fatalf("As[Register](old frame): %v", err)
	}
	if got.ProtocolVersion != 0 {
		t.Fatalf("old frame ProtocolVersion = %d, want 0 (absent)", got.ProtocolVersion)
	}
	if got.AgentCaps != nil {
		t.Fatalf("old frame AgentCaps = %+v, want nil", got.AgentCaps)
	}
	if got.WorkerID != "w" || !reflect.DeepEqual(got.Agents, []string{"a"}) {
		t.Fatalf("old frame lost its known fields: %+v", got)
	}
	// A pre-federation worker is below the gate; a current one is not.
	if got.ProtocolVersion >= ProtocolVersion {
		t.Fatalf("old frame must be below the version gate (proto=%d, min=%d)",
			got.ProtocolVersion, ProtocolVersion)
	}
}

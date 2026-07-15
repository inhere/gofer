package wsproto

import (
	"encoding/json"
	"testing"
)

// TestSupportsPolicy: the policy capability is negotiated against the version the
// PEER reported, exactly like SupportsReload. Bumping Current to 4 (policy frames)
// must NOT retroactively make a v2/v3 worker "unsupported" at registration — those
// keep registering (Min stays 2), they simply do not receive a policy frame.
func TestSupportsPolicy(t *testing.T) {
	cases := []struct {
		proto int
		want  bool
	}{
		{0, false},                            // pre-federation (never registers anyway)
		{MinProtocolVersion, false},           // v2: registers fine, no policy frames
		{PolicyMinProtocolVersion - 1, false}, // v3: reload but no policy yet
		{PolicyMinProtocolVersion, true},      // v4: first version with policy
		{CurrentProtocolVersion, true},
		{CurrentProtocolVersion + 1, true}, // a newer worker than this hub still has it
	}
	for _, tc := range cases {
		if got := SupportsPolicy(tc.proto); got != tc.want {
			t.Fatalf("SupportsPolicy(%d) = %v, want %v", tc.proto, got, tc.want)
		}
	}
}

// TestPolicyVersionConstants pins the version matrix (验收 2): the floor stays 2 so no
// v2/v3 worker is evicted by shipping v4, and the policy floor is exactly the current
// implemented version.
func TestPolicyVersionConstants(t *testing.T) {
	if MinProtocolVersion != 2 {
		t.Fatalf("MinProtocolVersion = %d, want 2 (must not rise — would evict existing workers)", MinProtocolVersion)
	}
	if CurrentProtocolVersion != 4 {
		t.Fatalf("CurrentProtocolVersion = %d, want 4 (policy frames)", CurrentProtocolVersion)
	}
	if PolicyMinProtocolVersion != 4 {
		t.Fatalf("PolicyMinProtocolVersion = %d, want 4", PolicyMinProtocolVersion)
	}
	// SupportsPolicy must gate below the current build so a v2/v3 peer is never sent a
	// frame it cannot parse, yet both remain above the registration floor.
	if SupportsPolicy(MinProtocolVersion) {
		t.Fatal("a floor (v2) worker must not be considered policy-capable")
	}
}

// TestRegisteredOldServerProtocolVersionZero (验收 3): an OLD server's registered ack
// carries no protocol_version / policy. As[T] is a plain json.Unmarshal (no
// DisallowUnknownFields), so a new worker decodes the absent fields to their zero
// values — ProtocolVersion 0, Policy nil — and must treat 0 as "server predates this
// field", never as a real version. The frame still decodes its known fields cleanly.
func TestRegisteredOldServerProtocolVersionZero(t *testing.T) {
	// Literal wire an old server would emit: only accepted + server_time.
	oldWire := []byte(`{"accepted":true,"server_time":1745989200000}`)
	env := Envelope{Type: TypeRegistered, Payload: json.RawMessage(oldWire)}

	got, err := As[Registered](env)
	if err != nil {
		t.Fatalf("As[Registered] on an old-server frame: %v", err)
	}
	if !got.Accepted {
		t.Fatalf("known fields lost: Accepted = %v, want true", got.Accepted)
	}
	if got.ProtocolVersion != 0 {
		t.Fatalf("old-server ProtocolVersion = %d, want 0 (absent field)", got.ProtocolVersion)
	}
	if got.Policy != nil {
		t.Fatalf("old-server Policy = %+v, want nil (absent field)", got.Policy)
	}
}

// TestPolicyProjectAllowedAgentsNullEqualsEmpty (验收 4): a downstream consumer must
// treat wire `null` and `[]` as equivalent for AllowedAgents — it judges by len, not
// nil-ness (MEDIUM-1). Both wire forms must decode to an empty (len 0) slice.
func TestPolicyProjectAllowedAgentsNullEqualsEmpty(t *testing.T) {
	forms := map[string][]byte{
		"null":  []byte(`{"key":"k","host_path":"/p","allowed_agents":null,"interactive_allowed_agents":null,"allow_exec":true}`),
		"empty": []byte(`{"key":"k","host_path":"/p","allowed_agents":[],"interactive_allowed_agents":[],"allow_exec":true}`),
	}
	for name, wire := range forms {
		env := Envelope{Type: TypePolicy, Payload: json.RawMessage(wire)}
		got, err := As[PolicyProject](env)
		if err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		if len(got.AllowedAgents) != 0 {
			t.Fatalf("%s: len(AllowedAgents) = %d, want 0", name, len(got.AllowedAgents))
		}
		if len(got.InteractiveAllowedAgents) != 0 {
			t.Fatalf("%s: len(InteractiveAllowedAgents) = %d, want 0", name, len(got.InteractiveAllowedAgents))
		}
	}
}

// TestPolicyProjectNilSliceMarshalsToNull documents the wire reality (MEDIUM-1): a Go
// nil slice marshals to `null` EVEN WITHOUT omitempty, so the v0.2 assumption "no
// omitempty ⇒ always []" is false. The contract is len-equivalence downstream, NOT a
// guaranteed `[]` on the wire — this test pins the fact, so nobody "fixes" it by
// asserting [] elsewhere.
func TestPolicyProjectNilSliceMarshalsToNull(t *testing.T) {
	p := PolicyProject{Key: "k", HostPath: "/p", AllowExec: true} // AllowedAgents nil
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if string(m["allowed_agents"]) != "null" {
		t.Fatalf("nil AllowedAgents marshalled to %s, want null (Go nil slice → null)", m["allowed_agents"])
	}
}

// TestPolicyH2OptionalFields pins the H2 tri-state semantics of the two optional
// PolicyProject fields: MaxConcurrentJobs omitempty (absent == 0 == unlimited) and
// CaptureDiff *bool (absent == nil == default-on; only a present false is opt-out).
func TestPolicyH2OptionalFields(t *testing.T) {
	// Unset: neither key on the wire.
	unset := PolicyProject{Key: "k", HostPath: "/p"}
	b, _ := json.Marshal(unset)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	if _, ok := m["max_concurrent_jobs"]; ok {
		t.Fatal("MaxConcurrentJobs 0 must be omitted (== unlimited), but the key is present")
	}
	if _, ok := m["capture_diff"]; ok {
		t.Fatal("CaptureDiff nil must be omitted (== default-on), but the key is present")
	}

	// Present explicit false: capture_diff must survive as false, distinguishable from absent.
	no := false
	set := PolicyProject{Key: "k", HostPath: "/p", MaxConcurrentJobs: 3, CaptureDiff: &no}
	b2, _ := json.Marshal(set)
	env := Envelope{Type: TypePolicy, Payload: json.RawMessage(b2)}
	got, err := As[PolicyProject](env)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.MaxConcurrentJobs != 3 {
		t.Fatalf("MaxConcurrentJobs = %d, want 3", got.MaxConcurrentJobs)
	}
	if got.CaptureDiff == nil || *got.CaptureDiff != false {
		t.Fatalf("CaptureDiff = %v, want a non-nil false (explicit opt-out preserved)", got.CaptureDiff)
	}
}

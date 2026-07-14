package wsproto

import (
	"encoding/json"
	"testing"
)

// TestAgentBriefOldWorkerReportsUnknownAvailability is the *bool decision's regression
// lock. A worker built before P2 sends agent_caps WITHOUT an `available` key. With a
// plain bool that absence would decode as false — indistinguishable from a new worker
// saying "the CLI is not there" — and any consumer filtering on it would wipe out every
// agent of every old worker in the fleet. It must decode as nil (unknown) instead.
func TestAgentBriefOldWorkerReportsUnknownAvailability(t *testing.T) {
	const oldFrame = `{"worker_id":"w1","protocol_version":3,` +
		`"agents":["claude","exec"],` +
		`"agent_caps":[{"key":"claude","type":"cli-agent","interactive":true},{"key":"exec","type":"exec"}]}`

	var got Register
	if err := json.Unmarshal([]byte(oldFrame), &got); err != nil {
		t.Fatalf("decode old register frame: %v", err)
	}
	if len(got.AgentCaps) != 2 {
		t.Fatalf("old worker's agent_caps lost entries: %+v", got.AgentCaps)
	}
	for _, b := range got.AgentCaps {
		if b.Available != nil {
			t.Fatalf("agent %q of an OLD worker decoded Available=%v; a worker that never "+
				"reported the field must stay unknown (nil), never a false that reads as "+
				"'cannot run'", b.Key, *b.Available)
		}
		if b.Version != "" {
			t.Fatalf("agent %q invented a version: %q", b.Key, b.Version)
		}
	}
}

// TestAgentBriefAvailabilityRoundTrip: a new worker's explicit false survives the wire
// as an explicit false (distinct from the old worker's nil above), and true carries the
// version string along.
func TestAgentBriefAvailabilityRoundTrip(t *testing.T) {
	no, yes := false, true
	want := Caps{
		Agents: []string{"claude", "ghost"},
		AgentCaps: []AgentBrief{
			{Key: "claude", Type: "cli-agent", Interactive: true, Available: &yes, Version: "2.1.208"},
			{Key: "ghost", Type: "cli-agent", Available: &no}, // declared, CLI not found — still advertised
		},
	}

	b, err := EncodeFrame(TypeCaps, "", want)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	env, err := DecodeEnvelope(b)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	got, err := As[Caps](env)
	if err != nil {
		t.Fatalf("As[Caps]: %v", err)
	}
	if len(got.AgentCaps) != 2 {
		t.Fatalf("agent_caps lost entries: %+v", got.AgentCaps)
	}
	c := got.AgentCaps[0]
	if c.Available == nil || !*c.Available || c.Version != "2.1.208" {
		t.Fatalf("available agent round-trip broke: %+v (available=%v)", c, c.Available)
	}
	g := got.AgentCaps[1]
	if g.Available == nil || *g.Available {
		t.Fatalf("an explicit available=false must decode as a non-nil false, got %v", g.Available)
	}
}

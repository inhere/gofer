package commands

import (
	"reflect"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/wsproto"
)

// TestAgentBriefs: the worker's agent map projects onto the typed capability report
// it sends on register — key + type + interactive, sorted by key so the reported
// order is stable across registers (Go map iteration is not).
func TestAgentBriefs(t *testing.T) {
	got := agentBriefs(map[string]config.AgentConfig{
		"shell": {Type: "exec"},
		"codex": {Type: "agent", Interactive: true},
		"aider": {Type: "agent", Command: "aider"}, // Interactive defaults to false
	})
	want := []wsproto.AgentBrief{
		{Key: "aider", Type: "agent", Interactive: false},
		{Key: "codex", Type: "agent", Interactive: true},
		{Key: "shell", Type: "exec", Interactive: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agentBriefs:\n got %+v\nwant %+v", got, want)
	}
}

// TestAgentBriefsEmpty: no agents → nil (omitempty on the wire, not an empty list).
func TestAgentBriefsEmpty(t *testing.T) {
	if got := agentBriefs(nil); got != nil {
		t.Fatalf("agentBriefs(nil) = %+v, want nil", got)
	}
	if got := agentBriefs(map[string]config.AgentConfig{}); got != nil {
		t.Fatalf("agentBriefs(empty) = %+v, want nil", got)
	}
}

// TestAgentBriefsKeysMatchAgentKeys: AgentCaps and the back-compat Agents key list
// are built from the SAME map, so the hub can rely on them describing the same set
// (the redundancy is intentional until every consumer reads AgentCaps).
func TestAgentBriefsKeysMatchAgentKeys(t *testing.T) {
	agents := map[string]config.AgentConfig{
		"codex": {Type: "agent", Interactive: true},
		"shell": {Type: "exec"},
	}
	keys := agentKeys(agents)
	briefs := agentBriefs(agents)
	if len(keys) != len(briefs) {
		t.Fatalf("agentKeys=%v vs agentBriefs=%v: length mismatch", keys, briefs)
	}
	inBriefs := map[string]bool{}
	for _, b := range briefs {
		inBriefs[b.Key] = true
	}
	for _, k := range keys {
		if !inBriefs[k] {
			t.Fatalf("agent key %q reported in Agents but missing from AgentCaps", k)
		}
	}
}

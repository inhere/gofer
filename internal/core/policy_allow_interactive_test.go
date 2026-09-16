package core

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestPolicyCarriesAllowInteractive pins the wire form of the AGT-02 project switch:
// the policy carries the RESOLVED value (explicit switch if written, else the legacy
// interactive_allowed_agents reading), always present — an explicit false must
// actually travel, because the worker reads an ABSENT field as "pre-AGT-02 server"
// and falls back to the legacy list.
func TestPolicyCarriesAllowInteractive(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }
	cfg := &config.Config{
		Runners: map[string]config.RunnerConfig{"pool": {Type: "worker"}},
		Projects: map[string]config.ProjectConfig{
			"on":     {HostPath: "/srv/on", AllowedRunners: []string{"pool"}, AllowInteractive: boolPtr(true)},
			"off":    {HostPath: "/srv/off", AllowedRunners: []string{"pool"}, AllowInteractive: boolPtr(false), InteractiveAllowedAgents: []string{"claude"}},
			"legacy": {HostPath: "/srv/legacy", AllowedRunners: []string{"pool"}, InteractiveAllowedAgents: []string{"claude"}},
			"unset":  {HostPath: "/srv/unset", AllowedRunners: []string{"pool"}},
		},
	}

	got := policyByKey(computePolicy(cfg, "w-any", 3))
	want := map[string]bool{"on": true, "off": false, "legacy": true, "unset": false}
	for key, wantAllow := range want {
		pp, ok := got[key]
		if !ok {
			t.Fatalf("project %q missing from the policy", key)
		}
		if pp.AllowInteractive == nil {
			t.Fatalf("%q AllowInteractive = nil, want an explicit %v (the worker must not have to re-derive it)", key, wantAllow)
		}
		if *pp.AllowInteractive != wantAllow {
			t.Errorf("%q AllowInteractive = %v, want %v", key, *pp.AllowInteractive, wantAllow)
		}
	}

	// The narrowing list still rides along verbatim: it is what the worker's own
	// admission narrows with, independent of the switch.
	if !slices.Equal(got["off"].InteractiveAllowedAgents, []string{"claude"}) {
		t.Errorf("off interactive_allowed_agents = %v, want [claude]", got["off"].InteractiveAllowedAgents)
	}

	// Wire assertion: omitempty on a *bool drops only nil, so an explicit denial is
	// serialised. Without it the worker could not tell "denied" from "old server".
	raw, err := json.Marshal(got["off"])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"allow_interactive":false`) {
		t.Fatalf("explicit false must travel on the wire: %s", raw)
	}
}

package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// availTestConfig declares one cli-agent whose command is deliberately NOT on PATH,
// so any live probe reports it unavailable — which is what makes a cached
// available=true unambiguous evidence that no probe ran.
func availTestConfig() *config.Config {
	return &config.Config{
		Agents: map[string]config.AgentConfig{
			"ghost": {Type: TypeCLIAgent, Command: "gofer-ghost-cli-not-installed", Args: []string{"{{prompt}}"}},
		},
	}
}

// TestAvailabilityServesSeededCache: a seeded registry answers from the cache and
// never re-probes. The sentinel proves it: `ghost` is not on PATH, so a live probe
// could only ever say unavailable — reading back available=true + the seeded version
// means the answer came from the one detect pass core.Build already paid for.
func TestAvailabilityServesSeededCache(t *testing.T) {
	seed := map[string]DetectResult{
		"ghost": {Available: true, Version: "9.9.9"},
		"exec":  {Available: true, Version: "builtin"},
	}
	r := NewRegistryWith(availTestConfig(), seed)

	for i := range 3 { // repeated reads (a browser refresh / an agent hammering ListAgents)
		got := r.Availability()
		if !got["ghost"].Available || got["ghost"].Version != "9.9.9" {
			t.Fatalf("call %d: ghost = %+v, want cached {available true, version 9.9.9}", i, got["ghost"])
		}
	}
}

// TestAvailabilityColdCacheProbesLive: a registry built OUTSIDE core (NewRegistry —
// the core-less CLI paths, test fixtures) has no detect pass to serve, and must NOT
// report every agent unavailable. That would be a false negative: an installed agent
// vanishing from /v1/agents and the MCP tool. It probes live once instead.
func TestAvailabilityColdCacheProbesLive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI shim is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "gofer-fake-cli")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho fake 1.2.3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"fake":  {Type: TypeCLIAgent, Command: "gofer-fake-cli", Args: []string{"{{prompt}}"}},
		"ghost": {Type: TypeCLIAgent, Command: "gofer-ghost-cli-not-installed"},
	}}
	r := NewRegistry(cfg) // cold

	got := r.Availability()
	if !got["fake"].Available {
		t.Fatalf("cold cache reported an INSTALLED agent unavailable (false negative): %+v", got["fake"])
	}
	if got["fake"].Version != "fake 1.2.3" {
		t.Fatalf("fake version = %q, want %q", got["fake"].Version, "fake 1.2.3")
	}
	if got["ghost"].Available {
		t.Fatalf("ghost is not on PATH, want unavailable: %+v", got["ghost"])
	}

	// ...and memoizes it: with the shim gone from PATH, a second call that re-probed
	// would flip `fake` to unavailable. It must still read available (cache hit).
	t.Setenv("PATH", t.TempDir())
	if again := r.Availability(); !again["fake"].Available {
		t.Fatalf("cold cache was not memoized: second call re-probed, fake = %+v", again["fake"])
	}
}

// TestAvailabilityExecAlwaysAvailable: the BUILT-IN exec agent has no external CLI to
// find, so it is available by construction — a Detector that reports nothing about it
// (NoopDetector in every hermetic test, or a Windows host with no `sh` to run a
// configured exec probe with) must not be able to make it disappear.
func TestAvailabilityExecAlwaysAvailable(t *testing.T) {
	r := NewRegistryWith(availTestConfig(), nil) // seeded, but the detector reported nothing

	got := r.Availability()
	if !got["exec"].Available {
		t.Fatalf("built-in exec must always be available, got %+v", got["exec"])
	}
	// An agent the detect pass did not report IS unavailable (Detector contract) — but
	// only a declared CLI agent, never the built-in.
	if got["ghost"].Available {
		t.Fatalf("ghost was not reported by the detector, want unavailable: %+v", got["ghost"])
	}
}

// TestReloadWithSwapsCache: the availability cache turns over WITH the config, in one
// atomic swap — a reader can never pair a new config with the previous config's detect
// results, and a CLI installed since boot shows up after the reload's detect pass.
func TestReloadWithSwapsCache(t *testing.T) {
	r := NewRegistryWith(availTestConfig(), map[string]DetectResult{
		"ghost": {Available: true, Version: "old"},
	})

	newCfg := &config.Config{Agents: map[string]config.AgentConfig{
		"fresh": {Type: TypeCLIAgent, Command: "gofer-fresh-cli-not-installed"},
	}}
	r.ReloadWith(newCfg, map[string]DetectResult{"fresh": {Available: true, Version: "new"}})

	got := r.Availability()
	if _, stale := got["ghost"]; stale {
		t.Fatalf("stale agent from the previous config still in the cache: %+v", got)
	}
	if !got["fresh"].Available || got["fresh"].Version != "new" {
		t.Fatalf("fresh = %+v, want the reload's detect result {available true, version new}", got["fresh"])
	}
}

// TestReloadColdsTheCache: the cfg-only Reload has no detect results to install, so it
// leaves the cache COLD (re-probed lazily) rather than serving the OLD config's
// verdicts against the NEW config's agents.
func TestReloadColdsTheCache(t *testing.T) {
	r := NewRegistryWith(availTestConfig(), map[string]DetectResult{"ghost": {Available: true, Version: "stale"}})
	r.Reload(&config.Config{Agents: map[string]config.AgentConfig{
		"ghost": {Type: TypeCLIAgent, Command: "gofer-ghost-cli-not-installed"},
	}})

	// Cold ⇒ live probe ⇒ the ghost command really is not on PATH.
	if got := r.Availability(); got["ghost"].Available || got["ghost"].Version == "stale" {
		t.Fatalf("Reload kept the previous config's detect result: %+v", got["ghost"])
	}
}

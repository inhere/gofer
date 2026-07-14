package serve

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/wshub"
	"github.com/inhere/gofer/internal/wsproto"
)

// proberFor builds a runner.PeerProber over a single peer-http runner pointing at
// baseURL, with a short per-probe timeout for fast tests.
func proberFor(name, baseURL string) *runner.PeerProber {
	cfg := &config.Config{Runners: map[string]config.RunnerConfig{
		name: {Type: "peer-http", BaseURL: baseURL},
	}}
	return runner.NewPeerProber(cfg, 2*time.Second)
}

// snapUp reports whether the named target's last probe was up.
func snapUp(p *runner.PeerProber, name string) bool {
	for _, r := range p.Snapshot() {
		if r.Name == name {
			return r.Up
		}
	}
	return false
}

// TestStartProbeLoopShutsDownClean: startProbeLoop probes once at startup, then
// closing stop must make the goroutine return promptly with no leak. We assert
// the startup probe landed (cache populated) and that closing stop is observed.
func TestStartProbeLoopShutsDownClean(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := proberFor("peer", srv.URL)
	stop := make(chan struct{})
	// A long interval so the only probe we rely on is the startup one; shutdown
	// must not wait for a tick.
	startProbeLoop(gcli.NewCommand("t", ""), p, time.Hour, stop)

	// The startup probe runs synchronously-then-async; poll until the cache fills.
	deadline := time.Now().Add(2 * time.Second)
	for len(p.Snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !snapUp(p, "peer") {
		t.Fatalf("startup probe did not populate cache: %+v", p.Snapshot())
	}

	// Closing stop must unblock the loop goroutine; with no synchronisation hook we
	// simply ensure it does not panic and returns (the per-round ctx is cancelled).
	close(stop)
	// Give the goroutine a moment to observe stop and return. A leaked goroutine
	// would be flagged by -race / the test binary, not here, but this exercises the
	// shutdown path.
	time.Sleep(20 * time.Millisecond)
}

// TestStartProbeLoopNilNoop: a nil prober (no peer-http runners) makes
// startProbeLoop a no-op that never starts a goroutine.
func TestStartProbeLoopNilNoop(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	startProbeLoop(gcli.NewCommand("t", ""), nil, time.Second, stop) // must not panic
}

// TestHubWorkerRegistryNilHub: the adapter is nil-safe — a nil hub reports every
// worker as not-connected (the handler renders that as `disconnected`).
func TestHubWorkerRegistryNilHub(t *testing.T) {
	a := hubWorkerRegistry{hub: nil}
	if _, ok := a.WorkerStatus("w1"); ok {
		t.Fatal("nil hub adapter should report ok=false")
	}
}

// TestBriefsFromSnapshot (P4 T4.1): the adapter maps the wshub snapshot's typed
// agent capabilities into httpapi's local AgentBrief type field-for-field, and an
// empty capability set maps to nil (so the view's json omitempty drops the field).
func TestBriefsFromSnapshot(t *testing.T) {
	snap := wshub.WorkerSnapshot{
		AgentCaps: []wsproto.AgentBrief{
			{Key: "exec", Type: "exec"},
			{Key: "claude", Type: "cli-agent", Interactive: true},
		},
	}
	got := briefsFromSnapshot(snap)
	if len(got) != 2 {
		t.Fatalf("want 2 briefs, got %+v", got)
	}
	if got[0].Key != "exec" || got[0].Type != "exec" || got[0].Interactive {
		t.Fatalf("exec brief wrong: %+v", got[0])
	}
	if got[1].Key != "claude" || got[1].Type != "cli-agent" || !got[1].Interactive {
		t.Fatalf("claude brief wrong: %+v", got[1])
	}
	if briefsFromSnapshot(wshub.WorkerSnapshot{}) != nil {
		t.Fatal("empty snapshot must map to nil (json omitempty)")
	}
}

// TestBriefsFromSnapshotAvailabilityPassthrough (P2 T3): availability/version are copied
// through as pure DISPLAY detail and never filter.
//
//   - An OLD worker (pre-P2 build) sends no `available` key at all → the brief carries
//     nil, and BOTH its agents still reach /v1/meta. Dropping them — or defaulting them
//     to false and letting some consumer grey them out — would blank the agent list of
//     every worker in a fleet mid-rollout.
//   - A NEW worker's explicit false (an operator-declared agent whose CLI the probe did
//     not find) is likewise reported, never filtered: that agent still runs.
func TestBriefsFromSnapshotAvailabilityPassthrough(t *testing.T) {
	no, yes := false, true
	snap := wshub.WorkerSnapshot{
		AgentCaps: []wsproto.AgentBrief{
			{Key: "old-a", Type: "cli-agent"},                 // old worker: field absent
			{Key: "old-b", Type: "exec"},                      // old worker: field absent
			{Key: "ghost", Type: "cli-agent", Available: &no}, // declared, CLI not found
			{Key: "claude", Type: "cli-agent", Available: &yes, Version: "2.1.208"},
		},
	}

	got := briefsFromSnapshot(snap)
	if len(got) != 4 {
		t.Fatalf("the view LOST agents (%d of 4 survived): %+v", len(got), got)
	}
	for _, b := range got[:2] {
		if b.Available != nil {
			t.Fatalf("old worker's agent %q became Available=%v; it must stay unknown (nil)",
				b.Key, *b.Available)
		}
	}
	if got[2].Available == nil || *got[2].Available {
		t.Fatalf("ghost must be reported available=false (and still listed): %+v", got[2])
	}
	if got[3].Available == nil || !*got[3].Available || got[3].Version != "2.1.208" {
		t.Fatalf("claude lost its availability/version: %+v", got[3])
	}
}

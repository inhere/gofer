package commands

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
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

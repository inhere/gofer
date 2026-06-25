package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// proberFor builds a PeerProber over a single peer-http runner pointing at
// baseURL, with a short per-probe timeout for fast tests.
func proberFor(name, baseURL string) *PeerProber {
	cfg := &config.Config{Runners: map[string]config.RunnerConfig{
		name: {Type: "peer-http", BaseURL: baseURL},
	}}
	return NewPeerProber(cfg, 2*time.Second)
}

// snapByName indexes the prober snapshot by runner name.
func snapByName(p *PeerProber) map[string]struct {
	up  bool
	err string
	lat int64
} {
	out := map[string]struct {
		up  bool
		err string
		lat int64
	}{}
	for _, r := range p.Snapshot() {
		out[r.Name] = struct {
			up  bool
			err string
			lat int64
		}{r.Up, r.Err, r.LatencyMS}
	}
	return out
}

// TestPeerProberNoTargets: with no peer-http runners NewPeerProber returns nil so
// serve can skip the loop entirely (zero behaviour change).
func TestPeerProberNoTargets(t *testing.T) {
	cfg := &config.Config{Runners: map[string]config.RunnerConfig{
		"local-ish": {Type: "worker", WorkerID: "w1"},
	}}
	if p := NewPeerProber(cfg, time.Second); p != nil {
		t.Fatalf("expected nil prober with no peer-http targets, got %+v", p)
	}
}

// TestPeerProberUp: a /health endpoint returning 200 yields an up result with a
// non-empty cache entry and no error.
func TestPeerProberUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	p := proberFor("peer", srv.URL)
	p.ProbeOnce(context.Background())

	snap := snapByName(p)
	r, ok := snap["peer"]
	if !ok || !r.up || r.err != "" {
		t.Fatalf("expected up result, got ok=%v %+v", ok, r)
	}
}

// TestPeerProberUpToDown (acceptance §8.3): a healthy peer probes up; after the
// peer is closed a re-probe flips it to down with a non-empty error — proving the
// host detects a killed peer within one probe round.
func TestPeerProberUpToDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	p := proberFor("peer", srv.URL)

	p.ProbeOnce(context.Background())
	if r := snapByName(p)["peer"]; !r.up {
		t.Fatalf("setup: expected up before close, got %+v", r)
	}

	// Kill the peer; the next probe round must observe a transport error → down.
	srv.Close()
	p.ProbeOnce(context.Background())
	r := snapByName(p)["peer"]
	if r.up || r.err == "" {
		t.Fatalf("expected down with error after peer closed, got %+v", r)
	}
}

// TestPeerProberNon2xxIsDown: a /health returning 503 is a down result (not up),
// carrying the unhealthy status in the error.
func TestPeerProberNon2xxIsDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	p := proberFor("peer", srv.URL)
	p.ProbeOnce(context.Background())
	r := snapByName(p)["peer"]
	if r.up || r.err == "" {
		t.Fatalf("expected down for non-2xx, got %+v", r)
	}
}

// TestPeerProberRace (-race): concurrent ProbeOnce writers and Snapshot readers
// must not data-race on the cache.
func TestPeerProberRace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := proberFor("peer", srv.URL)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); p.ProbeOnce(context.Background()) }()
		go func() { defer wg.Done(); _ = p.Snapshot() }()
	}
	wg.Wait()
}

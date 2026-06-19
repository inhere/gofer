package commands

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/wshub"
)

// hubWorkerRegistry adapts the ws-worker *wshub.Hub to httpapi's workerRegistry
// interface for the C6/P4 /v1/runners endpoint (D2: httpapi reads worker state
// through a narrow consumer-side interface, never importing wshub's types). It
// converts the registry snapshot (unix-seconds heartbeat) into httpapi's
// WorkerStatus (unix-millis heartbeat, matching the SR102 response convention).
type hubWorkerRegistry struct{ hub *wshub.Hub }

// WorkerStatus implements httpapi.workerRegistry: ok=false when the worker is
// offline / never connected (the handler renders that as `disconnected`).
func (a hubWorkerRegistry) WorkerStatus(workerID string) (httpapi.WorkerStatus, bool) {
	if a.hub == nil {
		return httpapi.WorkerStatus{}, false
	}
	snap, ok := a.hub.WorkerSnapshot(workerID)
	if !ok {
		return httpapi.WorkerStatus{}, false
	}
	return httpapi.WorkerStatus{
		Connected:     true,
		LastHeartbeat: snap.LastHeartbeat * 1000, // seconds → millis (SR102)
		InFlight:      snap.InFlight,
		Labels:        snap.Labels,
	}, true
}

// peerProber is the C6/P4 peer-http active health-probe component held by serve.
// It periodically GETs each peer-http runner's /health and caches the last
// result (up/down + millis timestamp + latency + error) under an RWMutex. The
// /v1/runners handler reads the cache via httpapi.Snapshot (never a live probe),
// so probing never blocks request handling (§7 D1/D4).
//
// It does NOT reuse the peer-http job-forwarding client (D1): the probe only
// needs a lightweight unauthenticated GET /health with its own timeout, so it
// uses a dedicated http.Client and stays decoupled from job dispatch.
type peerProber struct {
	targets []probeTarget
	client  *http.Client
	nowFn   func() time.Time

	mu    sync.RWMutex
	cache map[string]httpapi.ProbeResult
}

// probeTarget is one peer-http runner the prober polls: its runner name and the
// base URL whose /health is checked.
type probeTarget struct {
	name    string
	baseURL string
}

// newPeerProber builds a prober over the peer-http runners in cfg.Runners
// (type=="peer-http"). It returns nil when there are no such runners so serve can
// skip wiring the probe loop entirely (zero behaviour change without peers). The
// timeout bounds a single /health GET.
func newPeerProber(cfg *config.Config, timeout time.Duration) *peerProber {
	var targets []probeTarget
	for name, rc := range cfg.Runners {
		if rc.Type == "peer-http" {
			targets = append(targets, probeTarget{name: name, baseURL: rc.BaseURL})
		}
	}
	if len(targets) == 0 {
		return nil
	}
	return &peerProber{
		targets: targets,
		client:  &http.Client{Timeout: timeout},
		nowFn:   time.Now,
		cache:   make(map[string]httpapi.ProbeResult, len(targets)),
	}
}

// Snapshot returns a copy of the current cached probe results (one per target).
// It is the non-blocking read the /v1/runners handler uses; it takes only the
// RLock and never triggers a live probe. Implements httpapi's runnerProber.
func (p *peerProber) Snapshot() []httpapi.ProbeResult {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]httpapi.ProbeResult, 0, len(p.cache))
	for _, r := range p.cache {
		out = append(out, r)
	}
	return out
}

// probeOnce probes every target concurrently and writes the results into the
// cache. Each probe is bounded by ctx (the http.Client timeout is the hard cap);
// a non-2xx or transport error yields a down result with the error text. The
// write takes the full Lock once after all probes complete so a Snapshot reader
// never observes a half-updated set.
func (p *peerProber) probeOnce(ctx context.Context) {
	results := make([]httpapi.ProbeResult, len(p.targets))
	var wg sync.WaitGroup
	for i, t := range p.targets {
		wg.Add(1)
		go func(i int, t probeTarget) {
			defer wg.Done()
			results[i] = p.probeTarget(ctx, t)
		}(i, t)
	}
	wg.Wait()

	p.mu.Lock()
	for _, r := range results {
		p.cache[r.Name] = r
	}
	p.mu.Unlock()
}

// probeTarget performs one GET <base_url>/health and returns the probe result.
// 2xx => up; any non-2xx status or transport error => down with an error string.
// The latency is measured around the request regardless of outcome.
func (p *peerProber) probeTarget(ctx context.Context, t probeTarget) httpapi.ProbeResult {
	url := strings.TrimRight(t.baseURL, "/") + "/health"
	start := p.nowFn()
	res := httpapi.ProbeResult{Name: t.name, CheckedAt: start.UnixMilli()}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	resp, err := p.client.Do(req)
	res.LatencyMS = p.nowFn().Sub(start).Milliseconds()
	if err != nil {
		res.Err = err.Error()
		return res
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		res.Up = true
		return res
	}
	res.Err = "unhealthy: HTTP " + resp.Status
	return res
}

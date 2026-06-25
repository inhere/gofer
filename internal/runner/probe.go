package runner

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// ProbeResult is the cached outcome of one peer-http /health probe (C6/P4). It is
// the DTO the serve-side prober feeds into the httpapi handler through its
// consumer-side runnerProber interface. CheckedAt is a unix-millis timestamp
// (SR102, matching handleHealth's server_time); Up=false with a non-empty Err is
// a down result.
//
// It lives in the runner package (the probe's natural产物) so httpapi/serve
// reference runner.ProbeResult without runner ever importing httpapi (httpapi →
// runner is the正向 dependency; the reverse would be a cycle — D-B3).
type ProbeResult struct {
	Name      string
	Up        bool
	CheckedAt int64 // unix millis of the probe; 0 when never probed yet
	LatencyMS int64
	Err       string
}

// PeerProber is the C6/P4 peer-http active health-probe component held by serve.
// It periodically GETs each peer-http runner's /health and caches the last
// result (up/down + millis timestamp + latency + error) under an RWMutex. The
// /v1/runners handler reads the cache via Snapshot (never a live probe),
// so probing never blocks request handling (§7 D1/D4).
//
// It does NOT reuse the peer-http job-forwarding client (D1): the probe only
// needs a lightweight unauthenticated GET /health with its own timeout, so it
// uses a dedicated http.Client and stays decoupled from job dispatch.
type PeerProber struct {
	targets []probeTarget
	client  *http.Client
	nowFn   func() time.Time

	mu    sync.RWMutex
	cache map[string]ProbeResult
}

// probeTarget is one peer-http runner the prober polls: its runner name and the
// base URL whose /health is checked.
type probeTarget struct {
	name    string
	baseURL string
}

// NewPeerProber builds a prober over the peer-http runners in cfg.Runners
// (type=="peer-http"). It returns nil when there are no such runners so serve can
// skip wiring the probe loop entirely (zero behaviour change without peers). The
// timeout bounds a single /health GET.
func NewPeerProber(cfg *config.Config, timeout time.Duration) *PeerProber {
	var targets []probeTarget
	for name, rc := range cfg.Runners {
		if rc.Type == "peer-http" {
			targets = append(targets, probeTarget{name: name, baseURL: rc.BaseURL})
		}
	}
	if len(targets) == 0 {
		return nil
	}
	return &PeerProber{
		targets: targets,
		client:  &http.Client{Timeout: timeout},
		nowFn:   time.Now,
		cache:   make(map[string]ProbeResult, len(targets)),
	}
}

// TargetCount returns the number of peer-http targets the prober polls. It lets
// serve log the wired target count without exposing the unexported targets slice.
func (p *PeerProber) TargetCount() int { return len(p.targets) }

// Snapshot returns a copy of the current cached probe results (one per target).
// It is the non-blocking read the /v1/runners handler uses; it takes only the
// RLock and never triggers a live probe. Implements httpapi's runnerProber.
func (p *PeerProber) Snapshot() []ProbeResult {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]ProbeResult, 0, len(p.cache))
	for _, r := range p.cache {
		out = append(out, r)
	}
	return out
}

// ProbeOnce probes every target concurrently and writes the results into the
// cache. Each probe is bounded by ctx (the http.Client timeout is the hard cap);
// a non-2xx or transport error yields a down result with the error text. The
// write takes the full Lock once after all probes complete so a Snapshot reader
// never observes a half-updated set.
func (p *PeerProber) ProbeOnce(ctx context.Context) {
	results := make([]ProbeResult, len(p.targets))
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
func (p *PeerProber) probeTarget(ctx context.Context, t probeTarget) ProbeResult {
	url := strings.TrimRight(t.baseURL, "/") + "/health"
	start := p.nowFn()
	res := ProbeResult{Name: t.name, CheckedAt: start.UnixMilli()}

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

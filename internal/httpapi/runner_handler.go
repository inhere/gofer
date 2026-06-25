package httpapi

import (
	"net/http"
	"sort"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
)

// nowMillis returns the current unix time in milliseconds. It is a package var so
// tests can pin the clock for deterministic heartbeat-age assertions.
var nowMillis = func() int64 { return time.Now().UnixMilli() }

// Runner kind constants used by the /v1/runners view and the type switch.
const (
	runnerTypeLocal    = "local"
	runnerTypePeerHTTP = "peer-http"
	runnerTypeWorker   = "worker"
)

// Runner status values (C6/P4 §5). `unknown` covers a peer-http runner with no
// probe result yet, a worker with no registry wired (P3 not enabled), and any
// future/unrecognised runner type.
const (
	statusUp           = "up"           // local (always) / peer-http probe 2xx
	statusDown         = "down"         // peer-http probe failed
	statusConnected    = "connected"    // worker has a live connection
	statusDisconnected = "disconnected" // worker offline / never connected
	statusUnknown      = "unknown"      // no signal yet
)

// WorkerStatus is the read-only worker view the handler renders (C6/P4). It is
// produced by the serve-side adapter from the wshub registry snapshot, so httpapi
// never imports wshub's internal types. Connected=false means offline / never
// connected (rendered as `disconnected`).
type WorkerStatus struct {
	Connected     bool
	LastHeartbeat int64 // unix millis of the most recent inbound frame
	InFlight      int
	Labels        []string
}

// runnerProber is the consumer-side narrow interface (D2) the handler reads the
// peer-http probe cache through. The serve command injects an implementation;
// nil means no probing is wired (every peer-http row is `unknown`). Snapshot must
// be a non-blocking read of the cached results (never a live probe).
type runnerProber interface {
	Snapshot() []runner.ProbeResult
}

// workerRegistry is the consumer-side narrow interface (D2) the handler reads
// worker connection state through. nil means the registry is not wired (every
// worker row is `unknown`). WorkerStatus returns ok=false when the worker_id is
// unknown / offline.
type workerRegistry interface {
	WorkerStatus(workerID string) (WorkerStatus, bool)
}

// runnerView is one row of the /v1/runners response (C6/P4 §5). Type-specific
// blocks (Probe / Worker / BaseURL / WorkerID) are omitted when empty so a local
// runner stays a clean three-field object.
type runnerView struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Status string `json:"status"`

	// peer-http
	BaseURL string     `json:"base_url,omitempty"`
	Probe   *probeView `json:"probe,omitempty"`

	// worker
	WorkerID string      `json:"worker_id,omitempty"`
	Worker   *workerView `json:"worker,omitempty"`
}

// probeView is the peer-http probe detail (millis timestamp / latency / error).
type probeView struct {
	CheckedAt int64  `json:"checked_at"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// workerView is the worker connection detail. HeartbeatAgeMS is computed at read
// time from LastHeartbeat so the operator sees staleness directly.
type workerView struct {
	LastHeartbeat  int64    `json:"last_heartbeat"`
	HeartbeatAgeMS int64    `json:"heartbeat_age_ms"`
	InFlight       int      `json:"in_flight"`
	Labels         []string `json:"labels,omitempty"`
}

// handleListRunners returns the status of every configured runner plus the
// implicit `local` runner (C6/P4 §5). The list is always a non-nil array, so an
// empty runner set serialises as {"runners":[{local}]}. Rows are sorted by name
// for a stable response (local first). The handler only reads cached state (probe
// snapshot + registry snapshot) so it never blocks on a live probe.
func (s *Server) handleListRunners(c *rux.Context) {
	out := make([]runnerView, 0, len(s.runners)+1)

	// The implicit local runner is always present and always up (in-process).
	out = append(out, runnerView{Name: runnerTypeLocal, Type: runnerTypeLocal, Status: statusUp})

	probes := s.probeIndex()
	for name, rc := range s.runners {
		// A runner explicitly named "local" in config would collide with the
		// implicit row; skip it (local is always rendered above).
		if rc.Type == runnerTypeLocal || name == runnerTypeLocal {
			continue
		}
		out = append(out, s.renderRunner(name, rc, probes))
	}

	sort.Slice(out, func(i, j int) bool {
		// local first, then by name — deterministic regardless of map order.
		if out[i].Type == runnerTypeLocal && out[j].Type != runnerTypeLocal {
			return true
		}
		if out[j].Type == runnerTypeLocal && out[i].Type != runnerTypeLocal {
			return false
		}
		return out[i].Name < out[j].Name
	})

	c.JSON(http.StatusOK, map[string]any{"runners": out})
}

// probeIndex reads the prober snapshot (nil-safe) into a name→result map for O(1)
// lookup. A nil prober (no probing wired) yields an empty map so every peer-http
// row falls to `unknown`.
func (s *Server) probeIndex() map[string]runner.ProbeResult {
	idx := map[string]runner.ProbeResult{}
	if s.prober == nil {
		return idx
	}
	for _, pr := range s.prober.Snapshot() {
		idx[pr.Name] = pr
	}
	return idx
}

// renderRunner builds one runnerView from its config + the probe index. The
// per-type logic: local => up; peer-http => probe cache (up/down/unknown); worker
// => registry snapshot (connected/disconnected/unknown); unknown type => unknown.
func (s *Server) renderRunner(name string, rc config.RunnerConfig, probes map[string]runner.ProbeResult) runnerView {
	v := runnerView{Name: name, Type: rc.Type, Status: statusUnknown}
	switch rc.Type {
	case runnerTypeLocal:
		v.Status = statusUp
	case runnerTypePeerHTTP:
		v.BaseURL = rc.BaseURL
		if pr, ok := probes[name]; ok {
			v.Status = statusDown
			if pr.Up {
				v.Status = statusUp
			}
			v.Probe = &probeView{CheckedAt: pr.CheckedAt, LatencyMS: pr.LatencyMS, Error: pr.Err}
		}
		// no probe result yet (or no prober wired) => status stays `unknown`.
	case runnerTypeWorker:
		v.WorkerID = rc.WorkerID
		v.Status = s.renderWorkerStatus(rc.WorkerID, &v)
	}
	return v
}

// renderWorkerStatus resolves a worker runner's status from the registry and, when
// connected, fills v.Worker. A nil registry (P3 not wired) => `unknown`; a missing
// / offline worker => `disconnected`.
func (s *Server) renderWorkerStatus(workerID string, v *runnerView) string {
	if s.workers == nil {
		return statusUnknown
	}
	ws, ok := s.workers.WorkerStatus(workerID)
	if !ok || !ws.Connected {
		return statusDisconnected
	}
	age := nowMillis() - ws.LastHeartbeat
	if age < 0 {
		age = 0
	}
	v.Worker = &workerView{
		LastHeartbeat:  ws.LastHeartbeat,
		HeartbeatAgeMS: age,
		InFlight:       ws.InFlight,
		Labels:         ws.Labels,
	}
	return statusConnected
}

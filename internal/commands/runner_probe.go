package commands

import (
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

// workerCounts returns (connected, totalInFlight) across the config-registered
// worker set for the E16 gofer_workers_connected / gofer_worker_in_flight gauges.
// It is read at scrape time: it walks the allowed worker ids and counts only
// those the hub currently reports a live snapshot for (same liveness rule as the
// hubWorkerSelector), summing their in-flight job counts. A nil hub or empty
// worker set yields (0, 0).
func workerCounts(hub *wshub.Hub, allowed map[string]config.WorkerAuthConfig) (int, int) {
	if hub == nil {
		return 0, 0
	}
	connected, inflight := 0, 0
	for id := range allowed {
		snap, ok := hub.WorkerSnapshot(id)
		if !ok {
			continue
		}
		connected++
		inflight += snap.InFlight
	}
	return connected, inflight
}

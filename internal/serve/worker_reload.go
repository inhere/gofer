package serve

import (
	"context"
	"errors"

	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/wshub"
	"github.com/inhere/gofer/internal/wsproto"
)

// hubWorkerReloader adapts *wshub.Hub to httpapi's worker-reload seam. It is where
// the hub's ERROR TAXONOMY is translated into httpapi's transport-agnostic outcome:
// every errors.Is/errors.As against a wshub error lives here, once, so the HTTP layer
// can classify a reload without importing wshub (the D2/G022 boundary — httpapi must
// stay free of hub types, cf. hubWorkerRegistry above it).
//
// It adds no policy of its own: the deadline is the caller's ctx (httpapi owns the
// wait budget, since it is the N reported in the 504) and the worker's own reason
// travels through untouched.
type hubWorkerReloader struct{ hub *wshub.Hub }

// ReloadWorker implements httpapi's workerReloader: run the synchronous reload RPC
// and map its result onto an outcome.
func (a hubWorkerReloader) ReloadWorker(ctx context.Context, workerID, reason string) httpapi.WorkerReloadOutcome {
	if a.hub == nil {
		return httpapi.WorkerReloadOutcome{
			Status: httpapi.WorkerReloadFailed,
			Detail: "no ws-worker hub is wired on this server",
		}
	}

	caps, err := a.hub.ReloadWorker(ctx, workerID, reason)
	switch {
	case err == nil:
		// The hub applied these caps to its registry BEFORE returning, so the caps we
		// report and the ones /v1/meta serves next cannot disagree.
		return httpapi.WorkerReloadOutcome{Status: httpapi.WorkerReloadApplied, Caps: capsView(caps)}

	case errors.Is(err, wshub.ErrWorkerOffline):
		return httpapi.WorkerReloadOutcome{Status: httpapi.WorkerReloadOffline, Detail: err.Error()}

	case errors.Is(err, wshub.ErrWorkerTooOld):
		// Not a fault: the worker runs fine, it just has no reload frame to answer
		// with. The message already names the versions and says "upgrade and restart".
		return httpapi.WorkerReloadOutcome{Status: httpapi.WorkerReloadTooOld, Detail: err.Error()}

	case errors.Is(err, wshub.ErrReloadTimeout):
		return httpapi.WorkerReloadOutcome{Status: httpapi.WorkerReloadTimedOut, Detail: err.Error()}
	}

	// The worker answered and said no: its reason is the only thing that tells the
	// operator what to fix, so it is carried VERBATIM (not err.Error(), which prefixes
	// it) all the way to the response body.
	var rejected *wshub.ReloadRejected
	if errors.As(err, &rejected) {
		detail := rejected.Reason
		if detail == "" {
			detail = rejected.Error()
		}
		return httpapi.WorkerReloadOutcome{Status: httpapi.WorkerReloadRejected, Detail: detail}
	}

	return httpapi.WorkerReloadOutcome{Status: httpapi.WorkerReloadFailed, Detail: err.Error()}
}

// capsView converts the worker's capability snapshot into httpapi's local type
// (same reason as briefsFromSnapshot: httpapi names no wsproto type).
func capsView(caps wsproto.Caps) httpapi.WorkerCaps {
	out := httpapi.WorkerCaps{
		Labels:        caps.Labels,
		Projects:      caps.Projects,
		Agents:        caps.Agents,
		MaxConcurrent: caps.MaxConc,
	}
	for _, c := range caps.AgentCaps {
		out.AgentCaps = append(out.AgentCaps, httpapi.AgentBrief{Key: c.Key, Type: c.Type, Interactive: c.Interactive})
	}
	return out
}

package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gookit/rux/v2"
)

// Reload wait budget. defaultWorkerReloadWait is the whole round trip (frame out →
// worker re-reads its config and rebuilds its registries → receipt back), not just
// the network hop: long enough for a real reload, short enough that an operator's
// CLI never looks hung. A request may override it (timeout_sec) up to
// maxWorkerReloadWait — an unbounded wait would pin an HTTP handler on a silent
// worker forever, which is exactly the failure mode this endpoint exists to avoid.
const (
	defaultWorkerReloadWait = 10 * time.Second
	maxWorkerReloadWait     = 120 * time.Second
)

// WorkerReloadStatus classifies the outcome of one worker config reload. It is the
// transport-agnostic vocabulary the serve-side adapter translates the hub's error
// taxonomy into, so this package can pick a status code without importing wshub
// (the D2/G022 boundary — see workerReloader).
type WorkerReloadStatus string

const (
	// WorkerReloadApplied: the worker re-read its config and is now running it.
	WorkerReloadApplied WorkerReloadStatus = "applied"
	// WorkerReloadRejected: the worker answered but REFUSED the new config (bad
	// yaml, unknown agent, …) and still runs the previous one. Detail carries its
	// own words and must reach the user unchanged.
	WorkerReloadRejected WorkerReloadStatus = "rejected"
	// WorkerReloadOffline: the worker has no live connection (never connected, or
	// dropped while we waited for the receipt).
	WorkerReloadOffline WorkerReloadStatus = "offline"
	// WorkerReloadTooOld: the worker is connected and healthy but speaks a protocol
	// with no reload frame. It keeps working; it just has to be restarted to pick up
	// a new config.
	WorkerReloadTooOld WorkerReloadStatus = "too_old"
	// WorkerReloadTimedOut: no receipt within the deadline. The reload is NOT known
	// to have failed — the worker may still apply it — so it is reported as "no
	// receipt in time", never as "reload failed".
	WorkerReloadTimedOut WorkerReloadStatus = "timeout"
	// WorkerReloadFailed: anything else (write error, hub not wired, cancelled).
	WorkerReloadFailed WorkerReloadStatus = "failed"
)

// WorkerCaps is the worker's capability snapshot after a successful reload — the
// proof that the new config took effect. It mirrors the hub's caps frame but is
// redeclared here (like AgentBrief) so httpapi never imports wshub/wsproto; the
// serve-side adapter constructs it, hence the exported fields.
type WorkerCaps struct {
	Labels        []string     `json:"labels"`
	Projects      []string     `json:"projects"`
	Agents        []string     `json:"agents"`
	AgentCaps     []AgentBrief `json:"agent_caps,omitempty"`
	MaxConcurrent int          `json:"max_concurrent"`
}

// WorkerReloadOutcome is the adapter's structured answer to one reload request. It
// is deliberately (status, detail) rather than a Go error: classifying an error
// means matching on the types that define it, and those live in wshub — so the
// matching happens ONCE, in the serve-side adapter, and this package maps a status
// onto a status code. Detail is the human-readable reason (for
// WorkerReloadRejected: the worker's verbatim message).
type WorkerReloadOutcome struct {
	Status WorkerReloadStatus
	Detail string
	Caps   WorkerCaps
}

// workerReloader is the consumer-side narrow interface (D2) the reload handler
// drives the ws-worker hub through. serve injects an adapter over *wshub.Hub; a nil
// reloader means the hub is not wired (mcp / most tests) and the endpoint answers
// 503. The deadline is the CALLER's: this package owns the wait budget (it is the
// N in the 504 message), the adapter only passes ctx through.
type workerReloader interface {
	ReloadWorker(ctx context.Context, workerID, reason string) WorkerReloadOutcome
}

// SetWorkerReloader injects the worker-reload seam (serve). Mounting no route, it
// does not rebuild the router: the endpoint is always registered and answers 503
// while no reloader is wired.
func (s *Server) SetWorkerReloader(r workerReloader) { s.reloader = r }

// workerReloadReq is the (optional) request body. Both fields may be omitted — an
// empty body is a plain "reload now, no reason given".
type workerReloadReq struct {
	// Reason is free-form provenance forwarded to the worker and logged (who asked
	// and why). It never affects the outcome.
	Reason string `json:"reason,omitempty"`
	// TimeoutSec overrides the wait budget for this request (seconds).
	TimeoutSec int `json:"timeout_sec,omitempty"`
}

// wait returns the effective deadline: the request's override clamped into
// (0, maxWorkerReloadWait], else the default.
func (r workerReloadReq) wait() time.Duration {
	if r.TimeoutSec <= 0 {
		return defaultWorkerReloadWait
	}
	if w := time.Duration(r.TimeoutSec) * time.Second; w < maxWorkerReloadWait {
		return w
	}
	return maxWorkerReloadWait
}

// workerReloadResp is the body of EVERY outcome of this endpoint (2xx and 4xx/5xx
// alike), so a caller can read one shape: applied tells it what happened, error
// carries the failure text (for a rejected reload: the worker's own words,
// verbatim) and detail the surrounding context. It stays compatible with the
// uniform {error,detail} error shape the rest of the API uses.
type workerReloadResp struct {
	WorkerID string      `json:"worker_id"`
	Applied  bool        `json:"applied"`
	Caps     *WorkerCaps `json:"caps,omitempty"`
	Error    string      `json:"error,omitempty"`
	Detail   string      `json:"detail,omitempty"`
}

// handleWorkerReload asks one worker to re-read its local config and reports the
// OUTCOME, not an acknowledgement. It is synchronous on purpose: a config that the
// worker refuses (a syntax error in its yaml) must fail the very request that
// caused it — answering 202 and letting the failure land in a log is how a broken
// reload becomes an invisible one.
//
//	200 applied      {applied:true, caps:{…}}   caps = the worker's new capabilities,
//	                                            already visible on /v1/meta
//	409 rejected     {applied:false, error:"<the worker's own reason>"}
//	409 offline      not connected / dropped mid-wait
//	409 too_old      connected but cannot reload: upgrade and restart it
//	504 timeout      no receipt within the wait budget (the worker may still apply it)
//	404 unknown      no such worker in server.workers
func (s *Server) handleWorkerReload(c *rux.Context) {
	caller := callerFromCtx(c)
	if !s.callerMayAdmin(caller) {
		writeError(c, http.StatusForbidden, "admin not permitted for this caller", "caller lacks can_admin capability")
		return
	}
	workerID := c.Param("id")
	if workerID == "" {
		writeError(c, http.StatusBadRequest, "missing worker id", "path must be /v1/workers/{id}/reload")
		return
	}
	req, err := decodeWorkerReloadReq(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	// Unknown != offline: an id nobody ever configured is a caller mistake (404),
	// while a configured worker that is simply not connected is a state conflict
	// (409). Collapsing the two would let a typo look like a dead worker.
	if _, known := s.workerConfigs()[workerID]; !known {
		writeWorkerReload(c, http.StatusNotFound, workerReloadResp{
			WorkerID: workerID,
			Error:    "unknown worker",
			Detail:   "worker " + workerID + " is not registered in server.workers",
		})
		return
	}
	if s.reloader == nil {
		writeWorkerReload(c, http.StatusServiceUnavailable, workerReloadResp{
			WorkerID: workerID,
			Error:    "worker hub is not wired",
			Detail:   "this server cannot reach workers (no hub)",
		})
		return
	}

	wait := req.wait()
	// The deadline rides on the request context, so a client that hangs up also
	// releases the waiter (the hub drops its pending slot on ctx.Done).
	ctx, cancel := context.WithTimeout(c.Req.Context(), wait)
	defer cancel()
	out := s.reloader.ReloadWorker(ctx, workerID, req.Reason)

	switch out.Status {
	case WorkerReloadApplied:
		caps := out.Caps
		caps.Labels, caps.Projects, caps.Agents = nonNil(caps.Labels), nonNil(caps.Projects), nonNil(caps.Agents)
		writeWorkerReload(c, http.StatusOK, workerReloadResp{WorkerID: workerID, Applied: true, Caps: &caps})

	case WorkerReloadRejected:
		// The worker's message goes out UNCHANGED: it is the only thing that tells the
		// operator what to fix (which key, which line). Summarising it here ("reload
		// failed") is how a fixable config error becomes a mystery.
		msg := out.Detail
		if msg == "" {
			msg = "worker " + workerID + " rejected the config reload"
		}
		writeWorkerReload(c, http.StatusConflict, workerReloadResp{
			WorkerID: workerID,
			Error:    msg,
			Detail:   "worker " + workerID + " rejected the config reload and still runs its previous config",
		})

	case WorkerReloadOffline:
		writeWorkerReload(c, http.StatusConflict, workerReloadResp{
			WorkerID: workerID,
			Error:    "worker " + workerID + " is not connected",
			Detail:   out.Detail,
		})

	case WorkerReloadTooOld:
		writeWorkerReload(c, http.StatusConflict, workerReloadResp{
			WorkerID: workerID,
			Error:    "worker " + workerID + " is too old to reload its config: upgrade and restart it",
			Detail:   out.Detail,
		})

	case WorkerReloadTimedOut:
		writeWorkerReload(c, http.StatusGatewayTimeout, workerReloadResp{
			WorkerID: workerID,
			Error:    fmt.Sprintf("worker %s did not answer the reload request within %s", workerID, wait),
			Detail:   out.Detail,
		})

	default:
		writeWorkerReload(c, http.StatusInternalServerError, workerReloadResp{
			WorkerID: workerID,
			Error:    "reload failed",
			Detail:   out.Detail,
		})
	}
}

// decodeWorkerReloadReq reads the optional JSON body. No body at all is valid (the
// common `curl -X POST` case), so an empty payload decodes to the zero request
// rather than a 400.
func decodeWorkerReloadReq(c *rux.Context) (workerReloadReq, error) {
	var req workerReloadReq
	if c.Req == nil || c.Req.Body == nil || c.Req.ContentLength == 0 {
		return req, nil
	}
	if err := c.BindJSON(&req); err != nil {
		if errors.Is(err, io.EOF) { // empty body with an unknown length (chunked)
			return workerReloadReq{}, nil
		}
		return workerReloadReq{}, err
	}
	return req, nil
}

// writeWorkerReload encodes one reload outcome at the given status.
func writeWorkerReload(c *rux.Context, status int, body workerReloadResp) {
	c.JSON(status, body)
}

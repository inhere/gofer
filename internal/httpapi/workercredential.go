package httpapi

import (
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"
)

// Worker-token confinement (gofer-hxld).
//
// A worker authenticates with its OWN token (config.ServerConfig.Workers[<id>].token,
// kind = callerKindWorker) and that token used to be a full member of the /v1 surface:
// the auth middleware accepted it and every handler that did not explicitly look at
// the caller kind served it. In particular a worker token could POST /v1/jobs (submit
// work on any project it was allowed to run), and — because the administrative gate does not
// look at the caller kind and governance.require_admin_capability defaults to OFF —
// rewrite agent commands, projects, skills and register further workers.
//
// So the worker surface is a WHITELIST, in the same shape as SEC-01's job-credential
// table (jobcredential.go): a worker caller may only reach the routes a worker process
// itself needs, and everything else is a 403 before any handler runs.
//
// The list was derived from the route inventory's WorkerSelf rows and then checked
// against what the worker process actually issues (grep of internal/worker/ — the
// worker speaks HTTP to the hub only for these):
//
//	GET  /v1/workers/{id}/upgrade/file   downloads the staged upgrade binary the hub
//	                                     handed out in the `upgrade` frame
//	                                     (internal/worker/upgrade_run.go fetchFromHub)
//	PUT  /v1/xfer/{id}/content           uploads the result of a `get` transfer
//	                                     (internal/worker/xfer.go xferSend)
//	GET  /v1/xfer/{id}/content           fetches the source of a `put` transfer — the
//	                                     job-file seam of `gofer job run --upload`
//	                                     (internal/worker/xfer_job.go, xfer.go xferFetch)
//	GET  /v1/workers/{id}/assignable     read-only, self-scoped (a worker never calls
//	                                     it — `worker init` on the CLI does — kept
//	                                     because the handler refuses anyone but the
//	                                     worker itself)
//
// The three WebSocket connects a worker dials — /v1/workers/connect,
// /v1/workers/pty-connect and /v1/workers/tunnel-connect — are registered OUTSIDE the
// /v1 group (a WS handshake cannot use the JSON error envelope), so they never reach
// this middleware; they are listed here for completeness only.
//
// Everything else a worker could formerly reach is now closed. The handlers keep
// their own target-level rules on top (a worker may only fetch/upload a transfer
// assigned to itself, only download its own upgrade file): this gate is about the
// ROUTE, the handlers are about the OBJECT.
//
// Heartbeats, job state/log/result reports, cancels and reload/policy frames all
// travel the worker's WebSocket as frames — not HTTP — so nothing about the worker
// protocol depends on the routes removed here.

// workerSelfRoute is one whitelisted request: an HTTP method and a path template in
// which `*` stands for exactly one (non-empty) path segment.
type workerSelfRoute struct {
	method  string
	pattern string
}

// workerSelfRoutes is the WHOLE of what a worker token may call over plain HTTP.
// A route absent from this list is refused before its handler runs, so a newly
// registered endpoint is closed to workers until someone opens it here on purpose.
var workerSelfRoutes = []workerSelfRoute{
	{http.MethodGet, "/v1/workers/*/assignable"},
	{http.MethodGet, "/v1/workers/*/upgrade/file"},
	{http.MethodGet, "/v1/xfer/*/content"},
	{http.MethodPut, "/v1/xfer/*/content"},
}

// workerCredentialMiddleware is the worker gate. It runs AFTER authMiddleware (it
// needs the resolved caller kind) and BEFORE the job-credential gate (a worker token
// and a job credential are disjoint kinds, so the order between them is immaterial;
// the worker gate is the stricter one and goes first). Any caller that is not a
// worker is untouched: the user and job rules are exactly what they were.
func (s *Server) workerCredentialMiddleware(c *rux.Context) {
	if callerKindFromCtx(c) != callerKindWorker {
		c.Next()
		return
	}
	if workerRouteAllowed(c.Req.Method, c.Req.URL.Path) {
		c.Next()
		return
	}
	writeError(c, http.StatusForbidden,
		"worker credentials cannot call this endpoint",
		"a worker token is confined to the routes a worker process needs (its upgrade file, the transfer content assigned to it, its own assignable projects); "+c.Req.Method+" "+c.Req.URL.Path+" is not one of them — use an operator token")
	c.Abort()
}

// workerRouteAllowed reports whether a worker caller may make this request at all.
func workerRouteAllowed(method, path string) bool {
	for _, rt := range workerSelfRoutes {
		if rt.method == method && matchRouteTemplate(rt.pattern, path) {
			return true
		}
	}
	return false
}

// matchRouteTemplate matches a concrete request path against a `*`-per-segment
// template. The path is matched rather than rux's route template on purpose: this
// middleware runs BEFORE the handler chain, and rux publishes the matched route only
// around it (the metrics middleware reads it after c.Next() for the same reason).
func matchRouteTemplate(pattern, path string) bool {
	want := strings.Split(strings.Trim(pattern, "/"), "/")
	got := strings.Split(strings.Trim(path, "/"), "/")
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if want[i] == "*" {
			if got[i] == "" {
				return false
			}
			continue
		}
		if want[i] != got[i] {
			return false
		}
	}
	return true
}

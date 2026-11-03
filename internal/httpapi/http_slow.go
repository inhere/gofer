package httpapi

import (
	"log/slog"
	"time"

	"github.com/gookit/rux/v2"
)

// slowRequestThreshold is how long a request may take before the server logs it
// as server.http_slow. A constant (not a config key): this is an operator
// signal, not a behaviour anyone should tune per deployment. It is a var only so
// tests can lower it instead of sleeping for two seconds.
var slowRequestThreshold = 2 * time.Second

// neverSlowRoutes are the routes whose handler is SUPPOSED to stay open: the SSE
// streams, the websocket handshakes that then serve one live connection
// (worker hub, tunnels, attach, pty, preview, browser push), and the long-poll
// waits. Their duration says nothing about server health, and logging them
// would drown the signal — a single open tunnel holds a request for hours.
//
// Keyed by route TEMPLATE as rux reports it (routeTemplate: a param is `:id`, not
// `{id}`), so the comparison is exact and cannot be widened by a path prefix.
var neverSlowRoutes = map[string]struct{}{
	// SSE
	"/v1/jobs/:id/stream":     {},
	"/v1/jobs/:id/acp/stream": {},
	// websocket / data-plane upgrade
	"/v1/workers/connect":         {},
	"/v1/tunnels/connect":         {},
	"/v1/workers/tunnel-connect":  {},
	"/v1/workers/preview-connect": {},
	"/v1/workers/pty-connect":     {},
	"/v1/jobs/:id/attach":         {},
	"/v1/ws":                      {},
	// long-poll (hands the caller a JSON answer when there is one)
	"/v1/sessions/:sid/wait":      {},
	"/v1/sessions/:sid/turns/:id": {},
	"/v1/worker-enroll/:id":       {},
}

// ctxSlowExempt marks a request whose handler is EXPECTED to wait by the request's
// own parameters, so its duration says nothing about server health. A handler sets it
// before it starts waiting (the slow middleware reads it after the chain returns); the
// decision itself stays in the handler, next to the code that does the waiting.
const ctxSlowExempt = "slow_exempt"

// httpSlowMiddleware logs the requests that took too long, so a hung or
// throttling datastore shows up as a line naming the route instead of as a
// client-side timeout with no server trace.
//
// The route TEMPLATE is logged, never the URL: a query string carries caller
// data (targets, prompts, filters) and no log line needs it (SR1301).
func (s *Server) httpSlowMiddleware(c *rux.Context) {
	started := time.Now()
	c.Next()
	elapsed := time.Since(started)
	if elapsed < slowRequestThreshold {
		return
	}
	route := routeTemplate(c)
	if _, skip := neverSlowRoutes[route]; skip {
		return
	}
	// A handler that waited ON PURPOSE (a synchronous job submit blocks until the job
	// ends) marks the request: its duration is the job's, not the server's.
	if exempt, _ := c.Get(ctxSlowExempt); exempt != nil {
		return
	}
	attrs := []any{"event", "server.http_slow", "method", c.Req.Method, "route", route,
		"status", c.StatusCode(), "elapsed_ms", elapsed.Milliseconds()}
	if caller := callerFromCtx(c); caller != "" {
		attrs = append(attrs, "caller", caller)
	}
	slog.Warn("server.http_slow", attrs...)
}

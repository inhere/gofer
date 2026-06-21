package httpapi

import (
	"math"
	"net/http"

	"github.com/gookit/rux/v2"
	"golang.org/x/time/rate"
)

// rateLimitMiddleware is the E17 per-caller submit-rate limiter (design §7.3). It
// is mounted on the /v1 group AFTER authMiddleware (so callerFromCtx is set) and
// AFTER metricsMiddleware (so a 429 is still counted in gofer_http_requests_total).
//
// Scope is deliberately narrow (design §7.3 / D7): only the write-submit
// endpoints (POST /v1/jobs, POST /v1/workflows) are gated; every read (list /
// get / SSE / events) and every sub-action (cancel / answer / interactions) is
// untouched, so observation and lifecycle control are never throttled.
//
// The rate config真源 is the job service's CURRENT config (CallerRate reads the
// atomic.Pointer that SIGHUP-reload swaps), NOT a copy held on the Server — so a
// hot-reloaded rate_limit / rate_burst takes effect on the very next request. A
// caller with no rate gating (rps <= 0) passes straight through.
func (s *Server) rateLimitMiddleware(c *rux.Context) {
	if !isSubmitPath(c) {
		c.Next()
		return
	}
	caller := callerFromCtx(c)
	rps, burst := s.jobs.CallerRate(caller)
	if rps <= 0 {
		c.Next() // no rate gating configured for this caller.
		return
	}
	lim := s.limiterFor(caller, rps, burst)
	if !lim.Allow() {
		// Retry-After is advisory (SR901 family): a 1s hint matches the per-second
		// token-bucket refill granularity. The body uses the gofer {error,detail}
		// envelope (not the company envelope), consistent with respond.go.
		c.Resp.Header().Set("Retry-After", "1")
		writeRateLimited(c, caller)
		c.Abort()
		return
	}
	c.Next()
}

// isSubmitPath reports whether the request is a write-submit endpoint subject to
// the E17 rate limit (design §7.3). It matches EXACTLY POST /v1/jobs and POST
// /v1/workflows — not /v1/jobs/{id}/cancel, /answer, interactions, nor any GET —
// using a precise path compare so a sub-resource can never be folded into the
// submit limit.
func isSubmitPath(c *rux.Context) bool {
	if c.Req.Method != http.MethodPost {
		return false
	}
	p := c.Req.URL.Path
	return p == "/v1/jobs" || p == "/v1/workflows"
}

// limiterFor returns the per-caller token bucket, creating it on first use and
// re-syncing its Limit/Burst to the latest config on every call (E17 hot-reload,
// design §7.4). SetLimit/SetBurst mutate the EXISTING limiter in place, so a
// rate change after SIGHUP is applied without dropping the caller's accumulated
// tokens (no reset / no state loss). Guarded by limMu (independent of the job
// service's s.mu — see Server.limiters).
func (s *Server) limiterFor(caller string, rps float64, burst int) *rate.Limiter {
	if burst <= 0 {
		burst = int(math.Ceil(rps))
		if burst < 1 {
			burst = 1
		}
	}
	s.limMu.Lock()
	defer s.limMu.Unlock()
	lim, ok := s.limiters[caller]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(rps), burst)
		s.limiters[caller] = lim
		return lim
	}
	// Hot-reload: a changed rate/burst is applied to the live limiter in place.
	if float64(lim.Limit()) != rps {
		lim.SetLimit(rate.Limit(rps))
	}
	if lim.Burst() != burst {
		lim.SetBurst(burst)
	}
	return lim
}

package httpapi

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/rux/v2"
)

// handleMetrics serves the Prometheus exposition (E16, design §6.2). It is
// registered OUTSIDE the /v1 auth group (sibling of /health): scrapers rarely
// carry a Bearer token, so the intranet admission boundary is the primary guard
// (SR202). When metrics.token is configured it re-adds a constant-time Bearer
// check for environments that want authenticated scraping.
func (s *Server) handleMetrics(c *rux.Context) {
	if s.metricsToken != "" {
		got, ok := bearerToken(c.Req.Header.Get("Authorization"))
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.metricsToken)) != 1 {
			writeError(c, http.StatusUnauthorized, "unauthorized", "invalid metrics token")
			c.Abort()
			return
		}
	}
	s.metrics.Handler().ServeHTTP(c.Resp, c.Req)
}

// metricsMiddleware records per-request HTTP metrics for the /v1 group (E16). It
// is a no-op when metrics is not wired. It runs the rest of the chain, then
// records {method, route-template, status} + duration. The route label is the
// MATCHED route template (bounded cardinality, design §6.5), never the raw path.
func (s *Server) metricsMiddleware(c *rux.Context) {
	if s.metrics == nil {
		c.Next()
		return
	}
	start := time.Now()
	c.Next()
	route := routeTemplate(c)
	status := strconv.Itoa(c.StatusCode())
	s.metrics.ObserveHTTP(c.Req.Method, route, status, time.Since(start).Seconds())
}

// routeTemplate returns the bounded route template for the current request (E16
// cardinality guard, design §6.5). It prefers the rux MATCHED route's template
// (e.g. /v1/jobs/:id — already collapsed to a finite placeholder set at route
// registration), and only falls back to normalizeRoute(path) when no route
// matched (404 / SPA fallback), so an id segment can never become a label value.
func routeTemplate(c *rux.Context) string {
	if rt := c.Route(); rt != nil {
		if p := rt.Path(); p != "" {
			return p
		}
	}
	return normalizeRoute(c.Req.URL.Path)
}

// normalizeRoute collapses likely id segments (numeric / uuid / long hex / job
// ids like 20060102-150405-1a2b3c4d) of a raw path to "{id}", so an unmatched
// request can never explode the `route` label cardinality. It is only the
// fallback path — matched requests use the rux route template directly.
func normalizeRoute(p string) string {
	if p == "" {
		return "/"
	}
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		if isIDSegment(seg) {
			parts[i] = "{id}"
		}
	}
	return strings.Join(parts, "/")
}

// isIDSegment reports whether a path segment looks like a volatile id (so it must
// be folded to "{id}" rather than kept as a label value). It treats as ids: all
// digits; long (>=12) hex-ish strings; and gofer job ids (a segment containing a
// digit AND a '-', e.g. the time-prefixed 20060102-150405-1a2b3c4d).
func isIDSegment(seg string) bool {
	if seg == "" {
		return false
	}
	if isAllDigits(seg) {
		return true
	}
	if len(seg) >= 12 && isHexish(seg) {
		return true
	}
	// gofer job id shape: has a hyphen and at least one digit (date-time-rand).
	if strings.ContainsRune(seg, '-') && strings.IndexFunc(seg, isDigitRune) >= 0 {
		return true
	}
	return false
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isHexish reports whether s is composed only of hex digits and hyphens (covers
// uuids and the random hex suffixes used in job ids).
func isHexish(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		case r == '-':
		default:
			return false
		}
	}
	return true
}

func isDigitRune(r rune) bool { return r >= '0' && r <= '9' }

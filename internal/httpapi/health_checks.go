package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/serve/healthcheck"
)

// healthChecker is the slice of the server self-check runner (internal/serve/healthcheck)
// the HTTP surface reads. It is an interface so httpapi stays testable without a real
// runner, and nil when serve wired none (tests, mcp) — every reader below treats nil as
// "no checks, anything is fine".
type healthChecker interface {
	Snapshot() []healthcheck.Result
	Failing() []string
	Degraded() bool
}

// SetHealthChecks wires the server self-check runner whose results /v1/health/checks,
// /health (degraded bit) and /v1/stats (health_failing) publish.
func (s *Server) SetHealthChecks(h healthChecker) { s.health = h }

// handleHealthChecks publishes every self-check item (design 2026-11-14 §5, plan R11):
// {name, ok, consecutive_failures, failing, last_error, checked_at}. A caller that is
// merely logged in may read it — the items name no project and carry no secret.
func (s *Server) handleHealthChecks(c *rux.Context) {
	checks := []healthcheck.Result{}
	if s.health != nil {
		checks = s.health.Snapshot()
	}
	c.JSON(http.StatusOK, rux.M{"checks": checks})
}

// healthDegraded is the single bit /health publishes: an item is failing, no detail.
func (s *Server) healthDegraded() bool {
	return s.health != nil && s.health.Degraded()
}

// healthFailing lists the failing check names for /v1/stats and `serve status`.
func (s *Server) healthFailing() []string {
	if s.health == nil {
		return nil
	}
	return s.health.Failing()
}

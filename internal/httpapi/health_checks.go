package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/inbox"
	"github.com/inhere/gofer/internal/serve/healthcheck"
	"github.com/inhere/gofer/internal/today"
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

// healthCards builds the 「等我」 cards of the self-check items that reached their
// consecutive-failure threshold. The list is read from the runner's LATEST snapshot on every
// request, so a fault appears as soon as it is reported and disappears on the item's first
// successful run — nothing is stored, so nothing can go stale. The items name no project, so
// the caller's project scope does not filter them.
func (s *Server) healthCards(lang i18n.Lang) []today.Card {
	if s.health == nil {
		return nil
	}
	var items []inbox.HealthItem
	for _, res := range s.health.Snapshot() {
		if !res.Failing {
			continue
		}
		items = append(items, inbox.HealthItem{Name: res.Name, Failures: res.ConsecutiveFailures, LastError: res.LastError, At: res.CheckedAt})
	}
	return inbox.HealthCards(lang, items)
}

// healthFailing lists the failing check names for /v1/stats and `serve status`.
func (s *Server) healthFailing() []string {
	if s.health == nil {
		return nil
	}
	return s.health.Failing()
}

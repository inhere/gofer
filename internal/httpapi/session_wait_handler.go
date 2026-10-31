package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/sessionrelay"
)

// handleWaitSession long-polls a session's state
// (GET /v1/sessions/{sid}/wait?until=idle|waiting_reply|ended&timeout=30).
//
// `until` may be a comma-separated list. `timeout` is the request's own budget in
// seconds (0 = look once and answer) and is capped at
// sessionrelay.MaxWaitTimeout, so a CLI loops over this endpoint until its total
// --timeout runs out instead of holding one request open for minutes.
//
// The answer carries the session view and `reached`: false means the budget ran
// out with the state still not the wanted one, which the CLI turns into exit
// code 2 — a wait that timed out is not an error, and it is not success either.
func (s *Server) handleWaitSession(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	if !s.mayReadSession(c, s.sessionOwnerOrUnknown(c)) {
		s.denySessionAction(c, authz.PermSessionReadOthers)
		return
	}
	until, err := sessionrelay.ParseWaitStates(c.Query("until"))
	if err != nil {
		writeError(c, http.StatusBadRequest, "wait session failed", err.Error())
		return
	}
	timeout := parseWaitTimeout(c.Query("timeout"))
	start := time.Now()
	a, reached, err := s.relay.WaitForState(c.Req.Context(), c.Param("sid"), until, timeout, 0)
	if err != nil {
		status := relayStatus(err)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// The client hung up / its own budget passed: nothing to report.
			status = http.StatusRequestTimeout
		}
		writeError(c, status, "wait session failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"session":    s.toSessionView(s.requestLang(c), a),
		"state":      a.State,
		"until":      until,
		"reached":    reached,
		"waited_sec": int(time.Since(start).Seconds()),
	})
}

// parseWaitTimeout reads the `timeout` query (seconds): absent or 0 means "look
// once, answer immediately"; anything above the cap is clamped, so a client
// cannot pin a request (and its goroutine) for longer than the cap.
func parseWaitTimeout(raw string) time.Duration {
	sec, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || sec <= 0 {
		return 0
	}
	d := time.Duration(sec) * time.Second
	if d > sessionrelay.MaxWaitTimeout {
		return sessionrelay.MaxWaitTimeout
	}
	return d
}

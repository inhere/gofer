package httpapi

import (
	"errors"
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/today"
)

// N3 T3 「稍后」 (design §2.3): bind / validate and forward to internal/today. Snooze and
// put-back are a person's (like the action audit); the list is every person's read.

func (s *Server) todayPersonReady(c *rux.Context, what string) bool {
	if !s.todayReady(c) {
		return false
	}
	if callerKindFromCtx(c) != callerKindUser {
		writeError(c, http.StatusForbidden, what+" requires a user caller", "only a person snoozes home-page cards")
		return false
	}
	return true
}

// POST /v1/today/snooze {card_key, until_at? | until_job_id?}
func (s *Server) handleTodaySnooze(c *rux.Context) {
	if !s.todayPersonReady(c, "today snooze") {
		return
	}
	var body today.SnoozeInput
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	out, err := s.today.Snooze(body, workBy(c))
	if err != nil {
		writeTodaySnoozeError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// DELETE /v1/today/snooze/{key} — the card key is path-escaped (it may hold '/').
func (s *Server) handleTodayUnsnooze(c *rux.Context) {
	if !s.todayPersonReady(c, "today unsnooze") {
		return
	}
	key := c.Param("key")
	ok, err := s.today.Unsnooze(key)
	if err != nil {
		writeTodaySnoozeError(c, err)
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "card not snoozed", key)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"card_key": key, "unsnoozed": true})
}

// GET /v1/today/snoozed?include_exec=1
func (s *Server) handleTodaySnoozed(c *rux.Context) {
	if !s.todayReady(c) {
		return
	}
	rows, err := s.today.Snoozed(queryBool(c, "include_exec"))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list snoozed failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"snoozed": rows})
}

func writeTodaySnoozeError(c *rux.Context, err error) {
	switch {
	case errors.Is(err, today.ErrInvalidSnooze):
		writeError(c, http.StatusBadRequest, "invalid snooze", err.Error())
	case errors.Is(err, today.ErrCardNotQueued):
		writeError(c, http.StatusNotFound, "card not in the queue", err.Error())
	default:
		writeError(c, http.StatusInternalServerError, "today snooze failed", err.Error())
	}
}

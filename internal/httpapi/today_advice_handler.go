package httpapi

import (
	"errors"
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/today"
)

// N3 T4 管家建议: POST /v1/today/advice {card_key, text, action_id?, digest?}. Only a person
// or the steward's credential may write advice (SEC-01 default-denies it for member /
// leader job credentials before the handler runs; the check below repeats that rule so the
// handler is closed on its own too). Advice never executes anything.
func (s *Server) handleTodayAdvice(c *rux.Context) {
	if !s.todayReady(c) {
		return
	}
	jobID := ""
	if jc, ok := jobCallerFromCtx(c); ok {
		if !jc.isSteward() {
			writeError(c, http.StatusForbidden, "job credential may not write home-page advice",
				"only the steward (or a person) advises on 「今天」 cards")
			return
		}
		jobID = jc.JobID
	} else if callerKindFromCtx(c) != callerKindUser {
		writeError(c, http.StatusForbidden, "today advice requires a person or the steward", "")
		return
	}
	var body today.AdviceInput
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	adv, err := s.today.Advise(body, workBy(c), jobID)
	switch {
	case errors.Is(err, today.ErrInvalidAdvice):
		writeError(c, http.StatusBadRequest, "invalid advice", err.Error())
	case errors.Is(err, today.ErrUnknownCard):
		writeError(c, http.StatusNotFound, "unknown card", err.Error())
	case err != nil:
		writeError(c, http.StatusInternalServerError, "record advice failed", err.Error())
	default:
		c.JSON(http.StatusOK, map[string]any{"card_key": body.CardKey, "advice": adv})
	}
}

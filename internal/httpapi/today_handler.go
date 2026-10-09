package httpapi

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/today"
)

// N3 「今天」 (design docs/design/2026-10-09-n3-today-decision-home-design.md §2): the
// handlers only bind / validate and forward; the aggregation lives in internal/today.
// Reads follow the work-item rule (every person / job caller, never a worker transport
// token); the action audit is a person's (SEC-01 default-denies it for job credentials).

func (s *Server) todayReady(c *rux.Context) bool {
	if s.today == nil {
		writeError(c, http.StatusServiceUnavailable, "today unavailable", "no job store wired on this server")
		return false
	}
	return workNotAWorker(c)
}

// GET /v1/today?since=<unix>&include_exec=1
func (s *Server) handleToday(c *rux.Context) {
	if !s.todayReady(c) {
		return
	}
	var since int64
	if raw := strings.TrimSpace(c.Query("since")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeError(c, http.StatusBadRequest, "invalid since", "since must be a non-negative unix timestamp")
			return
		}
		since = parsed
	}
	resp, err := s.today.Today(today.Query{Since: since, IncludeExec: queryBool(c, "include_exec")})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "build today failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, resp)
}

// POST /v1/today/actions {card_key, action_id, advice_action_id?, title?, label?, kind?}
func (s *Server) handleTodayAction(c *rux.Context) {
	if !s.todayReady(c) {
		return
	}
	if callerKindFromCtx(c) != callerKindUser {
		writeError(c, http.StatusForbidden, "today action requires a user caller", "only a person records home-page actions")
		return
	}
	var body today.ActionInput
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	h, err := s.today.RecordAction(body, workBy(c))
	if err != nil {
		if errors.Is(err, today.ErrInvalidAction) {
			writeError(c, http.StatusBadRequest, "invalid today action", err.Error())
			return
		}
		writeError(c, http.StatusInternalServerError, "record today action failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, h)
}

// GET /v1/today/handled?days=7
func (s *Server) handleTodayHandled(c *rux.Context) {
	if !s.todayReady(c) {
		return
	}
	days := 7
	if raw := strings.TrimSpace(c.Query("days")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > today.MaxHandledDays {
			writeError(c, http.StatusBadRequest, "invalid days", "days must be 1-"+strconv.Itoa(today.MaxHandledDays))
			return
		}
		days = n
	}
	rows, err := s.today.HandledSince(days)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list handled failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"handled": rows, "days": days})
}

// todayRunners is the status bar's runner count: the built-in local runner (always
// up), the registered workers (connected or not) and the peer runners (probe up).
func (s *Server) todayRunners() today.RunnerStatus {
	out := today.RunnerStatus{Online: 1, Total: 1}
	for id := range s.workerConfigs() {
		out.Total++
		if s.workers != nil {
			if ws, ok := s.workers.WorkerStatus(id); ok && ws.Connected {
				out.Online++
				continue
			}
		}
		out.Offline = append(out.Offline, id)
	}
	probes := s.probeIndex()
	for name, rc := range s.runners {
		if rc.Type != runnerTypePeerHTTP {
			continue
		}
		out.Total++
		if pr, ok := probes[name]; ok && pr.Up {
			out.Online++
		} else {
			out.Offline = append(out.Offline, name)
		}
	}
	sort.Strings(out.Offline)
	return out
}

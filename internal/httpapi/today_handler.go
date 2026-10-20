package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
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
	respHolder := today.Query{Since: since, IncludeExec: queryBool(c, "include_exec")}
	if s.ceilingActive(c) {
		// gofer-7798 第二期: a caller whose ceiling bounds projects reads only the cards of its own —
		// a card that names no project counts as outside (ceilingAllowsProject). The unrestricted
		// caller passes no predicate at all, so its queue stays exactly what it was.
		respHolder.ProjectAllowed = s.scopeFilter(c)
	}
	resp, err := s.today.Today(respHolder)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "build today failed", err.Error())
		return
	}
	if !s.ceilingActive(c) {
		// The enrollment card is a server-global summary (names no project), so a restricted caller
		// does not get it (计划决策 2).
		s.appendWorkerEnrollCard(c, &resp)
	}
	s.scopeTodayStatus(c, &resp)
	c.JSON(http.StatusOK, resp)
}

// scopeTodayStatus narrows today's status bar for a caller under a ceiling (gofer-7798 第二期,
// 计划决策 2): the runner / worker rows keep only what the ceiling's workers / runners dimensions
// admit, the jobs-running-now count is recomputed over the caller's projects, and the
// server-global numbers (today's usage, the steward's day) are not returned at all. A caller
// without a ceiling leaves the response untouched.
func (s *Server) scopeTodayStatus(c *rux.Context, resp *today.Response) {
	ceil := s.ceilingOf(c)
	if ceil == nil {
		return
	}
	resp.Status.UsageToday = nil
	resp.Status.StewardToday = nil
	resp.Status.Runners.RunnerStatus = s.todayRunnersFor(ceil)
	resp.Status.Alerts = nil
	for _, name := range resp.Status.Runners.Offline {
		resp.Status.Alerts = append([]string{"runner " + name + " 离线"}, resp.Status.Alerts...)
	}
	if len(ceil.Projects) == 0 || s.jobs == nil {
		return
	}
	byStatus, err := s.jobs.Meta().CountJobsByStatusProjects(s.scopedProjectKeys(s.scopeFilter(c)))
	if err != nil {
		return // a failed count must not break the home page
	}
	resp.Status.Runners.RunningJobs = byStatus[job.StatusRunning]
}

// todayRunnersFor is todayRunners for a caller under a ceiling: the built-in local runner only
// when the ceiling's runners dimension admits it, the registered workers it admits, and the peer
// runners it admits.
func (s *Server) todayRunnersFor(ceil *config.CallerCeiling) today.RunnerStatus {
	out := today.RunnerStatus{}
	if ceil.AllowsRunner(config.BuiltinLocalRunnerAlias) {
		out.Online, out.Total = 1, 1
	}
	for id := range s.workerConfigs() {
		if !ceil.AllowsWorker(id) {
			continue
		}
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
		if rc.Type != runnerTypePeerHTTP || !ceil.AllowsRunner(name) {
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

// appendWorkerEnrollCard adds the pending worker enrollment summary card for a person
// holding worker.manage. It runs after the Service result and outside its cache / snooze /
// advice paths (nothing is written), and carries display fields only.
func (s *Server) appendWorkerEnrollCard(c *rux.Context, resp *today.Response) {
	if s.enroll == nil || callerKindFromCtx(c) != callerKindUser || !s.can(c, authz.PermWorkerManage) {
		return
	}
	if !s.enroll.Settings().Enabled {
		return
	}
	rows, err := s.enroll.ListAdmin(enrollRecent)
	if err != nil {
		return // a failed side lookup must not break the home page
	}
	now := time.Now().Unix()
	var pending []today.EnrollPending
	for _, e := range rows {
		if e.State != jobstore.EnrollPending || now >= e.DecideUntil {
			continue
		}
		var h enrollHost
		_ = json.Unmarshal([]byte(e.HostJSON), &h)
		pending = append(pending, today.EnrollPending{
			Hostname: stripControl(h.Hostname), RequestedID: stripControl(e.RequestedID),
			CreatedAt: e.CreatedAt, DecideUntil: e.DecideUntil,
		})
	}
	card, ok := today.WorkerEnrollCard(pending)
	if !ok {
		return
	}
	// Copy: the Service's slice may be shared with its cache.
	resp.Decisions = append(append(make([]today.Card, 0, len(resp.Decisions)+1), resp.Decisions...), card)
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
	var rows []today.Handled
	var err error
	if s.ceilingActive(c) {
		// gofer-7798 第二期: the handled rows are recorded with the project the card belonged to, so
		// a restricted caller sees only its own.
		rows, err = s.today.HandledSinceScoped(days, s.scopeFilter(c))
	} else {
		rows, err = s.today.HandledSince(days)
	}
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

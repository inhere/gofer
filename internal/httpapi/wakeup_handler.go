package httpapi

import (
	"errors"
	"io"
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// wakeupGate is the entry layer's half of the wakeup permission rule (design 架构: 「job 层
// wakeup.go 不再读配置，由入口层传入判定」）：a caller that is not the wakeup's own caller
// needs job.wake.others. The job layer applies it only after its own ownership comparison
// (a caller's own job, or a job credential waking itself, needs no permission).
func (s *Server) wakeupGate(c *rux.Context) job.WakeupGate {
	return func(job.JobResult) bool { return s.can(c, authz.PermJobWakeOthers) }
}

// mayWakeByID applies the wakeup permission rule to the job a wakeup belongs to (only the
// wakeup row knows it). An unknown wakeup is left to the handler that loads it, so the 404
// keeps its own wording.
func (s *Server) mayWakeByID(c *rux.Context, wakeupID string, submit bool) bool {
	w, ok, err := s.jobs.GetWakeup(wakeupID)
	if err != nil || !ok {
		return true
	}
	res, ok := s.jobForAccess(w.JobID)
	if !ok || s.mayWakeJob(c, res, submit) {
		return true
	}
	if submit {
		s.denyJobAction(c, authz.PermJobWakeOthers, authz.PermJobSubmit)
		return false
	}
	s.denyJobAction(c, authz.PermJobWakeOthers)
	return false
}

// wakeupView is the JSON projection of one JOB-09 wakeup (design §五.1). Every
// timestamp is unix SECONDS; the JSON filter columns are decoded back into arrays so
// a client never has to parse the stored text.
//
// continuation_job_id is cleared while a fire is in flight (the store holds the
// "pending" sentinel there): that is an internal claim, not a job the caller could
// look up.
type wakeupView struct {
	ID                string   `json:"id"`
	JobID             string   `json:"job_id"`
	Kind              string   `json:"kind"`
	At                int64    `json:"at,omitempty"`
	EverySec          int64    `json:"every_sec,omitempty"`
	Cron              string   `json:"cron,omitempty"`
	Timezone          string   `json:"timezone,omitempty"`
	EventTypes        []string `json:"event_types,omitempty"`
	FilterJobID       string   `json:"filter_job_id,omitempty"`
	FilterStatus      []string `json:"filter_status,omitempty"`
	Mode              string   `json:"mode"`
	Instruction       string   `json:"instruction,omitempty"`
	Enabled           bool     `json:"enabled"`
	Revision          int64    `json:"revision"`
	NextRunAt         int64    `json:"next_run_at,omitempty"`
	LastFiredAt       int64    `json:"last_fired_at,omitempty"`
	FiredCount        int64    `json:"fired_count"`
	CoalescedCount    int64    `json:"coalesced_count"`
	ContinuationJobID string   `json:"continuation_job_id,omitempty"`
	CreatedBy         string   `json:"created_by,omitempty"`
	CreatedAt         int64    `json:"created_at"`
	ExpiresAt         int64    `json:"expires_at,omitempty"`
}

// wakeupsResp is the list envelope (`{wakeups: [...]}`), matching the other list
// endpoints' shape.
type wakeupsResp struct {
	Wakeups []wakeupView `json:"wakeups"`
}

// wakeupEnabledReq is the PATCH body: only the switch is mutable through the API
// (the timer spec is fixed at registration; re-arm happens when it is turned on).
type wakeupEnabledReq struct {
	Enabled bool `json:"enabled"`
}

func toWakeupView(w jobstore.WakeupRecord) wakeupView {
	cont := w.ContinuationJobID
	if w.IsContinuationPending() {
		cont = "" // an in-flight claim, not a job id
	}
	return wakeupView{
		ID:                w.ID,
		JobID:             w.JobID,
		Kind:              w.Kind,
		At:                w.At,
		EverySec:          w.EverySec,
		Cron:              w.CronExpr,
		Timezone:          w.Timezone,
		EventTypes:        jobstore.DecodeStringList(w.EventTypesJSON),
		FilterJobID:       w.FilterJobID,
		FilterStatus:      jobstore.DecodeStringList(w.FilterStatusJSON),
		Mode:              w.Mode,
		Instruction:       w.Instruction,
		Enabled:           w.Enabled == 1,
		Revision:          w.Revision,
		NextRunAt:         w.NextRunAt,
		LastFiredAt:       w.LastFiredAt,
		FiredCount:        w.FiredCount,
		CoalescedCount:    w.CoalescedCount,
		ContinuationJobID: cont,
		CreatedBy:         w.CreatedBy,
		CreatedAt:         w.CreatedAt,
		ExpiresAt:         w.ExpiresAt,
	}
}

// wakeupStatus maps a wakeup error to a status (mirrors resumeStatus): an unknown
// job/wakeup is 404; a caller that may not act on the job is 403; everything
// well-formed-but-refused is 400 via submitStatus.
func wakeupStatus(err error) int {
	switch {
	case errors.Is(err, job.ErrUnknownJob):
		return http.StatusNotFound
	case errors.Is(err, job.ErrWakeupForbidden):
		return http.StatusForbidden
	default:
		return submitStatus(err)
	}
}

// handleCreateWakeup serves POST /v1/jobs/{id}/wakeups (JOB-09): register an event
// subscription or timer on the job. The orchestration (vocabulary, validation, the
// resume-permission check) lives in job.Service.CreateWakeup (G021) — the handler
// binds the body, stamps the authenticated caller and maps the sentinels.
//
// It is a USER-caller surface: a worker token is an executing machine, and a wakeup
// starts new work as a named caller, so it must not be able to register one.
func (s *Server) handleCreateWakeup(c *rux.Context) {
	if callerKindFromCtx(c) == callerKindWorker {
		writeError(c, http.StatusForbidden, "wakeup not permitted for this caller",
			"worker tokens cannot register a wakeup: only a user caller starts new work")
		return
	}
	var spec job.WakeupSpec
	if err := c.BindJSON(&spec); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	spec.JobID = c.Param("id")
	// SEC-01: a job credential may register a wakeup on ITSELF and nothing else — the
	// "continue me when this fires" case an agent inside a job asks for.
	if !s.jobMayWakeJob(c, spec.JobID) {
		return
	}
	// 权限（设计 §1：wakeups 创建与修改是 job.submit 入口；wakeup 的归属走 job.wake.others）。
	// 两个权限都在这里判定：job 层接着会用 wakeupGate 再问一次 job.wake.others（设计 架构：
	// 「wakeup.go 不再读配置，由入口层传入判定」），这里先给出 403 文案。
	if res, ok := s.jobForAccess(spec.JobID); ok && !s.mayWakeJob(c, res, true) {
		s.denyJobAction(c, authz.PermJobWakeOthers, authz.PermJobSubmit)
		return
	}
	w, err := s.jobs.CreateWakeup(spec, callerFromCtx(c), s.wakeupGate(c))
	if err != nil {
		writeError(c, wakeupStatus(err), "wakeup rejected", err.Error())
		return
	}
	c.JSON(http.StatusOK, toWakeupView(w))
}

// handleListWakeups serves GET /v1/jobs/{id}/wakeups: the job's wakeups, oldest
// first. An unknown job is a 404 (an empty list for a typo would read as "no
// wakeups registered").
func (s *Server) handleListWakeups(c *rux.Context) {
	id := c.Param("id")
	res, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "job "+id+" does not exist")
		return
	}
	if !s.mayReadJob(c, res) {
		s.denyJobAction(c, authz.PermJobReadOthers)
		return
	}
	rows, err := s.jobs.ListWakeups(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list wakeups failed", err.Error())
		return
	}
	out := wakeupsResp{Wakeups: make([]wakeupView, 0, len(rows))}
	for _, w := range rows {
		out.Wakeups = append(out.Wakeups, toWakeupView(w))
	}
	c.JSON(http.StatusOK, out)
}

// handleGetWakeup serves GET /v1/wakeups/{wid}: one wakeup by id.
func (s *Server) handleGetWakeup(c *rux.Context) {
	w, ok, err := s.jobs.GetWakeup(c.Param("wid"))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get wakeup failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown wakeup", "wakeup "+c.Param("wid")+" does not exist")
		return
	}
	// 声明 InHandler(job.read.others, deny)：读的是这条 wakeup 所属的 job。
	if res, ok := s.jobForAccess(w.JobID); ok && !s.mayReadJob(c, res) {
		s.denyJobAction(c, authz.PermJobReadOthers)
		return
	}
	c.JSON(http.StatusOK, toWakeupView(w))
}

// handleUpdateWakeup serves PATCH /v1/wakeups/{wid}: enable or disable it (the
// "开关" of the web block). Enabling a timer re-arms it from now, so a wakeup that
// was off never replays the ticks it missed.
func (s *Server) handleUpdateWakeup(c *rux.Context) {
	if callerKindFromCtx(c) == callerKindWorker {
		writeError(c, http.StatusForbidden, "wakeup not permitted for this caller",
			"worker tokens cannot change a wakeup")
		return
	}
	var req wakeupEnabledReq
	if err := c.BindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	// 声明 InHandler(job.wake.others, job.submit)：重新布防的 wakeup 会在到点时起 job
	// （设计 §1 的 job.submit 入口清单：「wakeups 创建与修改」）。
	if !s.mayWakeByID(c, c.Param("wid"), true) {
		return
	}
	w, err := s.jobs.SetWakeupEnabled(c.Param("wid"), callerFromCtx(c), req.Enabled, s.wakeupGate(c))
	if err != nil {
		writeError(c, wakeupStatus(err), "wakeup update rejected", err.Error())
		return
	}
	c.JSON(http.StatusOK, toWakeupView(w))
}

// handleDeleteWakeup serves DELETE /v1/wakeups/{wid}.
func (s *Server) handleDeleteWakeup(c *rux.Context) {
	if callerKindFromCtx(c) == callerKindWorker {
		writeError(c, http.StatusForbidden, "wakeup not permitted for this caller",
			"worker tokens cannot remove a wakeup")
		return
	}
	if !s.mayWakeByID(c, c.Param("wid"), false) {
		return
	}
	if err := s.jobs.DeleteWakeup(c.Param("wid"), callerFromCtx(c), s.wakeupGate(c)); err != nil {
		writeError(c, wakeupStatus(err), "wakeup delete rejected", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "deleted"})
}

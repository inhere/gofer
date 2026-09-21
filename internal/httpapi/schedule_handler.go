package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// createScheduleReq is the POST /v1/schedules body. enabled/catch_up default to
// true when omitted; the persisted shape remains the jobstore 1/0 integer form.
type createScheduleReq struct {
	Name     string         `json:"name"`
	Type     string         `json:"type"`
	Cron     string         `json:"cron"`
	DelaySec int64          `json:"delay_sec,omitempty"`
	RunAt    int64          `json:"run_at,omitempty"`
	Request  job.JobRequest `json:"request"`
	Enabled  *bool          `json:"enabled,omitempty"`
	CatchUp  *bool          `json:"catch_up,omitempty"`
	// Webhook enables the schedule's own external trigger endpoint (AUTO-02b): the
	// server mints a trigger_token, and POST /v1/schedules/{id}/trigger runs the
	// schedule for anyone presenting it.
	Webhook bool `json:"webhook,omitempty"`
}

type scheduleView struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Cron       string         `json:"cron"`
	Enabled    int            `json:"enabled"`
	CatchUp    int            `json:"catch_up"`
	NextRunAt  int64          `json:"next_run_at"`
	LastRunAt  int64          `json:"last_run_at"`
	LastJobID  string         `json:"last_job_id"`
	ProjectKey string         `json:"project_key"`
	Request    job.JobRequest `json:"request"`
	// TriggerToken is the schedule's webhook secret (AUTO-02b); empty = the trigger
	// endpoint is not enabled for it. It is shown to authenticated callers only (every
	// /v1 schedule read is authenticated).
	TriggerToken string `json:"trigger_token,omitempty"`
}

func (s *Server) handleCreateSchedule(c *rux.Context) {
	var req createScheduleReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if req.Name == "" {
		writeError(c, http.StatusBadRequest, "invalid schedule", "name is required")
		return
	}

	now := s.jobs.Now()
	scheduleType := strings.TrimSpace(req.Type)
	if scheduleType == "" {
		scheduleType = "cron"
	}
	cronExpr := strings.TrimSpace(req.Cron)
	next, err := nextScheduleRun(scheduleType, cronExpr, req.DelaySec, req.RunAt, now)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid schedule", err.Error())
		return
	}

	req.Request.CallerID = callerFromCtx(c)
	if err := s.validateScheduleRequest(req.Request); err != nil {
		writeError(c, scheduleStatus(err), "invalid schedule request", err.Error())
		return
	}

	raw, err := json.Marshal(req.Request)
	if err != nil {
		writeError(c, http.StatusBadRequest, "marshal schedule request failed", err.Error())
		return
	}

	enabled, catchUp := 1, 1
	if req.Enabled != nil && !*req.Enabled {
		enabled = 0
	}
	if req.CatchUp != nil && !*req.CatchUp {
		catchUp = 0
	}
	ts := now.Unix()
	rec := jobstore.ScheduleRecord{
		ID:           newScheduleID(now),
		Name:         req.Name,
		ScheduleType: scheduleType,
		CronExpr:     cronExpr,
		RequestJSON:  string(raw),
		Enabled:      enabled,
		NextRunAt:    next,
		CatchUp:      catchUp,
		ProjectKey:   req.Request.ProjectKey,
		CreatedAt:    ts,
		UpdatedAt:    ts,
	}
	if req.Webhook {
		// AUTO-02b: the token is minted here and returned ONCE in the create response
		// (and on every authenticated read of the schedule, since the endpoint is for a
		// trusted operator's own automation).
		token, err := newTriggerToken()
		if err != nil {
			writeError(c, http.StatusInternalServerError, "generate trigger token failed", err.Error())
			return
		}
		rec.TriggerToken = token
	}
	if err := s.jobs.Meta().InsertSchedule(rec); err != nil {
		writeError(c, scheduleStatus(err), "create schedule failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, scheduleToView(rec))
}

func (s *Server) handleListSchedules(c *rux.Context) {
	list, err := s.jobs.Meta().ListSchedules(c.Query("project"), false)
	if err != nil {
		writeError(c, scheduleStatus(err), "list schedules failed", err.Error())
		return
	}
	views := make([]scheduleView, 0, len(list))
	for _, rec := range list {
		views = append(views, scheduleToView(rec))
	}
	c.JSON(http.StatusOK, map[string]any{"schedules": views})
}

func (s *Server) handleGetSchedule(c *rux.Context) {
	rec, ok, err := s.jobs.Meta().GetSchedule(c.Param("id"))
	if err != nil {
		writeError(c, scheduleStatus(err), "get schedule failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown schedule", "no schedule with id "+c.Param("id"))
		return
	}
	c.JSON(http.StatusOK, scheduleToView(rec))
}

func (s *Server) handleDeleteSchedule(c *rux.Context) {
	id := c.Param("id")
	if _, ok, err := s.jobs.Meta().GetSchedule(id); err != nil {
		writeError(c, scheduleStatus(err), "get schedule failed", err.Error())
		return
	} else if !ok {
		writeError(c, http.StatusNotFound, "unknown schedule", "no schedule with id "+id)
		return
	}
	if err := s.jobs.Meta().DeleteSchedule(id); err != nil {
		writeError(c, scheduleStatus(err), "delete schedule failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleEnableSchedule(c *rux.Context) {
	s.setScheduleEnabled(c, 1)
}

func (s *Server) handleDisableSchedule(c *rux.Context) {
	s.setScheduleEnabled(c, 0)
}

func (s *Server) setScheduleEnabled(c *rux.Context, enabled int) {
	id := c.Param("id")
	if _, ok, err := s.jobs.Meta().GetSchedule(id); err != nil {
		writeError(c, scheduleStatus(err), "get schedule failed", err.Error())
		return
	} else if !ok {
		writeError(c, http.StatusNotFound, "unknown schedule", "no schedule with id "+id)
		return
	}
	if err := s.jobs.Meta().SetScheduleEnabled(id, enabled); err != nil {
		writeError(c, scheduleStatus(err), "set schedule enabled failed", err.Error())
		return
	}
	rec, ok, err := s.jobs.Meta().GetSchedule(id)
	if err != nil {
		writeError(c, scheduleStatus(err), "get schedule failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown schedule", "no schedule with id "+id)
		return
	}
	c.JSON(http.StatusOK, scheduleToView(rec))
}

func (s *Server) handleRunSchedule(c *rux.Context) {
	id := c.Param("id")
	rec, ok, err := s.jobs.Meta().GetSchedule(id)
	if err != nil {
		writeError(c, scheduleStatus(err), "get schedule failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown schedule", "no schedule with id "+id)
		return
	}
	var req job.JobRequest
	if err := json.Unmarshal([]byte(rec.RequestJSON), &req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid schedule request", err.Error())
		return
	}
	req.Channel = "cron"
	res, err := s.jobs.Submit(req)
	if err != nil {
		writeError(c, scheduleStatus(err), "run schedule failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// newTriggerToken mints a schedule's webhook secret (AUTO-02b): 24 random bytes in
// base64url (32 chars, no padding) — long enough that guessing is hopeless and safe to
// put in a URL or a header. A crypto/rand failure is reported, never silently replaced
// by something predictable.
func newTriggerToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// handleRotateScheduleToken is POST /v1/schedules/{id}/rotate-token (AUTO-02b,
// authenticated): it mints a fresh secret, which also ENABLES the webhook for a schedule
// created without --webhook. The old token stops working immediately.
func (s *Server) handleRotateScheduleToken(c *rux.Context) {
	id := c.Param("id")
	if _, ok, err := s.jobs.Meta().GetSchedule(id); err != nil {
		writeError(c, scheduleStatus(err), "get schedule failed", err.Error())
		return
	} else if !ok {
		writeError(c, http.StatusNotFound, "unknown schedule", "no schedule with id "+id)
		return
	}
	token, err := newTriggerToken()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "generate trigger token failed", err.Error())
		return
	}
	if err := s.jobs.Meta().SetScheduleTriggerToken(id, token); err != nil {
		writeError(c, scheduleStatus(err), "rotate trigger token failed", err.Error())
		return
	}
	rec, _, err := s.jobs.Meta().GetSchedule(id)
	if err != nil {
		writeError(c, scheduleStatus(err), "get schedule failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, scheduleToView(rec))
}

// handleScheduleTrigger is POST /v1/schedules/{id}/trigger (AUTO-02b): the EXTERNAL
// webhook entry. It is registered OUTSIDE the /v1 auth group on purpose — the caller is
// some other system's automation, which has no gofer bearer — so the schedule's own
// trigger_token is the whole credential, compared in constant time.
//
// The run is exactly the run-now semantics (submit the stored request, channel
// "webhook"), plus an event ON THE NEW JOB saying a webhook caused it. Failures are
// distinguished: an unknown schedule is 404, a missing/wrong token (or a schedule whose
// webhook is off) is 401, and a repeat within triggerRateWindow is 429 — a webhook that
// fires in a storm must not queue a hundred identical jobs.
func (s *Server) handleScheduleTrigger(c *rux.Context) {
	id := c.Param("id")
	rec, ok, err := s.jobs.Meta().GetSchedule(id)
	if err != nil {
		writeError(c, scheduleStatus(err), "get schedule failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown schedule", "no schedule with id "+id)
		return
	}
	presented := c.Query("token")
	if presented == "" {
		presented = c.Header("X-Gofer-Trigger-Token")
	}
	if rec.TriggerToken == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(rec.TriggerToken)) != 1 {
		writeError(c, http.StatusUnauthorized, "invalid trigger token",
			"a valid ?token= (or X-Gofer-Trigger-Token header) is required")
		return
	}
	if !s.allowScheduleTrigger(id, time.Now()) {
		writeError(c, http.StatusTooManyRequests, "schedule triggered too recently",
			"the same schedule may be triggered once every 10s")
		return
	}

	var req job.JobRequest
	if err := json.Unmarshal([]byte(rec.RequestJSON), &req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid schedule request", err.Error())
		return
	}
	req.Channel = channelWebhook
	// An external caller has no gofer identity; the schedule's own record is the
	// provenance, and CallerID stays empty rather than being invented.
	req.CallerID = ""
	res, err := s.jobs.Submit(req)
	if err != nil {
		writeError(c, scheduleStatus(err), "trigger schedule failed", err.Error())
		return
	}
	s.jobs.RecordJobEvent(res.ID, job.EventScheduleTriggered, map[string]any{
		"source": "webhook", "schedule_id": id,
	})
	c.JSON(http.StatusOK, res)
}

// channelWebhook is the JobRequest.Channel of a job a schedule's external webhook
// started (AUTO-02b), next to cli / web / mcp / cron.
const channelWebhook = "webhook"

// triggerRateWindow is the per-schedule minimum spacing between webhook triggers
// (AUTO-02b). It is deliberately in-memory and per-process: the endpoint's job is to
// blunt a retry storm, not to meter a billing-grade quota, and losing the state on a
// restart only means one extra run.
const triggerRateWindow = 10 * time.Second

// allowScheduleTrigger reports whether a schedule may be triggered now, recording the
// moment when it may. A schedule's first trigger is always allowed.
func (s *Server) allowScheduleTrigger(id string, now time.Time) bool {
	s.scheduleTriggerMu.Lock()
	defer s.scheduleTriggerMu.Unlock()
	if s.scheduleTriggerAt == nil {
		s.scheduleTriggerAt = make(map[string]time.Time)
	}
	if last, ok := s.scheduleTriggerAt[id]; ok && now.Sub(last) < triggerRateWindow {
		return false
	}
	s.scheduleTriggerAt[id] = now
	return true
}

func (s *Server) validateScheduleRequest(req job.JobRequest) error {
	cfg := s.jobs.Config()
	remote := job.IsRemoteRunner(cfg, req.Runner)
	_, err := s.jobs.Validate(cfg, req, remote)
	return err
}

func scheduleToView(rec jobstore.ScheduleRecord) scheduleView {
	var req job.JobRequest
	_ = json.Unmarshal([]byte(rec.RequestJSON), &req)
	return scheduleView{
		ID:         rec.ID,
		Name:       rec.Name,
		Type:       scheduleTypeOrDefault(rec.ScheduleType),
		Cron:       rec.CronExpr,
		Enabled:    rec.Enabled,
		CatchUp:    rec.CatchUp,
		NextRunAt:  rec.NextRunAt,
		LastRunAt:  rec.LastRunAt,
		LastJobID:  rec.LastJobID,
		ProjectKey: rec.ProjectKey,
		Request:    req,
		// AUTO-02b: the webhook secret is part of the schedule an operator manages, and
		// every read of it is authenticated — so it travels on the view.
		TriggerToken: rec.TriggerToken,
	}
}

func nextScheduleRun(scheduleType, cronExpr string, delaySec, runAt int64, now time.Time) (int64, error) {
	switch scheduleType {
	case "cron":
		if cronExpr == "" {
			return 0, fmt.Errorf("cron is required")
		}
		return jobstore.NextCronRun(cronExpr, now)
	case "once":
		if cronExpr != "" {
			return 0, fmt.Errorf("once schedule must not set cron")
		}
		target := runAt
		if target <= 0 && delaySec > 0 {
			target = now.Unix() + delaySec
		}
		if target <= 0 {
			return 0, fmt.Errorf("once schedule requires run_at or delay_sec")
		}
		min := now.Unix() + 3
		if target < min {
			return 0, fmt.Errorf("once run time must be at least 3 seconds in the future")
		}
		return target, nil
	default:
		return 0, fmt.Errorf("type must be cron or once")
	}
}

func scheduleTypeOrDefault(t string) string {
	if t == "" {
		return "cron"
	}
	return t
}

func newScheduleID(now time.Time) string {
	return fmt.Sprintf("sch-%d-%s", now.UnixNano(), job.RandomSuffix())
}

func scheduleStatus(err error) int {
	return http.StatusBadRequest
}

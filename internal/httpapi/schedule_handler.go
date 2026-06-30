package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// createScheduleReq is the POST /v1/schedules body. enabled/catch_up default to
// true when omitted; the persisted shape remains the jobstore 1/0 integer form.
type createScheduleReq struct {
	Name    string         `json:"name"`
	Cron    string         `json:"cron"`
	Request job.JobRequest `json:"request"`
	Enabled *bool          `json:"enabled,omitempty"`
	CatchUp *bool          `json:"catch_up,omitempty"`
}

type scheduleView struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Cron       string         `json:"cron"`
	Enabled    int            `json:"enabled"`
	CatchUp    int            `json:"catch_up"`
	NextRunAt  int64          `json:"next_run_at"`
	LastRunAt  int64          `json:"last_run_at"`
	LastJobID  string         `json:"last_job_id"`
	ProjectKey string         `json:"project_key"`
	Request    job.JobRequest `json:"request"`
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
	next, err := jobstore.NextCronRun(req.Cron, now)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid cron", err.Error())
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
		ID:          newScheduleID(now),
		Name:        req.Name,
		CronExpr:    req.Cron,
		RequestJSON: string(raw),
		Enabled:     enabled,
		NextRunAt:   next,
		CatchUp:     catchUp,
		ProjectKey:  req.Request.ProjectKey,
		CreatedAt:   ts,
		UpdatedAt:   ts,
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
		Cron:       rec.CronExpr,
		Enabled:    rec.Enabled,
		CatchUp:    rec.CatchUp,
		NextRunAt:  rec.NextRunAt,
		LastRunAt:  rec.LastRunAt,
		LastJobID:  rec.LastJobID,
		ProjectKey: rec.ProjectKey,
		Request:    req,
	}
}

func newScheduleID(now time.Time) string {
	return fmt.Sprintf("sch-%d-%s", now.UnixNano(), job.RandomSuffix())
}

func scheduleStatus(err error) int {
	return http.StatusBadRequest
}

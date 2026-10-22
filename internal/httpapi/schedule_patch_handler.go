package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// patchScheduleReq is the PATCH /v1/schedules/{id} body (design 2026-10-22 §D4). Every
// field is optional; a pointer that is nil was not sent. The identity half of a schedule
// (command, agent, runner, project) has no field on purpose — see
// patchScheduleForbiddenKeys.
type patchScheduleReq struct {
	Cron       *string              `json:"cron"`
	At         *int64               `json:"at"`
	Title      *string              `json:"title"`
	Params     *[]job.ScheduleParam `json:"params"`
	Enabled    *bool                `json:"enabled"`
	TimeoutSec *int                 `json:"timeout_sec"`
}

// patchScheduleEditable are the only body keys PATCH accepts.
var patchScheduleEditable = map[string]bool{
	"cron": true, "at": true, "title": true, "params": true, "enabled": true, "timeout_sec": true,
}

// patchScheduleForbiddenKeys is the identity half: sending one of these is a 400 that says
// the schedule has to be deleted and recreated, because those fields decide which command
// runs under whose project — editing them in place would let a PATCH reuse a different
// task's permissions and history.
var patchScheduleForbiddenKeys = map[string]string{
	"cmd":         "the command",
	"command":     "the command",
	"agent":       "the agent",
	"runner":      "the runner",
	"project":     "the project",
	"project_key": "the project",
	"request":     "the stored request",
	"name":        "the title (use \"title\")",
	"type":        "the type (set cron or at instead)",
}

// handlePatchSchedule serves PATCH /v1/schedules/{id} (design 2026-10-22 §D4): edit the
// cron expression / one-shot time, title, parameter declarations, enable switch and
// timeout. Changing cron or the run time recomputes next_run_at from now.
//
// The permission is the delete one (own schedule, or schedule.manage.others); flipping the
// switch ON additionally asks job.submit, exactly like POST .../enable, because it turns
// the cron loop loose on the stored request with no later checkpoint.
func (s *Server) handlePatchSchedule(c *rux.Context) {
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
	if !s.mayManageSchedule(c, scheduleOwner(rec)) {
		return
	}

	req, err := readPatchScheduleReq(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid schedule patch", err.Error())
		return
	}

	now := s.jobs.Now()
	upd := jobstore.ScheduleUpdate{
		Name:         rec.Name,
		ScheduleType: scheduleTypeOrDefault(rec.ScheduleType),
		CronExpr:     rec.CronExpr,
		RequestJSON:  rec.RequestJSON,
		ParamsJSON:   rec.ParamsJSON,
		Enabled:      rec.Enabled,
		NextRunAt:    rec.NextRunAt,
	}

	// ---- when it runs ----
	if req.Cron != nil && req.At != nil {
		writeError(c, http.StatusBadRequest, "invalid schedule patch", "cron and at are mutually exclusive")
		return
	}
	if req.Cron != nil {
		upd.ScheduleType = "cron"
		upd.CronExpr = strings.TrimSpace(*req.Cron)
	} else if req.At != nil {
		upd.ScheduleType = "once"
		upd.CronExpr = ""
	}
	if req.Cron != nil || req.At != nil {
		runAt := int64(0)
		if req.At != nil {
			runAt = *req.At
		}
		next, err := nextScheduleRun(upd.ScheduleType, upd.CronExpr, 0, runAt, now)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid schedule patch", err.Error())
			return
		}
		upd.NextRunAt = next
	}

	// ---- title / params / timeout ----
	if req.Title != nil {
		if strings.TrimSpace(*req.Title) == "" {
			writeError(c, http.StatusBadRequest, "invalid schedule patch", "title must not be empty")
			return
		}
		upd.Name = strings.TrimSpace(*req.Title)
	}
	if req.Params != nil {
		paramsJSON, err := job.EncodeScheduleParams(*req.Params)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid schedule params", err.Error())
			return
		}
		upd.ParamsJSON = paramsJSON
	}
	if req.TimeoutSec != nil {
		var stored job.JobRequest
		if err := json.Unmarshal([]byte(rec.RequestJSON), &stored); err != nil {
			writeError(c, http.StatusBadRequest, "invalid schedule request", err.Error())
			return
		}
		if *req.TimeoutSec < 0 {
			writeError(c, http.StatusBadRequest, "invalid schedule patch", "timeout_sec must be >= 0")
			return
		}
		stored.TimeoutSec = *req.TimeoutSec
		if err := s.validateScheduleRequest(stored); err != nil {
			writeError(c, scheduleStatus(err), "invalid schedule request", err.Error())
			return
		}
		raw, err := json.Marshal(stored)
		if err != nil {
			writeError(c, http.StatusBadRequest, "marshal schedule request failed", err.Error())
			return
		}
		upd.RequestJSON = string(raw)
	}

	// ---- enable switch ----
	if req.Enabled != nil {
		enabling := *req.Enabled && upd.Enabled == 0
		if enabling && !s.can(c, authz.PermJobSubmit) {
			s.denyScheduleAction(c, authz.PermJobSubmit)
			return
		}
		upd.Enabled = 0
		if *req.Enabled {
			upd.Enabled = 1
			// An enabled schedule must have a future instant: either this patch set a new
			// time or the stored one is still ahead. Otherwise re-arm from now.
			if req.Cron == nil && req.At == nil && upd.NextRunAt <= now.Unix() {
				next, err := nextScheduleRun(upd.ScheduleType, upd.CronExpr, 0, 0, now)
				if err != nil {
					writeError(c, http.StatusBadRequest, "invalid schedule patch", err.Error())
					return
				}
				upd.NextRunAt = next
			}
		}
	}

	if err := s.jobs.Meta().UpdateSchedule(id, upd); err != nil {
		writeError(c, scheduleStatus(err), "update schedule failed", err.Error())
		return
	}
	rec, ok, err = s.jobs.Meta().GetSchedule(id)
	if err != nil {
		writeError(c, scheduleStatus(err), "get schedule failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown schedule", "no schedule with id "+id)
		return
	}
	c.JSON(http.StatusOK, s.scheduleToViewFor(c, rec))
}

// readPatchScheduleReq decodes the body and refuses any key that is not an editable one,
// naming the reason (identity fields: delete and recreate instead).
func readPatchScheduleReq(c *rux.Context) (patchScheduleReq, error) {
	var req patchScheduleReq
	raw, _ := io.ReadAll(c.Req.Body)
	if len(bytes.TrimSpace(raw)) == 0 {
		return req, fmt.Errorf("empty body: send at least one of cron, at, title, params, enabled, timeout_sec")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return req, fmt.Errorf("body must be a JSON object: %w", err)
	}
	for key := range fields {
		if patchScheduleEditable[key] {
			continue
		}
		if what, known := patchScheduleForbiddenKeys[key]; known {
			return req, fmt.Errorf("%s cannot be changed by PATCH: delete the schedule and create a new one", what)
		}
		return req, fmt.Errorf("unknown field %q (editable: cron, at, title, params, enabled, timeout_sec)", key)
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return req, err
	}
	return req, nil
}

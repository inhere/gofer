package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/overview"
)

// Dashboard statistics wall (gofer-yelm, design
// docs/design/2026-10-09-dashboard-redesign-design.md): the handlers only bind /
// validate and forward (G021). The aggregation and its cache live in internal/overview,
// the per-job metrics backfill in job.Service.BackfillMetrics.

// GET /v1/stats/overview?range=today|7d|30d|all&tz=<UTC offset in minutes, east positive>
// (range defaults to 7d; today adds the 24-row hourly series).
func (s *Server) handleStatsOverview(c *rux.Context) {
	if s.overview == nil {
		writeError(c, http.StatusServiceUnavailable, "overview unavailable", "no job store wired on this server")
		return
	}
	q := overview.Query{Range: strings.TrimSpace(c.Query("range")), TZMin: serverTZOffsetSec() / 60}
	if raw := strings.TrimSpace(c.Query("tz")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid tz", "tz must be the UTC offset in minutes (e.g. 480)")
			return
		}
		q.TZMin = n
	}
	ov, err := s.overview.Get(q)
	if err != nil {
		if errors.Is(err, overview.ErrInvalidQuery) {
			writeError(c, http.StatusBadRequest, "invalid overview query", err.Error())
			return
		}
		writeError(c, http.StatusInternalServerError, "build overview failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, ov)
}

// statsBackfillReq is the POST /v1/stats/backfill body (one batch; the CLI loops on
// the returned cursor until done).
type statsBackfillReq struct {
	Since      int64  `json:"since"`
	AfterEnded int64  `json:"after_ended"`
	AfterID    string `json:"after_id"`
	Limit      int    `json:"limit"`
	Force      bool   `json:"force"`
}

// statsBackfillBudget bounds one batch well inside the CLI's 30s HTTP deadline.
var statsBackfillBudget = 15 * time.Second

// POST /v1/stats/backfill — compute job_metrics for jobs that ended before the live
// write existed (`gofer tool stats-backfill`). A person's action only.
func (s *Server) handleStatsBackfill(c *rux.Context) {
	if callerKindFromCtx(c) != callerKindUser {
		writeError(c, http.StatusForbidden, "stats backfill requires a user caller", "only a person runs the metrics backfill")
		return
	}
	if s.jobs == nil {
		writeError(c, http.StatusServiceUnavailable, "backfill unavailable", "no job service wired on this server")
		return
	}
	var body statsBackfillReq
	if c.Req.ContentLength != 0 {
		if err := c.BindJSON(&body); err != nil {
			writeError(c, http.StatusBadRequest, "invalid body", err.Error())
			return
		}
	}
	if body.Limit < 0 || body.Limit > 1000 || body.Since < 0 {
		writeError(c, http.StatusBadRequest, "invalid backfill request", "limit must be 0-1000 and since >= 0")
		return
	}
	res, err := s.jobs.BackfillMetrics(job.BackfillOptions{
		Since: body.Since, AfterEnded: body.AfterEnded, AfterID: body.AfterID,
		Limit: body.Limit, Force: body.Force, Budget: statsBackfillBudget,
	})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "stats backfill failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
)

// Session nudges (N2 §E, SESS-12): REST face of sessionrelay's nudge rules. A person
// (user / admin caller, and only the session's owner or a can_answer caller) sets and
// manages them; job credentials — steward included — are refused by the SEC-01 tables
// before a handler runs, and the handlers re-check the caller kind as the second lock.

// sessionNudgeView is the JSON projection of one nudge. Times are unix seconds.
type sessionNudgeView struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	Kind        string `json:"kind"` // every | stalled
	IntervalSec int64  `json:"interval_sec"`
	Text        string `json:"text"`
	UntilAt     int64  `json:"until_at,omitempty"`
	State       string `json:"state"` // active | paused | ended
	PauseReason string `json:"pause_reason,omitempty"`
	EndedReason string `json:"ended_reason,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	NextRunAt   int64  `json:"next_run_at,omitempty"`
	LastFiredAt int64  `json:"last_fired_at,omitempty"`
	FireCount   int64  `json:"fire_count"`
	FailCount   int64  `json:"fail_count"`
	LastError   string `json:"last_error,omitempty"`
}

func toNudgeView(n jobstore.SessionNudge) sessionNudgeView {
	return sessionNudgeView{ID: n.ID, SessionID: n.SessionID, Kind: n.Kind, IntervalSec: n.IntervalSec,
		Text: n.Text, UntilAt: n.UntilAt, State: n.State, PauseReason: n.PauseReason, EndedReason: n.EndedReason,
		CreatedBy: n.CreatedBy, CreatedAt: n.CreatedAt, NextRunAt: n.NextRunAt, LastFiredAt: n.LastFiredAt,
		FireCount: n.FireCount, FailCount: n.FailCount, LastError: n.LastError}
}

func nudgeViews(list []jobstore.SessionNudge) []sessionNudgeView {
	out := make([]sessionNudgeView, 0, len(list))
	for _, n := range list {
		out = append(out, toNudgeView(n))
	}
	return out
}

func nudgeStatus(err error) int {
	if errors.Is(err, sessionrelay.ErrNudgeNotFound) {
		return http.StatusNotFound
	}
	return relayStatus(err)
}

// nudgePersonOnly is the second lock behind the SEC-01 tables: only a person manages nudges.
func nudgePersonOnly(c *rux.Context) bool {
	if callerKindFromCtx(c) == callerKindJob {
		writeError(c, http.StatusForbidden, "session nudge not permitted for this caller",
			"only a person sets session nudges: a job credential (steward included) cannot")
		return false
	}
	return true
}

type createNudgeReq struct {
	Kind        string `json:"kind"`
	IntervalSec int64  `json:"interval_sec"`
	Text        string `json:"text"`
	Message     string `json:"message"`
	UntilAt     int64  `json:"until_at"`
}

// POST /v1/sessions/{sid}/nudges
func (s *Server) handleCreateSessionNudge(c *rux.Context) {
	if !s.relayReady(c) || !nudgePersonOnly(c) {
		return
	}
	sid := c.Param("sid")
	if !s.sessionMayAnswer(c, sid, "set session nudge") {
		return
	}
	var body createNudgeReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	text := body.Text
	if text == "" {
		text = body.Message
	}
	n, err := s.relay.CreateNudge(sid, sessionrelay.NudgeInput{Kind: body.Kind,
		Interval: time.Duration(body.IntervalSec) * time.Second, Text: text, UntilAt: body.UntilAt, By: callerFromCtx(c)})
	if err != nil {
		writeError(c, nudgeStatus(err), "set session nudge failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, toNudgeView(n))
}

// GET /v1/sessions/{sid}/nudges[?all=1]
func (s *Server) handleListSessionNudges(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	list, err := s.relay.Nudges(c.Param("sid"), c.Query("all") == "1" || c.Query("all") == "true")
	if err != nil {
		writeError(c, nudgeStatus(err), "list session nudges failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"nudges": nudgeViews(list)})
}

// GET /v1/nudges[?all=1] — every session's nudges (the CLI's `nudge ls` without an id).
func (s *Server) handleListAllNudges(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	list, err := s.jobs.Meta().ListSessionNudges("", c.Query("all") == "1" || c.Query("all") == "true")
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list session nudges failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"nudges": nudgeViews(list)})
}

// nudgeForManage loads the nudge and applies the person + session-owner gate.
func (s *Server) nudgeForManage(c *rux.Context, action string) (jobstore.SessionNudge, bool) {
	if !s.relayReady(c) || !nudgePersonOnly(c) {
		return jobstore.SessionNudge{}, false
	}
	n, err := s.relay.Nudge(c.Param("id"))
	if err != nil {
		writeError(c, nudgeStatus(err), action+" failed", err.Error())
		return jobstore.SessionNudge{}, false
	}
	if !s.sessionMayAnswer(c, n.SessionID, action) {
		return jobstore.SessionNudge{}, false
	}
	return n, true
}

// PATCH /v1/nudges/{id}  body {state: "paused" | "active"}
func (s *Server) handlePatchNudge(c *rux.Context) {
	n, ok := s.nudgeForManage(c, "change session nudge")
	if !ok {
		return
	}
	var body struct {
		State string `json:"state"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	var (
		out jobstore.SessionNudge
		err error
	)
	switch body.State {
	case jobstore.NudgePaused:
		out, err = s.relay.PauseNudge(n.ID)
	case jobstore.NudgeActive, "resume":
		out, err = s.relay.ResumeNudge(n.ID)
	default:
		writeError(c, http.StatusBadRequest, "invalid nudge state", `state must be "paused" or "active"`)
		return
	}
	if err != nil {
		writeError(c, nudgeStatus(err), "change session nudge failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, toNudgeView(out))
}

// DELETE /v1/nudges/{id}
func (s *Server) handleDeleteNudge(c *rux.Context) {
	n, ok := s.nudgeForManage(c, "delete session nudge")
	if !ok {
		return
	}
	if err := s.relay.RemoveNudge(n.ID); err != nil {
		writeError(c, nudgeStatus(err), "delete session nudge failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"id": n.ID, "deleted": true})
}

// nudgeSweepInterval is the sweeper cadence: well under the 1m minimum nudge interval.
const nudgeSweepInterval = 30 * time.Second

// StartNudgeSweeper runs the session-nudge sweeper (N2 §E) until stop closes: one pass at
// startup, then every nudgeSweepInterval. It is the server's own goroutine because the
// relay service lives here; serve only starts it.
func (s *Server) StartNudgeSweeper(stop <-chan struct{}) {
	if s.relay == nil {
		return
	}
	go func() {
		ctx, cancel := contextFromStop(stop)
		defer cancel()
		s.relay.SweepNudges(ctx, time.Now().Unix())
		t := time.NewTicker(nudgeSweepInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				s.relay.SweepNudges(ctx, time.Now().Unix())
			}
		}
	}()
}

func contextFromStop(stop <-chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

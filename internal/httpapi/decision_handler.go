package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/jobstore"
)

// decisionView is the HTTP projection of a plan_decisions row (decision
// channel, Part C §C3). Options is the options_json column DESERIALISED at the
// view layer: nil/empty means free-text answer. Timestamps are unix seconds
// (web multiplies by 1000 for display).
type decisionView struct {
	ID         string   `json:"id"`
	PlanID     string   `json:"plan_id,omitempty"`
	Title      string   `json:"title"`
	Question   string   `json:"question"`
	Options    []string `json:"options,omitempty"`
	Answer     string   `json:"answer,omitempty"`
	State      string   `json:"state"`
	TimeoutSec int64    `json:"timeout_sec"`
	AskedAt    int64    `json:"asked_at"`
	AnsweredAt int64    `json:"answered_at,omitempty"`
	AnsweredBy string   `json:"answered_by,omitempty"`
	// SessionID / Kind identify a session-relay turn (SESS-01, kind="relay");
	// both empty for a plain gofer_ask_human decision.
	SessionID string `json:"session_id,omitempty"`
	Kind      string `json:"kind,omitempty"`
}

func toDecisionView(d jobstore.PlanDecision) decisionView {
	v := decisionView{
		ID: d.ID, PlanID: d.PlanID, Title: d.Title, Question: d.Question,
		Answer: d.Answer, State: d.State, TimeoutSec: d.TimeoutSec,
		AskedAt: d.AskedAt, AnsweredAt: d.AnsweredAt, AnsweredBy: d.AnsweredBy,
		SessionID: d.SessionID, Kind: d.Kind,
	}
	if d.OptionsJSON != "" {
		// options_json is written only by InsertDecision from validated input;
		// a malformed blob degrades to free-text rather than failing the read.
		_ = json.Unmarshal([]byte(d.OptionsJSON), &v.Options)
	}
	return v
}

// askDecisionReq is the POST /v1/decisions body. PlanID is optional (empty =
// global question); Options empty = free-text answer. TimeoutSec is clamped
// authoritatively by jobstore.InsertDecision (plan HIGH-2).
type askDecisionReq struct {
	PlanID     string   `json:"plan_id,omitempty"`
	Title      string   `json:"title"`
	Question   string   `json:"question"`
	Options    []string `json:"options,omitempty"`
	TimeoutSec int64    `json:"timeout_sec,omitempty"`
}

// handleAskDecision raises an OPEN decision (D1: single ask entry, plan_id in
// body). A non-empty plan_id must reference an existing plan (L2: 404 on a
// dangling one).
func (s *Server) handleAskDecision(c *rux.Context) {
	var body askDecisionReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		writeError(c, http.StatusBadRequest, "title required", "a decision requires a title")
		return
	}
	if strings.TrimSpace(body.Question) == "" {
		writeError(c, http.StatusBadRequest, "question required", "a decision requires a question")
		return
	}
	planID := strings.TrimSpace(body.PlanID)
	if planID != "" {
		if _, ok, err := s.jobs.Meta().GetPlan(planID); err != nil {
			writeError(c, http.StatusInternalServerError, "get plan failed", err.Error())
			return
		} else if !ok {
			writeError(c, http.StatusNotFound, "unknown plan", "no plan with id "+planID)
			return
		}
	}
	var optionsJSON string
	if len(body.Options) > 0 {
		raw, err := json.Marshal(body.Options)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid options", err.Error())
			return
		}
		optionsJSON = string(raw)
	}
	d := jobstore.PlanDecision{
		PlanID:      planID,
		Title:       body.Title,
		Question:    body.Question,
		OptionsJSON: optionsJSON,
		TimeoutSec:  body.TimeoutSec,
	}
	if err := s.jobs.Meta().InsertDecision(&d); err != nil {
		writeError(c, http.StatusInternalServerError, "ask decision failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, toDecisionView(d))
}

// handleListDecisions lists decisions with optional ?state= / ?plan_id=
// filters (bell / PlanDetail discovery).
func (s *Server) handleListDecisions(c *rux.Context) {
	state := strings.TrimSpace(c.Query("state"))
	if state != "" && !jobstore.ValidDecisionState(state) {
		writeError(c, http.StatusBadRequest, "invalid state",
			"state must be one of OPEN|ANSWERED|EXPIRED")
		return
	}
	list, err := s.jobs.Meta().ListDecisions(state, strings.TrimSpace(c.Query("plan_id")))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list decisions failed", err.Error())
		return
	}
	out := make([]decisionView, 0, len(list))
	for _, d := range list {
		out = append(out, toDecisionView(*d))
	}
	c.JSON(http.StatusOK, map[string]any{"decisions": out})
}

// handleGetDecision returns one decision (MCP polling path).
func (s *Server) handleGetDecision(c *rux.Context) {
	id := c.Param("id")
	d, ok, err := s.jobs.Meta().GetDecision(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get decision failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown decision", "no decision with id "+id)
		return
	}
	c.JSON(http.StatusOK, toDecisionView(d))
}

type answerDecisionReq struct {
	Answer string `json:"answer"`
}

// handleAnswerDecision records the human answer. Error mapping deliberately
// splits 404/409 (plan M5, NOT the interaction precedent): unknown id → 404
// (via GetDecision first); known but already answered/expired → 409 (the
// conditional UPDATE affecting 0 rows).
func (s *Server) handleAnswerDecision(c *rux.Context) {
	id := c.Param("id")
	var body answerDecisionReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if strings.TrimSpace(body.Answer) == "" {
		writeError(c, http.StatusBadRequest, "answer required", "answering requires a non-empty answer")
		return
	}
	if _, ok, err := s.jobs.Meta().GetDecision(id); err != nil {
		writeError(c, http.StatusInternalServerError, "get decision failed", err.Error())
		return
	} else if !ok {
		writeError(c, http.StatusNotFound, "unknown decision", "no decision with id "+id)
		return
	}
	ok, err := s.jobs.Meta().AnswerDecision(id, body.Answer, callerFromCtx(c))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "answer decision failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusConflict, "decision not open",
			"decision "+id+" is already answered or expired")
		return
	}
	d, _, err := s.jobs.Meta().GetDecision(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "reload decision failed", err.Error())
		return
	}
	// A relay turn answered from the bell: the waiting session goes back to running.
	if s.relay != nil {
		s.relay.OnAnswered(d)
	}
	c.JSON(http.StatusOK, toDecisionView(d))
}

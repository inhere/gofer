package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

// Work items (W1, design 2026-10-05): REST face of the work service. Handlers only
// bind/validate and translate errors; the rules live in internal/work and jobstore.

// Work returns the work-item service (nil without a job store); serve starts its
// background loop and feeds it config.
func (s *Server) Work() *work.Service { return s.work }

// workJobProbe adapts job.Service to the work service's linked-job lookup.
type workJobProbe struct{ jobs *job.Service }

func (p workJobProbe) JobStates(ids []string) map[string]work.JobState {
	out := make(map[string]work.JobState, len(ids))
	pending := map[string]bool{}
	if inter, err := p.jobs.ListPendingInteractions(); err == nil {
		for _, it := range inter {
			pending[it.JobID] = true
		}
	}
	for _, id := range ids {
		st := work.JobState{PendingInteraction: pending[id]}
		if r, ok := p.jobs.Get(id); ok {
			st.Status = r.Status
		}
		out[id] = st
	}
	return out
}

func (s *Server) workReady(c *rux.Context) bool {
	if s.work == nil {
		writeError(c, http.StatusServiceUnavailable, "work items unavailable", "no job store wired on this server")
		return false
	}
	return true
}

// workBy names the journal author for a request: a person (`human:<caller>`), or the job
// whose credential made it.
func workBy(c *rux.Context) string {
	if jc, ok := jobCallerFromCtx(c); ok {
		return "job:" + jc.JobID
	}
	if id := callerFromCtx(c); id != "" {
		return "human:" + id
	}
	return "human"
}

// workNotAWorker refuses worker transport tokens: a work item is a person's record.
func workNotAWorker(c *rux.Context) bool {
	if callerKindFromCtx(c) == callerKindWorker {
		writeError(c, http.StatusForbidden, "work items not permitted for this caller",
			"worker tokens are a transport credential and cannot read or write work items")
		return false
	}
	return true
}

func writeWorkError(c *rux.Context, err error, what string) {
	switch {
	case errors.Is(err, jobstore.ErrWorkItemNotFound):
		writeError(c, http.StatusNotFound, what+" failed", err.Error())
	case errors.Is(err, jobstore.ErrWorkItemConflict):
		writeError(c, http.StatusConflict, what+" failed", err.Error())
	case errors.Is(err, jobstore.ErrWorkInvalid), errors.Is(err, work.ErrEmptyReport):
		writeError(c, http.StatusBadRequest, what+" failed", err.Error())
	default:
		writeError(c, http.StatusInternalServerError, what+" failed", err.Error())
	}
}

// GET /v1/work-items
func (s *Server) handleListWorkItems(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	o := jobstore.WorkListOpts{
		Project: c.Query("project"), Workspace: c.Query("workspace"), SessionID: c.Query("session"),
		Query: c.Query("q"), IncludeClosed: queryBool(c, "closed"), IncludeMerged: queryBool(c, "merged"),
		Due: queryBool(c, "due"),
	}
	if raw := strings.TrimSpace(c.Query("status")); raw != "" {
		for _, st := range strings.Split(raw, ",") {
			if st = strings.TrimSpace(st); st != "" {
				o.Statuses = append(o.Statuses, st)
			}
		}
	}
	if raw := c.Query("unsorted"); raw != "" {
		v := queryBool(c, "unsorted")
		o.Unsorted = &v
	}
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			o.Limit = n
		}
	}
	items, err := s.work.List(o)
	if err != nil {
		writeWorkError(c, err, "list work items")
		return
	}
	s.fixWorkRunners(items)
	sum, err := s.work.Summarize()
	if err != nil {
		writeWorkError(c, err, "list work items")
		return
	}
	c.JSON(http.StatusOK, map[string]any{"items": items, "summary": sum})
}

// fixWorkRunners shows the same runner spelling the Sessions page does (G043).
func (s *Server) fixWorkRunners(items []work.ItemView) {
	for i := range items {
		for j := range items[i].Sessions {
			items[i].Sessions[j].Runner = s.resolveRunnerName(items[i].Sessions[j].Runner)
		}
	}
}

type workCreateReq struct {
	Title      string   `json:"title"`
	Goal       string   `json:"goal"`
	Status     string   `json:"status"`
	ProjectKey string   `json:"project_key"`
	Workspace  string   `json:"workspace"`
	NextStep   string   `json:"next_step"`
	Priority   int      `json:"priority"`
	SessionIDs []string `json:"session_ids"`
}

// POST /v1/work-items
func (s *Server) handleCreateWorkItem(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	var body workCreateReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	w, err := s.work.Store().CreateWorkItem(jobstore.WorkItemInput{
		Title: body.Title, Goal: body.Goal, Status: body.Status, ProjectKey: body.ProjectKey, Workspace: body.Workspace,
		NextStep: body.NextStep, Priority: body.Priority, SessionIDs: body.SessionIDs, Source: jobstore.WorkOriginHuman,
		By: workBy(c),
	})
	if err != nil {
		writeWorkError(c, err, "create work item")
		return
	}
	s.work.SyncItem(w.ID)
	s.respondWorkDetail(c, w.ID, http.StatusOK)
}

func (s *Server) respondWorkDetail(c *rux.Context, id string, status int) {
	d, err := s.work.Detail(id, 200)
	if err != nil {
		writeWorkError(c, err, "get work item")
		return
	}
	for i := range d.Sessions {
		d.Sessions[i].Runner = s.resolveRunnerName(d.Sessions[i].Runner)
	}
	c.JSON(status, d)
}

// GET /v1/work-items/{id}
func (s *Server) handleGetWorkItem(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	s.respondWorkDetail(c, c.Param("id"), http.StatusOK)
}

type workPatchReq struct {
	Rev          int64   `json:"rev"`
	Title        *string `json:"title"`
	Goal         *string `json:"goal"`
	Status       *string `json:"status"`
	StatusSource *string `json:"status_source"`
	BlockerKind  *string `json:"blocker_kind"`
	BlockerText  *string `json:"blocker_text"`
	NextStep     *string `json:"next_step"`
	Summary      *string `json:"summary"`
	ProjectKey   *string `json:"project_key"`
	Workspace    *string `json:"workspace"`
	Priority     *int    `json:"priority"`
	ParkUntil    *int64  `json:"park_until"`
	ParkNote     *string `json:"park_note"`
	RemindAt     *int64  `json:"remind_at"`
	Unsorted     *bool   `json:"unsorted"`
}

// PATCH /v1/work-items/{id}
func (s *Server) handlePatchWorkItem(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	var body workPatchReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	// `status_source` is not a free knob: a person can only hand the status BACK to
	// the automatic mapping ("auto"); `human` is implied by setting a status.
	if body.StatusSource != nil && *body.StatusSource != jobstore.WorkSourceAuto {
		writeError(c, http.StatusBadRequest, "invalid status_source", `only "auto" may be requested; setting a status makes it the human's`)
		return
	}
	id := c.Param("id")
	_, err := s.work.Update(id, jobstore.WorkItemPatch{
		Title: body.Title, Goal: body.Goal, Status: body.Status, StatusSource: body.StatusSource,
		BlockerKind: body.BlockerKind, BlockerText: body.BlockerText, NextStep: body.NextStep, Summary: body.Summary,
		ProjectKey: body.ProjectKey, Workspace: body.Workspace, Priority: body.Priority, ParkUntil: body.ParkUntil,
		ParkNote: body.ParkNote, RemindAt: body.RemindAt, Unsorted: body.Unsorted,
	}, body.Rev, workBy(c))
	if err != nil {
		if errors.Is(err, jobstore.ErrWorkItemConflict) {
			if d, derr := s.work.Detail(id, 200); derr == nil {
				c.JSON(http.StatusConflict, map[string]any{"error": "update work item failed", "detail": err.Error(), "current": d})
				return
			}
		}
		writeWorkError(c, err, "update work item")
		return
	}
	s.respondWorkDetail(c, id, http.StatusOK)
}

// GET /v1/work-items/{id}/journal
func (s *Server) handleListWorkJournal(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	if _, ok, err := s.work.Store().GetWorkItem(c.Param("id")); err != nil {
		writeWorkError(c, err, "list work journal")
		return
	} else if !ok {
		writeWorkError(c, jobstore.ErrWorkItemNotFound, "list work journal")
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	before, _ := strconv.ParseInt(c.Query("before"), 10, 64)
	j, err := s.work.Store().ListWorkJournal(c.Param("id"), limit, before)
	if err != nil {
		writeWorkError(c, err, "list work journal")
		return
	}
	c.JSON(http.StatusOK, map[string]any{"journal": j})
}

// POST /v1/work-items/{id}/journal — a person's note.
func (s *Server) handleAddWorkJournal(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	e, err := s.work.Store().AppendWorkJournal(c.Param("id"), jobstore.WorkJournalNote, body.Text, workBy(c))
	if err != nil {
		writeWorkError(c, err, "add work note")
		return
	}
	c.JSON(http.StatusOK, e)
}

// GET /v1/work-items/{id}/sessions
func (s *Server) handleListWorkSessions(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	d, err := s.work.Detail(c.Param("id"), 1)
	if err != nil {
		writeWorkError(c, err, "list work sessions")
		return
	}
	for i := range d.Sessions {
		d.Sessions[i].Runner = s.resolveRunnerName(d.Sessions[i].Runner)
	}
	c.JSON(http.StatusOK, map[string]any{"sessions": d.Sessions})
}

// POST /v1/work-items/{id}/sessions {session_id}
func (s *Server) handleAttachWorkSession(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	var body struct {
		SessionID string `json:"session_id"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	sid := strings.TrimSpace(body.SessionID)
	if s.relay != nil {
		if _, err := s.relay.Session(sid); err != nil {
			writeError(c, relayStatus(err), "attach work session failed", err.Error())
			return
		}
	}
	if err := s.work.Store().AttachWorkSession(c.Param("id"), sid, workBy(c)); err != nil {
		writeWorkError(c, err, "attach work session")
		return
	}
	s.work.SyncItem(c.Param("id"))
	s.respondWorkDetail(c, c.Param("id"), http.StatusOK)
}

// DELETE /v1/work-items/{id}/sessions/{sid}
func (s *Server) handleDetachWorkSession(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	if err := s.work.Store().DetachWorkSession(c.Param("id"), c.Param("sid"), workBy(c)); err != nil {
		writeWorkError(c, err, "detach work session")
		return
	}
	s.respondWorkDetail(c, c.Param("id"), http.StatusOK)
}

// GET /v1/work-items/{id}/links
func (s *Server) handleListWorkLinks(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	if _, ok, _ := s.work.Store().GetWorkItem(c.Param("id")); !ok {
		writeWorkError(c, jobstore.ErrWorkItemNotFound, "list work links")
		return
	}
	l, err := s.work.Store().ListWorkLinks(c.Param("id"))
	if err != nil {
		writeWorkError(c, err, "list work links")
		return
	}
	c.JSON(http.StatusOK, map[string]any{"links": l})
}

// POST /v1/work-items/{id}/links {kind, ref}
func (s *Server) handleAddWorkLink(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	var body struct {
		Kind string `json:"kind"`
		Ref  string `json:"ref"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if _, err := s.work.Store().AddWorkLink(c.Param("id"), body.Kind, body.Ref, workBy(c)); err != nil {
		writeWorkError(c, err, "add work link")
		return
	}
	s.work.SyncItem(c.Param("id"))
	s.respondWorkDetail(c, c.Param("id"), http.StatusOK)
}

// DELETE /v1/work-items/{id}/links?kind=&ref=
func (s *Server) handleRemoveWorkLink(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	if _, err := s.work.Store().RemoveWorkLink(c.Param("id"), c.Query("kind"), c.Query("ref"), workBy(c)); err != nil {
		writeWorkError(c, err, "remove work link")
		return
	}
	s.respondWorkDetail(c, c.Param("id"), http.StatusOK)
}

// POST /v1/work-items/{id}/merge {sources:[...]}
func (s *Server) handleMergeWorkItems(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	var body struct {
		Sources []string `json:"sources"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if _, err := s.work.Store().MergeWorkItems(c.Param("id"), body.Sources, workBy(c)); err != nil {
		writeWorkError(c, err, "merge work items")
		return
	}
	s.work.SyncItem(c.Param("id"))
	s.respondWorkDetail(c, c.Param("id"), http.StatusOK)
}

// POST /v1/work-items/{id}/split
func (s *Server) handleSplitWorkItem(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	var body struct {
		Title        string   `json:"title"`
		Goal         string   `json:"goal"`
		SessionIDs   []string `json:"session_ids"`
		KeepSessions bool     `json:"keep_sessions"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	src, nw, err := s.work.Store().SplitWorkItem(c.Param("id"), jobstore.SplitWorkItemInput{
		Title: body.Title, Goal: body.Goal, SessionIDs: body.SessionIDs, KeepSessions: body.KeepSessions, By: workBy(c),
	})
	if err != nil {
		writeWorkError(c, err, "split work item")
		return
	}
	s.work.SyncItem(src.ID)
	s.work.SyncItem(nw.ID)
	newDetail, err := s.work.Detail(nw.ID, 200)
	if err != nil {
		writeWorkError(c, err, "split work item")
		return
	}
	srcDetail, err := s.work.Detail(src.ID, 200)
	if err != nil {
		writeWorkError(c, err, "split work item")
		return
	}
	c.JSON(http.StatusOK, map[string]any{"source": srcDetail, "item": newDetail})
}

type workReportReq struct {
	Goal      string `json:"goal"`
	Status    string `json:"status"`
	Blocker   string `json:"blocker"`
	Next      string `json:"next"`
	Summary   string `json:"summary"`
	SessionID string `json:"session_id"`
}

// POST /v1/work-items/{id}/report — a session's (or its job's) self-report. Open to a
// job credential: it only appends a report line and fills the descriptive fields.
func (s *Server) handleReportWorkItem(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	var body workReportReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	by := workBy(c)
	if sid := strings.TrimSpace(body.SessionID); sid != "" {
		by = s.work.SessionBy(sid)
	}
	if _, err := s.work.Report(c.Param("id"), work.ReportInput{
		Goal: body.Goal, Status: body.Status, Blocker: body.Blocker, Next: body.Next, Summary: body.Summary, By: by,
	}); err != nil {
		writeWorkError(c, err, "report work item")
		return
	}
	s.respondWorkDetail(c, c.Param("id"), http.StatusOK)
}

// workReportRequestText is the fixed text sent to a running session when the human
// presses "请它汇报" (phase 2's steward will take over the tidy-up for the others).
func workReportRequestText(id, title string) string {
	return fmt.Sprintf("[gofer 工作项汇报请求] 请汇报工作项 %s「%s」当前进展：运行 "+
		"`gofer work report %s --goal \"<目标>\" --status <active|needs_me|waiting_resource|needs_onsite|review|parked> "+
		"--blocker \"<卡在哪>\" --next \"<下一步>\" --summary \"<做到哪了>\"`（不需要的字段可省略；若已不再受阻请用 --status active）。",
		id, title, id)
}

type workReportRequestResult struct {
	SessionID string `json:"session_id"`
	Sent      bool   `json:"sent"`
	Channel   string `json:"channel,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// POST /v1/work-items/{id}/report-request {session_id?}
func (s *Server) handleWorkReportRequest(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) || !s.relayReady(c) {
		return
	}
	if callerKindFromCtx(c) == callerKindJob {
		writeError(c, http.StatusForbidden, "job credential may not ask a session to report", "only a person can ask a session to report")
		return
	}
	var body struct {
		SessionID string `json:"session_id"`
	}
	_ = c.BindJSON(&body)
	d, err := s.work.Detail(c.Param("id"), 1)
	if err != nil {
		writeWorkError(c, err, "request work report")
		return
	}
	text := workReportRequestText(d.ID, d.Title)
	results := make([]workReportRequestResult, 0, len(d.Sessions))
	for _, sb := range d.Sessions {
		if sb.Role != jobstore.WorkSessionCurrent {
			continue
		}
		if body.SessionID != "" && body.SessionID != sb.SessionID {
			continue
		}
		r := workReportRequestResult{SessionID: sb.SessionID}
		switch {
		case sb.Missing:
			r.Reason = "会话记录已不存在"
		case sb.State == jobstore.SessionEnded || sb.State == jobstore.SessionOffline || sb.State == jobstore.SessionHandedOff:
			r.Reason = "会话未在运行（" + sb.State + "），二期由管家整理"
		case !s.sessionMayAnswerQuiet(c, sb.SessionID):
			r.Reason = "无权向该会话传话"
		default:
			ctx, cancel := context.WithTimeout(c.Req.Context(), 2*time.Minute)
			m, merr := s.relay.SendMessage(ctx, sb.SessionID, text, callerFromCtx(c))
			cancel()
			if merr != nil {
				r.Reason = merr.Error()
			} else if m.Status == jobstore.SessionMessageFailed {
				r.Reason = m.Error
			} else {
				r.Sent, r.Channel = true, m.Channel
			}
		}
		results = append(results, r)
	}
	if len(results) == 0 {
		writeError(c, http.StatusConflict, "no running session", "this work item has no current session to ask")
		return
	}
	sent := false
	for _, r := range results {
		if r.Sent {
			sent = true
			_, _ = s.work.Store().AppendWorkJournal(d.ID, jobstore.WorkJournalNote, "已请会话 "+r.SessionID[:min(8, len(r.SessionID))]+" 汇报", workBy(c))
		}
	}
	c.JSON(http.StatusOK, map[string]any{"sent": sent, "results": results})
}

// sessionMayAnswerQuiet is the owner check of sessionMayAnswer without writing a response.
func (s *Server) sessionMayAnswerQuiet(c *rux.Context, sid string) bool {
	a, err := s.relay.Session(sid)
	if err != nil {
		return false
	}
	by := callerFromCtx(c)
	if a.CallerID == "" || a.CallerID == by {
		return true
	}
	return s.cfg != nil && s.cfg.Governance.RequireAnswerCapability && s.cfg.CallerCanAnswer(by)
}

// GET /v1/work-items/digest — the digest as it would be sent now (no side effects).
func (s *Server) handleWorkDigestPreview(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	d, err := s.work.BuildDigest(time.Now())
	if err != nil {
		writeWorkError(c, err, "build work digest")
		return
	}
	c.JSON(http.StatusOK, d)
}

// POST /v1/work-items/digest — send it now to the subscribed webhooks.
func (s *Server) handleWorkDigestSend(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	d, n, err := s.work.SendDigest(time.Now())
	if err != nil {
		writeWorkError(c, err, "send work digest")
		return
	}
	c.JSON(http.StatusOK, map[string]any{"digest": d, "queued": n})
}

package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/stage"
)

// The stage flow routes (plan 2026-11-26-flow-progression-plan.md, F1):
//
//	GET  /v1/work-items/{id}/stage                         the flow with the current gate evaluated
//	POST /v1/work-items/{id}/stage                         start a flow on that work item
//	POST /v1/work-items/stage                              start by issue (the item linked to it, else a new one)
//	POST /v1/work-items/{id}/stage/{done|back|blocked|approve|drop}
//
// The business lives in internal/stage; this file binds requests, applies the caller rules
// and maps errors to statuses. Work items are a person's record, so every route refuses a
// worker token (workNotAWorker); a job credential reaches only the reads (SEC-01 default-denies
// its writes). Approve is a person's alone: not a job, not a worker, and not a request that
// names a registered live session (the CLI inside a terminal session sends the user's token
// plus X-Gofer-Session-Id — that is the agent, not the person).

// stageHolder builds the stage service once, on first use.
type stageHolder struct {
	once sync.Once
	svc  *stage.Service
}

// stages returns the stage service; nil when this server has no metadata store.
func (s *Server) stages() *stage.Service {
	s.stageSvc.once.Do(func() {
		if s.jobs == nil || s.jobs.Meta() == nil {
			return
		}
		s.stageSvc.svc = stage.New(stage.Deps{
			Store: s.jobs.Meta(),
			Config: func() *config.Config {
				if s.projects == nil {
					return nil
				}
				return s.projects.Config()
			},
		})
	})
	return s.stageSvc.svc
}

// Stages exposes the stage service to the other entry layers (the session view, the
// reminders); nil without a store.
func (s *Server) Stages() *stage.Service { return s.stages() }

func (s *Server) stageReady(c *rux.Context) (*stage.Service, bool) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return nil, false
	}
	svc := s.stages()
	if svc == nil {
		writeError(c, http.StatusServiceUnavailable, "stage flow unavailable", "no job store wired on this server")
		return nil, false
	}
	return svc, true
}

// stageBy names who acts: the registered live session the request names (an agent in a
// terminal session), else the person / credential as for every work item write.
func (s *Server) stageBy(c *rux.Context) string {
	if w := s.seatWriter(c); strings.HasPrefix(w, jobstore.SeatBySessionPrefix) {
		return w
	}
	return workBy(c)
}

// stageJobProject keeps a job credential inside its own project (F2): a resident agent may
// start and move the flow of a work item in the project its job belongs to, and nothing
// else. Every other caller kind passes untouched.
func (s *Server) stageJobProject(c *rux.Context, projectKey string) bool {
	jc, ok := jobCallerFromCtx(c)
	if !ok {
		return true
	}
	asking, ok := s.jobs.Get(jc.JobID)
	if !ok {
		writeError(c, http.StatusForbidden, "job credential may not move a stage flow",
			"the credential's job "+jc.JobID+" is gone")
		return false
	}
	key := strings.TrimSpace(projectKey)
	if key == "" || key != asking.ProjectKey {
		writeError(c, http.StatusForbidden, "job credential may not move another project's stage flow",
			"the flow belongs to project "+key+", this credential belongs to "+asking.ProjectKey)
		return false
	}
	return true
}

// stageJobItem is stageJobProject for a route that names the work item: the item's project
// is what the credential is checked against.
func (s *Server) stageJobItem(c *rux.Context, workItemID string) bool {
	if _, ok := jobCallerFromCtx(c); !ok {
		return true
	}
	it, found, err := s.work.Store().GetWorkItem(workItemID)
	if err != nil || !found {
		return true // the handler answers the 404 itself
	}
	return s.stageJobProject(c, it.ProjectKey)
}

func writeStageError(c *rux.Context, err error, what string) {
	switch {
	case errors.Is(err, jobstore.ErrStageNotFound), errors.Is(err, jobstore.ErrWorkItemNotFound):
		writeError(c, http.StatusNotFound, what+" failed", err.Error())
	case errors.Is(err, jobstore.ErrStageActive), errors.Is(err, jobstore.ErrStageClosed), errors.Is(err, stage.ErrMoved):
		writeError(c, http.StatusConflict, what+" failed", err.Error())
	default:
		writeWorkError(c, err, what)
	}
}

type stageStartReq struct {
	Issue string `json:"issue"`
	// Template is a built-in template name: feature (default) | small.
	Template  string `json:"template"`
	Project   string `json:"project"`
	Title     string `json:"title"`
	SessionID string `json:"session_id"`
}

// sessionOf is the session a start binds as current: the body's, else the registered live
// session the request itself names.
func (s *Server) stageSession(c *rux.Context, bodySID string) string {
	if sid := strings.TrimSpace(bodySID); sid != "" {
		return sid
	}
	if w := s.seatWriter(c); strings.HasPrefix(w, jobstore.SeatBySessionPrefix) {
		return strings.TrimPrefix(w, jobstore.SeatBySessionPrefix)
	}
	return ""
}

// GET /v1/work-items/stages?project=
//
// Every flow in progress (project ? narrows it), without gates: a list view does not run
// git once per row.
func (s *Server) handleListStages(c *rux.Context) {
	svc, ok := s.stageReady(c)
	if !ok {
		return
	}
	project := strings.TrimSpace(c.Query("project"))
	if project != "" && !s.requireProjectScope(c, project) {
		return
	}
	list, err := svc.ListOpen(project)
	if err != nil {
		writeStageError(c, err, "list stages")
		return
	}
	c.JSON(http.StatusOK, map[string]any{"stages": list})
}

// GET /v1/work-items/{id}/stage
func (s *Server) handleGetStage(c *rux.Context) {
	svc, ok := s.stageReady(c)
	if !ok {
		return
	}
	v, err := svc.Status(c.Param("id"))
	if err != nil {
		writeStageError(c, err, "get stage")
		return
	}
	c.JSON(http.StatusOK, v)
}

// POST /v1/work-items/{id}/stage {issue?, template?, session_id?}
func (s *Server) handleStartStage(c *rux.Context) {
	svc, ok := s.stageReady(c)
	if !ok {
		return
	}
	if !s.stageJobItem(c, c.Param("id")) {
		return
	}
	var body stageStartReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	sid := s.stageSession(c, body.SessionID)
	if sid != "" && !s.requireResourceScope(c, "session", sid) {
		return
	}
	v, err := svc.Start(stage.StartInput{
		Issue: body.Issue, WorkItemID: c.Param("id"), Template: body.Template, SessionID: sid, By: s.stageBy(c),
	})
	if err != nil {
		writeStageError(c, err, "start stage")
		return
	}
	c.JSON(http.StatusCreated, v)
}

// POST /v1/work-items/stage {issue, template?, project?, title?, session_id?}
func (s *Server) handleStartStageByIssue(c *rux.Context) {
	svc, ok := s.stageReady(c)
	if !ok {
		return
	}
	var body stageStartReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if !s.requireProjectScope(c, body.Project) {
		return
	}
	if !s.stageJobProject(c, body.Project) {
		return
	}
	if strings.TrimSpace(body.Issue) == "" {
		writeError(c, http.StatusBadRequest, "start stage failed", "issue is required")
		return
	}
	// An item already linked to the issue may belong to another project: it must be inside
	// the caller's scope too, not only the project the body names.
	if linked, err := s.work.Store().WorkItemsLinkedToIssue(strings.TrimSpace(body.Issue)); err == nil && len(linked) > 0 {
		if !s.requireResourceScope(c, "work_item", linked[0].ID) {
			return
		}
	}
	sid := s.stageSession(c, body.SessionID)
	if sid != "" && !s.requireResourceScope(c, "session", sid) {
		return
	}
	v, err := svc.Start(stage.StartInput{
		Issue: body.Issue, Template: body.Template, ProjectKey: body.Project, Title: body.Title, SessionID: sid, By: s.stageBy(c),
	})
	if err != nil {
		writeStageError(c, err, "start stage")
		return
	}
	c.JSON(http.StatusCreated, v)
}

// POST /v1/work-items/{id}/stage/done
func (s *Server) handleStageDone(c *rux.Context) {
	svc, ok := s.stageReady(c)
	if !ok {
		return
	}
	if !s.stageJobItem(c, c.Param("id")) {
		return
	}
	res, err := svc.Done(c.Param("id"), s.stageBy(c))
	if err != nil {
		writeStageError(c, err, "stage done")
		return
	}
	c.JSON(http.StatusOK, res)
}

type stageReasonReq struct {
	Reason string `json:"reason"`
}

// POST /v1/work-items/{id}/stage/back {reason}
func (s *Server) handleStageBack(c *rux.Context) {
	svc, ok := s.stageReady(c)
	if !ok {
		return
	}
	if !s.stageJobItem(c, c.Param("id")) {
		return
	}
	var body stageReasonReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	v, err := svc.Back(c.Param("id"), body.Reason, s.stageBy(c))
	if err != nil {
		writeStageError(c, err, "stage back")
		return
	}
	c.JSON(http.StatusOK, v)
}

// POST /v1/work-items/{id}/stage/blocked {reason}
func (s *Server) handleStageBlocked(c *rux.Context) {
	svc, ok := s.stageReady(c)
	if !ok {
		return
	}
	if !s.stageJobItem(c, c.Param("id")) {
		return
	}
	var body stageReasonReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	v, err := svc.Blocked(c.Param("id"), body.Reason, s.stageBy(c))
	if err != nil {
		writeStageError(c, err, "stage blocked")
		return
	}
	c.JSON(http.StatusOK, v)
}

// POST /v1/work-items/{id}/stage/drop {reason?}
func (s *Server) handleStageDrop(c *rux.Context) {
	svc, ok := s.stageReady(c)
	if !ok {
		return
	}
	var body stageReasonReq
	if c.Req.ContentLength != 0 {
		if err := c.BindJSON(&body); err != nil {
			writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
			return
		}
	}
	v, err := svc.Drop(c.Param("id"), body.Reason, s.stageBy(c))
	if err != nil {
		writeStageError(c, err, "stage drop")
		return
	}
	c.JSON(http.StatusOK, v)
}

// POST /v1/work-items/{id}/stage/approve
//
// A person's approval of the current step: it passes the review gate and waives a gate this
// server cannot judge. Only a person's own credential is accepted.
func (s *Server) handleStageApprove(c *rux.Context) {
	svc, ok := s.stageReady(c)
	if !ok {
		return
	}
	if callerKindFromCtx(c) != callerKindUser {
		writeError(c, http.StatusForbidden, "approve not permitted for this caller", "only a person's credential can approve a stage")
		return
	}
	if w := s.seatWriter(c); w != jobstore.SeatByUser {
		writeError(c, http.StatusForbidden, "approve not permitted for this caller", "a session cannot approve its own stage: a person approves from the web console or outside the session")
		return
	}
	res, err := svc.Approve(c.Param("id"), workBy(c))
	if err != nil {
		writeStageError(c, err, "stage approve")
		return
	}
	c.JSON(http.StatusOK, res)
}

// withLinkRoles fills the role of each link (the plain link listing does not carry it).
func (s *Server) withLinkRoles(id string, links []jobstore.WorkLink) []jobstore.WorkLink {
	roles, err := s.work.Store().ListWorkLinksRole(id, "")
	if err != nil {
		return links
	}
	byKey := make(map[string]string, len(roles))
	for _, r := range roles {
		if r.Role != "" {
			byKey[r.Kind+"\x00"+r.Ref] = r.Role
		}
	}
	for i := range links {
		links[i].Role = byKey[links[i].Kind+"\x00"+links[i].Ref]
	}
	return links
}

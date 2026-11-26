package httpapi

import (
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/jobstore"
)

// The autonomy level and the consistency report of the stage flow (plan F4):
//
//	POST /v1/work-items/{id}/stage/level    {level: record|remind}, a person's alone
//	GET  /v1/work-items/{id}/stage/report   the "would do" record of one flow against what happened
//	GET  /v1/work-items/stages/report       the same over a project (?project=) or every flow

type stageLevelReq struct {
	Level string `json:"level"`
}

// POST /v1/work-items/{id}/stage/level
//
// Setting how much the flow may do for itself is a person's decision, like an approval: not a
// job credential (the route declares no job grant), not a worker, and not a request that
// names a registered live session (that is the agent asking to widen its own leash).
func (s *Server) handleStageLevel(c *rux.Context) {
	svc, ok := s.stageReady(c)
	if !ok {
		return
	}
	if callerKindFromCtx(c) != callerKindUser {
		writeError(c, http.StatusForbidden, "stage level not permitted for this caller", "only a person's credential can set the autonomy level of a stage flow")
		return
	}
	if w := s.seatWriter(c); w != jobstore.SeatByUser {
		writeError(c, http.StatusForbidden, "stage level not permitted for this caller", "a session cannot set its own flow's autonomy level: a person sets it from the web console or outside the session")
		return
	}
	var body stageLevelReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	v, err := svc.SetLevel(c.Param("id"), body.Level, workBy(c))
	if err != nil {
		writeStageError(c, err, "stage level")
		return
	}
	c.JSON(http.StatusOK, v)
}

// GET /v1/work-items/{id}/stage/report
func (s *Server) handleStageReport(c *rux.Context) {
	svc, ok := s.stageReady(c)
	if !ok {
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if _, found, err := s.work.Store().GetWorkItem(id); err != nil {
		writeStageError(c, err, "stage report")
		return
	} else if !found {
		writeStageError(c, jobstore.ErrWorkItemNotFound, "stage report")
		return
	}
	rep, err := svc.Report(id, "")
	if err != nil {
		writeStageError(c, err, "stage report")
		return
	}
	c.JSON(http.StatusOK, rep)
}

// GET /v1/work-items/stages/report?project=
//
// Every flow of the project (the caller's project scope applies; with no ?project= a caller
// under a ceiling gets its projects only, and the answer covers the flows of those).
func (s *Server) handleStagesReport(c *rux.Context) {
	svc, ok := s.stageReady(c)
	if !ok {
		return
	}
	project := strings.TrimSpace(c.Query("project"))
	if project != "" && !s.requireProjectScope(c, project) {
		return
	}
	if project == "" && s.ceilingActive(c) {
		writeError(c, http.StatusBadRequest, "stage report failed", "project is required for a caller whose access is limited to some projects")
		return
	}
	rep, err := svc.Report("", project)
	if err != nil {
		writeStageError(c, err, "stage report")
		return
	}
	c.JSON(http.StatusOK, rep)
}

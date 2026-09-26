package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/sessionrelay"
	"github.com/inhere/gofer/internal/workbench"
)

type patchWorkbenchThreadReq struct {
	Title  *string `json:"title"`
	Seen   *bool   `json:"seen"`
	Pinned *bool   `json:"pinned"`
}

type workbenchTurnReq struct {
	Text string `json:"text"`
}

type putWorkbenchLayoutReq struct {
	Version *int64          `json:"version"`
	Body    json.RawMessage `json:"body"`
}

type workbenchLayoutConflictResp struct {
	Error   string          `json:"error"`
	Detail  string          `json:"detail,omitempty"`
	Version int64           `json:"version"`
	Body    json.RawMessage `json:"body"`
}

func (s *Server) handleGetWorkbenchLayout(c *rux.Context) {
	if s.workbench == nil {
		writeError(c, http.StatusServiceUnavailable, "workbench unavailable", "job metadata service is not wired")
		return
	}
	layout, err := s.workbench.GetLayout(callerFromCtx(c))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get workbench layout failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, layout)
}

func (s *Server) handlePutWorkbenchLayout(c *rux.Context) {
	if !workbenchUserCaller(c) {
		writeError(c, http.StatusForbidden, "workbench mutation requires a user caller", "worker and job credentials are read-only on workbench layout")
		return
	}
	if s.workbench == nil {
		writeError(c, http.StatusServiceUnavailable, "workbench unavailable", "job metadata service is not wired")
		return
	}
	var body putWorkbenchLayoutReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if body.Version == nil || len(body.Body) == 0 {
		writeError(c, http.StatusBadRequest, "invalid request body", "version and body are required")
		return
	}
	layout, err := s.workbench.PutLayout(callerFromCtx(c), *body.Version, body.Body)
	if err == nil {
		c.JSON(http.StatusOK, layout)
		return
	}
	var conflict *workbench.LayoutVersionConflict
	switch {
	case errors.As(err, &conflict):
		c.JSON(http.StatusConflict, workbenchLayoutConflictResp{
			Error: "layout version conflict", Detail: err.Error(),
			Version: conflict.Current.Version, Body: conflict.Current.Body,
		})
	case errors.Is(err, workbench.ErrLayoutTooLarge):
		writeError(c, http.StatusRequestEntityTooLarge, "layout too large", err.Error())
	case errors.Is(err, workbench.ErrInvalidLayout):
		writeError(c, http.StatusBadRequest, "invalid workbench layout", err.Error())
	case errors.Is(err, workbench.ErrUnavailable):
		writeError(c, http.StatusServiceUnavailable, "workbench unavailable", err.Error())
	default:
		writeError(c, http.StatusInternalServerError, "save workbench layout failed", err.Error())
	}
}

func (s *Server) handleListWorkbenchThreads(c *rux.Context) {
	if s.workbench == nil {
		writeError(c, http.StatusServiceUnavailable, "workbench unavailable", "job metadata service is not wired")
		return
	}
	var since int64
	if raw := strings.TrimSpace(c.Query("since")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeError(c, http.StatusBadRequest, "invalid since", "since must be a non-negative unix timestamp")
			return
		}
		since = parsed
	}
	status := workbench.Status(strings.TrimSpace(c.Query("status")))
	if status != "" && !validWorkbenchStatus(status) {
		writeError(c, http.StatusBadRequest, "invalid status", "status must be blocked|working|review|done|idle")
		return
	}
	result, err := s.workbench.List(callerFromCtx(c), workbench.Query{
		Project: strings.TrimSpace(c.Query("project")),
		Status:  status,
		Q:       c.Query("q"),
		Since:   since,
	})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list workbench threads failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) handlePatchWorkbenchThread(c *rux.Context) {
	if !workbenchUserCaller(c) {
		writeError(c, http.StatusForbidden, "workbench mutation requires a user caller", "worker and job credentials are read-only on workbench threads")
		return
	}
	if s.workbench == nil {
		writeError(c, http.StatusServiceUnavailable, "workbench unavailable", "job metadata service is not wired")
		return
	}
	var body patchWorkbenchThreadReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if body.Title == nil && body.Seen == nil && body.Pinned == nil {
		writeError(c, http.StatusBadRequest, "empty patch", "at least one of title, seen or pinned is required")
		return
	}
	pref, err := s.workbench.Patch(callerFromCtx(c), c.Param("id"), workbench.PatchInput{
		Title: body.Title, Seen: body.Seen, Pinned: body.Pinned,
	})
	if err != nil {
		writeError(c, workbenchHTTPStatus(err), "patch workbench thread failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"thread_id": pref.ThreadID,
		"title":     pref.Title,
		"seen_at":   pref.SeenAt,
		"pinned":    pref.Pinned,
	})
}

func (s *Server) handleSeenAllWorkbenchThreads(c *rux.Context) {
	if !workbenchUserCaller(c) {
		writeError(c, http.StatusForbidden, "workbench mutation requires a user caller", "worker and job credentials are read-only on workbench threads")
		return
	}
	if s.workbench == nil {
		writeError(c, http.StatusServiceUnavailable, "workbench unavailable", "job metadata service is not wired")
		return
	}
	baseline, err := s.workbench.SeenAll(callerFromCtx(c))
	if err != nil {
		if errors.Is(err, workbench.ErrUnavailable) {
			writeError(c, http.StatusServiceUnavailable, "workbench unavailable", err.Error())
			return
		}
		writeError(c, http.StatusInternalServerError, "mark workbench threads seen failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]int64{"seen_baseline": baseline})
}

func (s *Server) handleWorkbenchTurn(c *rux.Context) {
	if !workbenchUserCaller(c) {
		writeError(c, http.StatusForbidden, "workbench turn requires a user caller", "worker and job credentials may not continue a thread")
		return
	}
	if s.workbench == nil {
		writeError(c, http.StatusServiceUnavailable, "workbench unavailable", "job metadata service is not wired")
		return
	}
	threadID := c.Param("id")
	kind, sessionID, err := workbench.ParseThreadID(threadID)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid thread id", err.Error())
		return
	}
	if kind == workbench.KindRelay && !s.sessionMayAnswer(c, sessionID, "reply to") {
		return
	}
	var body workbenchTurnReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	result, err := s.workbench.Turn(callerFromCtx(c), threadID, body.Text)
	if err != nil {
		writeError(c, workbenchHTTPStatus(err), "continue workbench thread failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, result)
}

func workbenchUserCaller(c *rux.Context) bool {
	return callerKindFromCtx(c) == callerKindUser
}

func validWorkbenchStatus(status workbench.Status) bool {
	switch status {
	case workbench.StatusBlocked, workbench.StatusWorking, workbench.StatusReview, workbench.StatusDone, workbench.StatusIdle:
		return true
	default:
		return false
	}
}

func workbenchHTTPStatus(err error) int {
	switch {
	case errors.Is(err, workbench.ErrInvalidThreadID), errors.Is(err, workbench.ErrEmptyTurn), errors.Is(err, job.ErrInvalidRequest):
		return http.StatusBadRequest
	case errors.Is(err, workbench.ErrUnknownThread), errors.Is(err, job.ErrUnknownJob), errors.Is(err, sessionrelay.ErrUnknownSession):
		return http.StatusNotFound
	case errors.Is(err, workbench.ErrNotResumable), errors.Is(err, job.ErrJobNotTerminal), errors.Is(err, sessionrelay.ErrNoOpenTurn), errors.Is(err, sessionrelay.ErrRelayOff):
		return http.StatusConflict
	case errors.Is(err, workbench.ErrUnavailable):
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadRequest
	}
}

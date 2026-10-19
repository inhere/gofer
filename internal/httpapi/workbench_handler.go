package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
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
	// The thread list is the FILTER form of job.read.others (design §1 / plan T1.5c):
	// without the permission a user-class caller sees only the threads its own jobs stand
	// for. A worker token and a job credential keep the whole list, as before the
	// migration (their own tables admitted them).
	restricted := !s.mayListAllJobs(c)
	caller := callerFromCtx(c)
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
	detail := strings.TrimSpace(c.Query("detail"))
	if detail != "" && detail != "full" && detail != "summary" {
		writeError(c, http.StatusBadRequest, "invalid detail", "detail must be full|summary")
		return
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
		// all=1 lifts the internal-job filter, like the job list does. (The projection
		// still only builds threads from interactive / session jobs, so a one-shot
		// messenger job never becomes a thread; the switch matters for session-bound ones.)
		IncludeInternal: c.Query("all") == "1" || c.Query("all") == "true",
	})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list workbench threads failed", err.Error())
		return
	}
	if restricted {
		result = s.filterWorkbenchThreads(result, caller)
	}
	result = s.scopeWorkbench(c, result)
	if detail == "summary" {
		result = result.Summary()
	}
	c.JSON(http.StatusOK, result)
}

// filterWorkbenchThreads narrows a workbench listing to the threads caller owns: the ones
// whose job (an agent / one-shot thread) or relay session carries its caller id. The
// attention list follows — an entry pointing at a thread that is no longer listed would
// offer an action on somebody else's work. A thread this server cannot resolve is nobody's
// and is dropped, which fails closed instead of leaking it.
func (s *Server) filterWorkbenchThreads(res workbench.Response, caller string) workbench.Response {
	kept := make(map[string]bool, len(res.Projects))
	out := workbench.Response{Since: res.Since, Projects: make([]workbench.ProjectGroup, 0, len(res.Projects))}
	total := 0
	for _, group := range res.Projects {
		threads := make([]workbench.Thread, 0, len(group.Threads))
		for _, th := range group.Threads {
			if owner, ok := s.workbenchThreadOwnerOf(th); !ok || owner != caller {
				continue
			}
			kept[th.ID] = true
			threads = append(threads, th)
		}
		if len(threads) == 0 {
			continue
		}
		group.Threads = threads
		total += len(threads)
		out.Projects = append(out.Projects, group)
	}
	for _, item := range res.Attention {
		if kept[item.ThreadID] {
			out.Attention = append(out.Attention, item)
		}
	}
	out.Total = total
	return out
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
	kind, rawID, err := workbench.ParseThreadID(threadID)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid thread id", err.Error())
		return
	}
	// The thread's TARGET decides the permission (design §1「说话入口按目标分流」): a relay
	// thread is speech into a terminal session (session.reply.others), an agent / one-shot
	// thread is 对会话 job 说话 (job.manage.others). Continuing an AGENT thread continues the
	// thread with a new job when it is not already waiting for input, so — like resume — that
	// branch also checks job.submit (design §1: workbench agent 线程)；a one-shot thread cannot
	// be continued at all and creates nothing. A thread nobody owns here (an id the projection
	// cannot resolve) is answered by the service, which is what the routes did before the
	// migration.
	if owner, ok := s.workbenchThreadOwner(kind, rawID); ok {
		if kind == workbench.KindRelay {
			if !s.mayReplyToSession(c, owner) {
				s.denySessionAction(c, authz.PermSessionReplyOthers)
				return
			}
		} else if !s.mayManageOwnedRow(c, owner, false) {
			s.denySessionAction(c, authz.PermJobManageOthers)
			return
		}
		if kind == workbench.KindAgent && !s.can(c, authz.PermJobSubmit) {
			s.denySessionAction(c, authz.PermJobSubmit)
			return
		}
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

// workbenchThreadOwner resolves the caller a workbench thread belongs to, from the kind and
// raw id of its thread id: the caller id of the terminal session a relay thread stands for,
// or of the job(s) an agent / one-shot thread continues. ok=false means the thread cannot
// be resolved here — an id the projection knows nothing about — and the route then keeps
// its own answer (404 from the service), exactly as it did before the migration.
func (s *Server) workbenchThreadOwner(kind workbench.ThreadKind, rawID string) (string, bool) {
	switch kind {
	case workbench.KindRelay:
		if s.relay == nil || rawID == "" {
			return "", false
		}
		a, err := s.relay.Session(rawID)
		if err != nil {
			return "", false
		}
		return a.CallerID, true
	case workbench.KindJob:
		if res, ok := s.jobs.Get(rawID); ok {
			return res.CallerID, true
		}
	case workbench.KindAgent:
		// An agent thread groups the jobs of one session; they all carry the caller that
		// registered it, so the first row answers for the thread (the projection's own
		// lookup for a thread, jobstore.ListQuery{Session: …}).
		rows, err := s.jobs.Meta().ListJobs(jobstore.ListQuery{Session: rawID, Limit: 1})
		if err != nil || len(rows) == 0 {
			return "", false
		}
		return rows[0].CallerID, true
	}
	return "", false
}

// workbenchThreadOwnerOf is workbenchThreadOwner for a thread of a listing.
func (s *Server) workbenchThreadOwnerOf(th workbench.Thread) (string, bool) {
	switch th.Kind {
	case workbench.KindRelay:
		if th.Relay == nil {
			return "", false
		}
		return s.workbenchThreadOwner(workbench.KindRelay, th.Relay.SessionID)
	case workbench.KindAgent, workbench.KindJob:
		return s.workbenchThreadOwner(th.Kind, th.LatestJobID)
	}
	return "", false
}

// workbenchThreadOwnerByID is workbenchThreadOwner for the routes that carry the thread id
// in their path and hand it to the service unchanged.
func (s *Server) workbenchThreadOwnerByID(threadID string) (string, bool) {
	kind, rawID, err := workbench.ParseThreadID(threadID)
	if err != nil {
		return "", false
	}
	return s.workbenchThreadOwner(kind, rawID)
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
	case errors.Is(err, workbench.ErrInvalidThreadID), errors.Is(err, workbench.ErrEmptyTurn), errors.Is(err, workbench.ErrInvalidReview), errors.Is(err, job.ErrInvalidRequest):
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

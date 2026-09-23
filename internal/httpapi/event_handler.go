package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// handleListEvents returns a job's append-only lifecycle events (E13) in seq
// order. An unknown job id is a 404 (consistent with handleGetJob). The optional
// ?since=<seq> returns only events strictly after that cursor (incremental poll;
// a non-numeric/absent value lists from the start). The list is always a non-nil
// array, so an empty result serialises as {"events":[]}.
func (s *Server) handleListEvents(c *rux.Context) {
	id := c.Param("id")
	if _, ok := s.jobs.Get(id); !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
		return
	}
	// since 非数值 -> 0 -> 不过滤（仿 list/stream 的容错）。
	since, _ := strconv.ParseInt(c.Query("since"), 10, 64)
	events, err := s.jobs.ListJobEvents(id, since)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list events failed", err.Error())
		return
	}
	if events == nil {
		events = []jobstore.JobEvent{}
	}
	c.JSON(http.StatusOK, map[string]any{"events": events})
}

// Plan event-area paging (LEAD-02). The stream is read newest-first because that is how
// a reader scans it; 50 covers a busy plan's recent history in one request, 200 is the
// ceiling a client cannot talk the server past.
const (
	planEventsDefaultLimit = 50
	planEventsMaxLimit     = 200
)

// handleListPlanEvents returns the events recorded on a PLAN's scope
// (`plan:<id>` — plan.blocked / plan.completed / plan.leader_* / comment.* …), newest
// first. LEAD-02 closes the S4 leftover where those events existed with no way to read
// them. Unlike the job timeline's forward `?since=` cursor, this one pages BACKWARDS:
// `?limit=` (default 50, max 200) and `?before=<seq>` (strictly older events).
//
// An unknown plan is a 404 (a plan's stream only exists alongside the plan, and an empty
// 200 would read as "this plan has no history"). Job credentials may read it like any
// other GET — a leader must be able to see its own plan's stream.
func (s *Server) handleListPlanEvents(c *rux.Context) {
	id := c.Param("id")
	if _, ok, err := s.jobs.Meta().GetPlan(id); err != nil {
		writeError(c, http.StatusInternalServerError, "get plan failed", err.Error())
		return
	} else if !ok {
		writeError(c, http.StatusNotFound, "unknown plan", "no plan with id "+id)
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit")) // 非数值/缺省 -> 0 -> 默认条数
	if limit <= 0 {
		limit = planEventsDefaultLimit
	}
	if limit > planEventsMaxLimit {
		limit = planEventsMaxLimit
	}
	before, _ := strconv.ParseInt(c.Query("before"), 10, 64)
	events, err := s.jobs.ListScopedEventsDesc(job.PlanEventScope(id), before, limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list plan events failed", err.Error())
		return
	}
	if events == nil {
		events = []jobstore.JobEvent{}
	}
	c.JSON(http.StatusOK, map[string]any{"events": events})
}

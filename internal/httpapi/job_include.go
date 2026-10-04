package httpapi

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// Q1: GET /v1/jobs/{id}?include=a,b,c folds the detail page's side requests into one
// response. An empty include is the plain job snapshot (CLI, MCP and every older
// caller see no change). Each item fails on its own: an error lands in
// include_errors[<item>] and the response stays 200, so a slow or broken side table
// can never blank the whole page.
const (
	includeEvents      = "events"
	includeComments    = "comments"
	includeDeliveries  = "deliveries"
	includeRetries     = "retries"
	includeWakeups     = "wakeups"
	includePtySessions = "pty_sessions"
	includeArtifacts   = "artifacts"
	includeSessionJobs = "session_jobs"

	// Per-item size caps. deliveries / retries / wakeups are small by nature and are
	// returned whole.
	includeEventsLimit      = 200
	includeCommentsLimit    = 100
	includeArtifactsLimit   = 200
	includeSessionJobsLimit = 50

	includeForbidden = "forbidden"
)

var knownIncludes = map[string]bool{
	includeEvents: true, includeComments: true, includeDeliveries: true, includeRetries: true,
	includeWakeups: true, includePtySessions: true, includeArtifacts: true, includeSessionJobs: true,
}

// parseIncludes splits ?include= into a set. An unknown token is an error so a typo
// is not silently read as "that part is empty".
func parseIncludes(raw string) (map[string]bool, string) {
	out := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !knownIncludes[part] {
			return nil, part
		}
		out[part] = true
	}
	return out, ""
}

// jobDetailView is the detail response: the job snapshot, the caller-computed
// permission bits, and whichever ?include= parts were asked for.
type jobDetailView struct {
	job.JobResult
	CanAttach bool `json:"can_attach"`
	CanDelete bool `json:"can_delete"`

	Events          []jobstore.JobEvent `json:"events,omitempty"`
	EventsLastSeq   int64               `json:"events_last_seq,omitempty"`
	EventsTruncated bool                `json:"events_truncated,omitempty"`
	Comments        []commentView       `json:"comments,omitempty"`
	CommentsTotal   *int                `json:"comments_total,omitempty"`
	Deliveries      []jobstore.Delivery `json:"deliveries,omitempty"`
	Retries         []retryView         `json:"retries,omitempty"`
	Wakeups         []wakeupView        `json:"wakeups,omitempty"`
	PtySessions     []ptySessionView    `json:"pty_sessions,omitempty"`
	Artifacts       []job.ArtifactItem  `json:"artifacts,omitempty"`
	ArtifactsTotal  *int                `json:"artifacts_total,omitempty"`
	SessionJobs     []job.JobResult     `json:"session_jobs,omitempty"`
	IncludeErrors   map[string]string   `json:"include_errors,omitempty"`
}

// fillIncludes resolves the requested parts onto v. caller is the authenticated
// caller (pty visibility); before is the optional events paging cursor.
func (s *Server) fillIncludes(v *jobDetailView, res job.JobResult, caller string, want map[string]bool, before int64) {
	errs := map[string]string{}
	fail := func(name string, err error) { errs[name] = err.Error() }

	if want[includeEvents] {
		rows, err := s.jobs.ListScopedEventsDesc(res.ID, before, includeEventsLimit+1)
		if err != nil {
			fail(includeEvents, err)
		} else {
			if len(rows) > includeEventsLimit {
				v.EventsTruncated = true
				rows = rows[:includeEventsLimit]
			}
			// ListScopedEventsDesc is newest-first; the wire is seq-ascending.
			evs := make([]jobstore.JobEvent, 0, len(rows))
			for i := len(rows) - 1; i >= 0; i-- {
				evs = append(evs, rows[i])
			}
			v.Events = evs
			if n := len(evs); n > 0 {
				v.EventsLastSeq = evs[n-1].Seq
			}
		}
	}
	if want[includeComments] {
		rows, err := s.jobs.ListComments(jobstore.CommentScopeJob, res.ID)
		if err != nil {
			fail(includeComments, err)
		} else {
			total := len(rows)
			if total > includeCommentsLimit {
				rows = rows[total-includeCommentsLimit:] // keep the newest
			}
			out := make([]commentView, 0, len(rows))
			for _, cm := range rows {
				out = append(out, toCommentView(cm, nil))
			}
			v.Comments = out
			v.CommentsTotal = &total
		}
	}
	if want[includeDeliveries] {
		rows, err := s.jobs.ListDeliveriesByJob(res.ID)
		if err != nil {
			fail(includeDeliveries, err)
		} else if rows == nil {
			v.Deliveries = []jobstore.Delivery{}
		} else {
			v.Deliveries = rows
		}
	}
	if want[includeRetries] {
		rows, err := s.jobs.ListRetries(res.ID)
		if err != nil {
			fail(includeRetries, err)
		} else {
			out := make([]retryView, 0, len(rows))
			for _, rec := range rows {
				out = append(out, toRetryView(rec))
			}
			v.Retries = out
		}
	}
	if want[includeWakeups] {
		rows, err := s.jobs.ListWakeups(res.ID)
		if err != nil {
			fail(includeWakeups, err)
		} else {
			out := make([]wakeupView, 0, len(rows))
			for _, w := range rows {
				out = append(out, toWakeupView(w))
			}
			v.Wakeups = out
		}
	}
	if want[includePtySessions] {
		switch {
		case !s.callerMayAttach(caller, res):
			// Same gate as GET /jobs/{id}/pty/sessions: the field is left out, not empty.
			errs[includePtySessions] = includeForbidden
		case s.ptySessions == nil:
			v.PtySessions = []ptySessionView{}
		default:
			rows, err := s.ptySessions.ListPtySessionsByJob(res.ID)
			if err != nil {
				fail(includePtySessions, err)
			} else {
				v.PtySessions = s.ptySessionViews(rows, false)
			}
		}
	}
	if want[includeArtifacts] && job.IsTerminal(res.Status) {
		// Only a finished job inlines its manifest: for a running one the manifest means
		// scanning the result dir, which a detail poll should not pay for.
		if m, ok := s.jobs.GetArtifactManifest(res.ID); ok {
			total := len(m.Items)
			items := m.Items
			if total > includeArtifactsLimit {
				items = items[:includeArtifactsLimit]
			}
			if items == nil {
				items = []job.ArtifactItem{}
			}
			v.Artifacts = items
			v.ArtifactsTotal = &total
		}
	}
	if want[includeSessionJobs] && res.SessionID != "" {
		list, err := s.jobs.ListJobs(job.ListOpts{Session: res.SessionID, Limit: includeSessionJobsLimit})
		if err != nil {
			fail(includeSessionJobs, err)
		} else {
			sort.SliceStable(list, func(i, j int) bool { return list[i].StartedAt < list[j].StartedAt })
			if list == nil {
				list = []job.JobResult{}
			}
			v.SessionJobs = list
		}
	}
	if len(errs) > 0 {
		v.IncludeErrors = errs
	}
}

// handleGetJob returns the current snapshot of a job; an unknown id is a 404.
// ?include= (see the Q1 constants) adds the detail page's side data in the same
// response; ?before=<seq> pages the events part backwards.
func (s *Server) handleGetJob(c *rux.Context) {
	id := c.Param("id")
	want, bad := parseIncludes(c.Query("include"))
	if bad != "" {
		writeError(c, http.StatusBadRequest, "unknown include", "include must be a comma list of "+
			"events, comments, deliveries, retries, wakeups, pty_sessions, artifacts, session_jobs; got "+bad)
		return
	}
	res, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
		return
	}
	caller := callerFromCtx(c)
	view := jobDetailView{
		JobResult: res,
		// can_attach 是详情视图计算位；列表端点保持原 JobResult 数组不变。
		CanAttach: s.canAttachNow(caller, res),
		CanDelete: canDeleteJob(s.cfg, caller, res),
	}
	if len(want) > 0 {
		before, _ := strconv.ParseInt(c.Query("before"), 10, 64)
		s.fillIncludes(&view, res, caller, want, before)
	}
	c.JSON(http.StatusOK, view)
}

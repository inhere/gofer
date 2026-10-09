package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/notify"
	"github.com/inhere/gofer/internal/steward"
	"github.com/inhere/gofer/internal/work"
)

// W2b: the REST face of the steward (design §14.4). Handlers only bind / validate and
// translate errors; the rules live in internal/steward and internal/work, and the steward
// credential's boundary is the route allowlist in jobcredential.go.

// Steward returns the steward service (nil without a job store); serve feeds it config and
// runs its loop.
func (s *Server) Steward() *steward.Service { return s.steward }

func callerIsSteward(c *rux.Context) bool {
	jc, ok := jobCallerFromCtx(c)
	return ok && jc.isSteward()
}

func (s *Server) stewardReady(c *rux.Context) bool {
	if s.steward == nil || s.work == nil {
		writeError(c, http.StatusServiceUnavailable, "steward unavailable", "no job store wired on this server")
		return false
	}
	return workNotAWorker(c)
}

// stewardUserOnly admits only a person (callerKindUser): starting, stopping, asking, the
// review, merge-suggestion and memory-suggestion decisions are a person's, whatever the
// credential kind — a job (the steward cannot drive itself) and a worker transport token
// are both refused. Every caller is a person-only action, so the check is an allowlist,
// not a "not a job" denylist that a future credential kind would slip through.
func stewardUserOnly(c *rux.Context, what string) bool {
	if callerKindFromCtx(c) != callerKindUser {
		writeError(c, http.StatusForbidden, "job credential may not "+what, "only a person can "+what)
		return false
	}
	return true
}

func writeStewardError(c *rux.Context, err error, what string) {
	switch {
	case errors.Is(err, steward.ErrDisabled), errors.Is(err, steward.ErrNoAgent):
		writeError(c, http.StatusConflict, what+" failed", err.Error())
	case errors.Is(err, steward.ErrBusy):
		writeError(c, http.StatusConflict, what+" failed", err.Error())
	case errors.Is(err, steward.ErrEmpty), errors.Is(err, jobstore.ErrWorkInvalid):
		writeError(c, http.StatusBadRequest, what+" failed", err.Error())
	case errors.Is(err, jobstore.ErrStewardNoReview):
		writeError(c, http.StatusConflict, what+" failed", err.Error())
	default:
		writeError(c, http.StatusInternalServerError, what+" failed", err.Error())
	}
}

// stewardSettingsView is the effective `steward:` block as the console shows it.
type stewardSettingsView struct {
	Enabled          bool   `json:"enabled"`
	Agent            string `json:"agent"`
	Project          string `json:"project"`
	ReviewTime       string `json:"review_time"`
	IdleEndMin       int    `json:"idle_end_min"`
	ReviewMaxItems   int    `json:"review_max_items"`
	EventWake        bool   `json:"event_wake"`
	EventThrottleMin int    `json:"event_throttle_min"`
	// ReviewTimeExplicit is true when review_time was set (false = derived from the digest).
	ReviewTimeExplicit bool `json:"review_time_explicit"`
}

func stewardSettingsOf(c config.StewardConfig, w config.WorkConfig) stewardSettingsView {
	h, m := c.ReviewClock(w)
	return stewardSettingsView{
		Enabled: c.Enabled, Agent: c.AgentName(), Project: c.ProjectKey(), ReviewTime: twoDigits(h) + ":" + twoDigits(m),
		IdleEndMin: int(c.IdleEnd().Minutes()), ReviewMaxItems: c.MaxReviewItems(), EventWake: c.EventWake,
		EventThrottleMin: int(c.EventThrottle().Minutes()), ReviewTimeExplicit: strings.TrimSpace(c.ReviewTime) != "",
	}
}

func (s *Server) liveStewardSettings() stewardSettingsView {
	var c config.StewardConfig
	var w config.WorkConfig
	if s.projects != nil {
		if live := s.projects.Config(); live != nil {
			c, w = live.Steward, live.Work
		}
	}
	return stewardSettingsOf(c, w)
}

// GET /v1/steward — the status plus the effective settings.
func (s *Server) handleStewardStatus(c *rux.Context) {
	if !s.stewardReady(c) {
		return
	}
	out := map[string]any{"status": s.steward.Status(), "settings": s.liveStewardSettings()}
	// OBS-13: digest / steward on but nobody subscribes to work.digest.
	if s.projects != nil {
		if w := notify.DigestNoSubscriberWarning(s.projects.Config()); w != "" {
			out["warnings"] = []string{w}
		}
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/steward/start
func (s *Server) handleStewardStart(c *rux.Context) {
	if !s.stewardReady(c) || !stewardUserOnly(c, "start the steward") {
		return
	}
	res, err := s.steward.Start(c.Req.Context())
	if err != nil {
		writeStewardError(c, err, "start steward")
		return
	}
	c.JSON(http.StatusOK, map[string]any{"job_id": res.JobID, "started": res.Started, "status": s.steward.Status()})
}

// POST /v1/steward/stop
func (s *Server) handleStewardStop(c *rux.Context) {
	if !s.stewardReady(c) || !stewardUserOnly(c, "stop the steward") {
		return
	}
	stopped, err := s.steward.Stop()
	if err != nil {
		writeStewardError(c, err, "stop steward")
		return
	}
	c.JSON(http.StatusOK, map[string]any{"stopped": stopped, "status": s.steward.Status()})
}

// POST /v1/steward/restart — end the session and open a fresh one from the prime.
func (s *Server) handleStewardRestart(c *rux.Context) {
	if !s.stewardReady(c) || !stewardUserOnly(c, "restart the steward") {
		return
	}
	res, err := s.steward.Restart(c.Req.Context())
	if err != nil {
		writeStewardError(c, err, "restart steward")
		return
	}
	c.JSON(http.StatusOK, map[string]any{"job_id": res.JobID, "started": res.Started, "status": s.steward.Status()})
}

// POST /v1/steward/ask {text}
func (s *Server) handleStewardAsk(c *rux.Context) {
	if !s.stewardReady(c) || !stewardUserOnly(c, "ask the steward") {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	res, err := s.steward.Ask(c.Req.Context(), body.Text, workBy(c))
	if err != nil {
		writeStewardError(c, err, "ask steward")
		return
	}
	c.JSON(http.StatusOK, map[string]any{"job_id": res.JobID, "started": res.Started})
}

// POST /v1/steward/review {force?}
func (s *Server) handleStewardReview(c *rux.Context) {
	if !s.stewardReady(c) || !stewardUserOnly(c, "run a steward review") {
		return
	}
	var body struct {
		Force bool `json:"force"`
	}
	_ = c.BindJSON(&body)
	res, err := s.steward.RunReview(c.Req.Context(), steward.ReviewOpts{Trigger: steward.TriggerManual, Force: body.Force})
	if err != nil {
		writeStewardError(c, err, "steward review")
		return
	}
	c.JSON(http.StatusOK, res)
}

// GET /v1/steward/notes[?version=N | ?history=1]
func (s *Server) handleStewardNotesGet(c *rux.Context) {
	if !s.stewardReady(c) {
		return
	}
	if queryBool(c, "history") {
		h, err := s.steward.NotesHistory()
		if err != nil {
			writeStewardError(c, err, "list steward notes")
			return
		}
		c.JSON(http.StatusOK, map[string]any{"history": h})
		return
	}
	version, _ := strconv.Atoi(c.Query("version"))
	n, ok, err := s.steward.GetNotes(version)
	if err != nil {
		writeStewardError(c, err, "get steward notes")
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "steward notes version not found", "no such version")
		return
	}
	info, _ := s.steward.NotesStatus()
	c.JSON(http.StatusOK, map[string]any{"notes": n, "info": info})
}

// PUT /v1/steward/notes {body, version} — a new version when version is the current one.
func (s *Server) handleStewardNotesPut(c *rux.Context) {
	if !s.stewardReady(c) {
		return
	}
	var body struct {
		Body    string `json:"body"`
		Version int    `json:"version"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	n, err := s.steward.SetNotes(body.Body, workBy(c), body.Version)
	switch {
	case errors.Is(err, steward.ErrNotesConflict):
		cur, _, _ := s.steward.GetNotes(0)
		c.JSON(http.StatusConflict, map[string]any{"error": "steward notes changed", "detail": err.Error(), "current": cur})
		return
	case errors.Is(err, steward.ErrNotesTooLarge):
		writeError(c, http.StatusRequestEntityTooLarge, "steward notes too large", err.Error())
		return
	case err != nil:
		writeStewardError(c, err, "set steward notes")
		return
	}
	info, _ := s.steward.NotesStatus()
	c.JSON(http.StatusOK, map[string]any{"notes": n, "info": info})
}

// POST /v1/steward/review-summary {text} — the review's point of view (the digest's comment).
func (s *Server) handleStewardReviewSummary(c *rux.Context) {
	if !s.stewardReady(c) {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	r, err := s.steward.SetReviewSummary(body.Text)
	if err != nil {
		writeStewardError(c, err, "set review summary")
		return
	}
	c.JSON(http.StatusOK, map[string]any{"review": r})
}

// stewardPatchWorkItem is PATCH /v1/work-items/{id} for the steward credential: the work
// service's steward rules decide what applies (no final status, a person's status wins).
func (s *Server) stewardPatchWorkItem(c *rux.Context, id string, p jobstore.WorkItemPatch, rev int64) {
	if p.StatusSource != nil {
		writeError(c, http.StatusForbidden, "steward credential may not set the status source", "the status source is a person's; the steward's own status is recorded as a report")
		return
	}
	res, err := s.work.StewardUpdate(id, p, rev, workBy(c))
	if err != nil {
		switch {
		case errors.Is(err, work.ErrStewardFinalStatus):
			writeError(c, http.StatusForbidden, "steward credential may not end a work item", err.Error())
		case errors.Is(err, jobstore.ErrWorkItemConflict):
			if d, derr := s.work.Detail(id, 200); derr == nil {
				c.JSON(http.StatusConflict, map[string]any{"error": "update work item failed", "detail": err.Error(), "current": d})
				return
			}
			writeWorkError(c, err, "update work item")
		default:
			writeWorkError(c, err, "update work item")
		}
		return
	}
	d, err := s.work.Detail(id, 200)
	if err != nil {
		writeWorkError(c, err, "get work item")
		return
	}
	for i := range d.Sessions {
		d.Sessions[i].Runner = s.resolveRunnerName(d.Sessions[i].Runner)
	}
	// The detail, plus what the steward asked for that was deliberately not applied.
	d.Notes = res.Notes
	c.JSON(http.StatusOK, d)
}

// GET /v1/work-items/merge-suggestions — the steward's recorded "these look like one thing".
func (s *Server) handleListMergeSuggestions(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	list, err := s.work.MergeSuggestions()
	if err != nil {
		writeWorkError(c, err, "list merge suggestions")
		return
	}
	c.JSON(http.StatusOK, map[string]any{"suggestions": list})
}

// POST /v1/work-items/{id}/merge-suggestions {source_id, reason?} — record that source should
// merge into {id}. It merges nothing.
func (s *Server) handleAddMergeSuggestion(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	var body struct {
		SourceID string `json:"source_id"`
		Reason   string `json:"reason"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	sg, stored, err := s.work.SuggestMerge(c.Param("id"), body.SourceID, body.Reason, workBy(c))
	if err != nil {
		writeWorkError(c, err, "suggest merge")
		return
	}
	c.JSON(http.StatusOK, map[string]any{"suggestion": sg, "recorded": stored})
}

func mergeSuggestionID(c *rux.Context) (int64, bool) {
	n, err := strconv.ParseInt(c.Param("n"), 10, 64)
	if err != nil || n <= 0 {
		writeError(c, http.StatusBadRequest, "invalid suggestion id", c.Param("n"))
		return 0, false
	}
	return n, true
}

func writeMergeSuggestionError(c *rux.Context, err error, what string) {
	switch {
	case errors.Is(err, jobstore.ErrMergeSuggestionNotFound):
		writeError(c, http.StatusNotFound, what+" failed", err.Error())
	case errors.Is(err, work.ErrMergeSuggestionResolved):
		writeError(c, http.StatusConflict, what+" failed", err.Error())
	default:
		writeWorkError(c, err, what)
	}
}

// POST /v1/work-items/merge-suggestions/{n}/accept — a person's yes: performs the merge.
func (s *Server) handleAcceptMergeSuggestion(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) || !stewardUserOnly(c, "accept a merge suggestion") {
		return
	}
	n, ok := mergeSuggestionID(c)
	if !ok {
		return
	}
	w, err := s.work.AcceptMergeSuggestion(n, workBy(c))
	if err != nil {
		writeMergeSuggestionError(c, err, "accept merge suggestion")
		return
	}
	s.respondWorkDetail(c, w.ID, http.StatusOK)
}

// POST /v1/work-items/merge-suggestions/{n}/dismiss
func (s *Server) handleDismissMergeSuggestion(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) || !stewardUserOnly(c, "dismiss a merge suggestion") {
		return
	}
	n, ok := mergeSuggestionID(c)
	if !ok {
		return
	}
	if err := s.work.DismissMergeSuggestion(n); err != nil {
		writeMergeSuggestionError(c, err, "dismiss merge suggestion")
		return
	}
	c.JSON(http.StatusOK, map[string]any{"status": "dismissed"})
}

// GET /v1/sessions/{sid}/tail[?bytes=N] — the tail of the session's transcript as plain
// text, read-only and bounded (a worker's session goes through its transcript_tail frame,
// which enforces the size limit on the worker's side too).
func (s *Server) handleSessionTail(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	n, _ := strconv.ParseInt(c.Query("bytes"), 10, 64)
	ctx := c.Req.Context()
	res, err := s.work.SessionTail(ctx, c.Param("sid"), n)
	if err != nil {
		if errors.Is(err, work.ErrSessionNotFound) {
			writeError(c, http.StatusNotFound, "get session tail failed", err.Error())
			return
		}
		writeError(c, http.StatusInternalServerError, "get session tail failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// stewardSettingsPatch is the partial body of PUT /v1/config/steward; absent fields stay.
type stewardSettingsPatch struct {
	Enabled          *bool   `json:"enabled"`
	Agent            *string `json:"agent"`
	Project          *string `json:"project"`
	ReviewTime       *string `json:"review_time"`
	IdleEndMin       *int    `json:"idle_end_min"`
	ReviewMaxItems   *int    `json:"review_max_items"`
	EventWake        *bool   `json:"event_wake"`
	EventThrottleMin *int    `json:"event_throttle_min"`
}

func (p stewardSettingsPatch) apply(w *config.StewardConfig) []string {
	var set []string
	add := func(name string) { set = append(set, name) }
	if p.Enabled != nil {
		w.Enabled = *p.Enabled
		add("enabled")
	}
	if p.Agent != nil {
		w.Agent = strings.TrimSpace(*p.Agent)
		add("agent")
	}
	if p.Project != nil {
		w.Project = strings.TrimSpace(*p.Project)
		add("project")
	}
	if p.ReviewTime != nil {
		w.ReviewTime = strings.TrimSpace(*p.ReviewTime)
		add("review_time")
	}
	if p.IdleEndMin != nil {
		w.IdleEndMin = *p.IdleEndMin
		add("idle_end_min")
	}
	if p.ReviewMaxItems != nil {
		w.ReviewMaxItems = *p.ReviewMaxItems
		add("review_max_items")
	}
	if p.EventWake != nil {
		w.EventWake = *p.EventWake
		add("event_wake")
	}
	if p.EventThrottleMin != nil {
		w.EventThrottleMin = *p.EventThrottleMin
		add("event_throttle_min")
	}
	return set
}

// PUT /v1/config/steward — a PARTIAL update of the steward: block through the config write
// transaction (admin only, validated, saved, hot-applied). Changing the agent (or switching
// the steward off) ends the running session; the next need rebuilds it.
func (s *Server) handlePutConfigSteward(c *rux.Context) {
	caller, ok := s.configWriteCaller(c)
	if !ok {
		return
	}
	cw, ok := s.configWriter(c)
	if !ok {
		return
	}
	var body stewardSettingsPatch
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	var applied []string
	err := cw.Update(func(next *config.Config) error {
		applied = body.apply(&next.Steward)
		if len(applied) == 0 {
			return &configWriteError{status: http.StatusBadRequest, msg: "empty request body", detail: "name at least one steward setting"}
		}
		if verr := validateCandidate(next); verr != nil {
			return &configWriteError{status: http.StatusBadRequest, msg: "invalid config", detail: verr.Error()}
		}
		return nil
	})
	if err != nil {
		s.writeConfigError(c, err)
		return
	}
	s.recordConfigUpdate(caller, "steward", "", applied)
	if s.steward != nil {
		s.steward.Reconcile()
	}
	c.JSON(http.StatusOK, configWriteResp{Status: "ok", Section: "steward", Reloaded: true, Fields: applied})
}

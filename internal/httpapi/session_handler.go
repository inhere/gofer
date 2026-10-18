package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/sessionrelay"
)

type sessionOutboxCursor struct {
	CreatedAt int64  `json:"created_at"`
	ID        string `json:"id"`
}

func encodeSessionOutboxCursor(m jobstore.SessionMessage) string {
	b, _ := json.Marshal(sessionOutboxCursor{CreatedAt: m.CreatedAt, ID: m.ID})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeSessionOutboxCursor(raw string) (sessionOutboxCursor, error) {
	if raw == "" {
		return sessionOutboxCursor{}, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return sessionOutboxCursor{}, fmt.Errorf("invalid before cursor: %w", err)
	}
	var c sessionOutboxCursor
	if err := json.Unmarshal(b, &c); err != nil || c.ID == "" {
		return sessionOutboxCursor{}, errors.New("invalid before cursor")
	}
	return c, nil
}

type sessionWatchView struct {
	JobID     string `json:"job_id"`
	Title     string `json:"title,omitempty"`
	Status    string `json:"status"`
	ExitCode  int    `json:"exit_code"`
	StartedAt int64  `json:"started_at,omitempty"`
	EndedAt   int64  `json:"ended_at,omitempty"`
	Duration  int64  `json:"duration_sec,omitempty"`
	// Error is the job's error line (gofer-9b1b: why a held job was cancelled — "hold
	// rejected by …" / "hold expired"), so the completion notice can say it.
	Error string `json:"error,omitempty"`
}

func (s *Server) sessionWatchView(w jobstore.SessionJobWatch) (sessionWatchView, bool) {
	res, ok := s.jobs.Get(w.JobID)
	if !ok {
		return sessionWatchView{}, false
	}
	duration := int64(0)
	if res.StartedAt > 0 && res.EndedAt >= res.StartedAt {
		duration = res.EndedAt - res.StartedAt
	}
	return sessionWatchView{JobID: res.ID, Title: res.Title, Status: res.Status,
		ExitCode: res.ExitCode, StartedAt: res.StartedAt, EndedAt: res.EndedAt,
		Duration: duration, Error: res.Error}, true
}

func (s *Server) handleAddSessionWatch(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	var body struct {
		JobID string `json:"job_id"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	jobID := strings.TrimSpace(body.JobID)
	if jobID == "" {
		writeError(c, http.StatusBadRequest, "job_id required", "a session watch requires job_id")
		return
	}
	if !s.jobMayWatchJob(c, jobID) {
		return
	}
	// A job credential may attach itself to ANY session (SEC-01's single session write: a
	// job listens to the session it was asked to watch); a person follows the session rule
	// (plan T1.5c).
	if _, isJob := jobCallerFromCtx(c); !isJob {
		a, ok := s.sessionForAccess(c, "watch job")
		if !ok {
			return
		}
		if !s.mayReplyToSession(c, a.CallerID) {
			s.denySessionAction(c, authz.PermSessionReplyOthers)
			return
		}
	}
	if _, ok := s.jobs.Get(jobID); !ok {
		writeError(c, http.StatusNotFound, "job not found", jobID)
		return
	}
	w, err := s.relay.AddJobWatch(c.Param("sid"), jobID)
	if err != nil {
		writeError(c, relayStatus(err), "add session watch failed", err.Error())
		return
	}
	view, _ := s.sessionWatchView(w)
	c.JSON(http.StatusOK, view)
}

func (s *Server) handleListSessionWatches(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	a, ok := s.sessionForAccess(c, "list session watches")
	if !ok {
		return
	}
	if !s.mayReadSession(c, a.CallerID) {
		s.denySessionAction(c, authz.PermSessionReadOthers)
		return
	}
	watches, err := s.relay.JobWatches(c.Param("sid"))
	if err != nil {
		writeError(c, relayStatus(err), "list session watches failed", err.Error())
		return
	}
	out := make([]sessionWatchView, 0, len(watches))
	for _, w := range watches {
		if view, ok := s.sessionWatchView(w); ok {
			out = append(out, view)
		}
	}
	c.JSON(http.StatusOK, map[string]any{"watches": out, "subagent_count": s.relay.SubagentCount(a.SessionID)})
}

func (s *Server) handleRemoveSessionWatch(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	a, ok := s.sessionForAccess(c, "remove job watch")
	if !ok {
		return
	}
	if !s.mayReplyToSession(c, a.CallerID) {
		s.denySessionAction(c, authz.PermSessionReplyOthers)
		return
	}
	ok, err := s.relay.RemoveJobWatch(c.Param("sid"), c.Param("job_id"))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "remove session watch failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"removed": ok})
}

func (s *Server) handleCompleteWatchedTurn(c *rux.Context) {
	s.completeWatches(c, c.Param("id"))
}

// handleCompleteWatchedJobs is the turn-less delivery ack (POST
// /v1/sessions/{sid}/watches/complete): the Stop hook of a relay-off session, or
// the UserPromptSubmit / SessionStart catch-up, injects the terminal-job notice
// itself and marks the watches delivered. completed=false means another hook
// already delivered them.
func (s *Server) handleCompleteWatchedJobs(c *rux.Context) {
	s.completeWatches(c, "")
}

func (s *Server) completeWatches(c *rux.Context, turnID string) {
	if !s.relayReady(c) {
		return
	}
	a, ok := s.sessionForAccess(c, "complete watched turn")
	if !ok {
		return
	}
	if !s.mayReplyToSession(c, a.CallerID) {
		s.denySessionAction(c, authz.PermSessionReplyOthers)
		return
	}
	var body struct {
		JobIDs []string `json:"job_ids"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	listed, err := s.relay.JobWatches(c.Param("sid"))
	if err != nil {
		writeError(c, relayStatus(err), "list session watches failed", err.Error())
		return
	}
	known := make(map[string]bool, len(listed))
	for _, watch := range listed {
		known[watch.JobID] = true
	}
	for _, id := range body.JobIDs {
		if !known[id] {
			writeError(c, http.StatusConflict, "job is not watched", id)
			return
		}
		res, ok := s.jobs.Get(id)
		// needs_review counts: the agent is done and the job waits for its supervisor.
		if !ok || !job.IsFinished(res.Status) {
			writeError(c, http.StatusConflict, "job is not terminal", id)
			return
		}
	}
	var completed bool
	if turnID == "" {
		completed, err = s.relay.CompleteWatchedJobs(c.Param("sid"), body.JobIDs)
	} else {
		completed, err = s.relay.CompleteWatchedTurn(c.Param("sid"), turnID, body.JobIDs)
	}
	if err != nil {
		writeError(c, relayStatus(err), "complete watched turn failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"completed": completed})
}

// sessionView is the HTTP projection of an agent_sessions row (session relay,
// SESS-01 §5). Timestamps are unix seconds.
type sessionView struct {
	SessionID  string `json:"session_id"`
	Agent      string `json:"agent"`
	ProjectKey string `json:"project_key,omitempty"`
	Runner     string `json:"runner,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	Title      string `json:"title,omitempty"`
	Transcript string `json:"transcript,omitempty"`
	TmuxPane   string `json:"tmux_pane,omitempty"`
	State      string `json:"state"`
	// RelayMode is the three-state switch (auto|on|off, R1) — what the CLI/web
	// toggle writes. WaitReason says whether a Stop would wait right now and WHY
	// (mode_on / idle_probe / turn_age, empty = does not wait); it is what the
	// hook keys on and what the pre-R1 `relay` boolean used to summarise.
	RelayMode string `json:"relay_mode"`
	// RelayDemotedAt is when an `on` switch fell back to `auto` because the human typed
	// in the terminal (single-session reads only; 0 = not the current story).
	RelayDemotedAt int64  `json:"relay_demoted_at,omitempty"`
	WaitReason     string `json:"wait_reason,omitempty"`
	// WaitReasonDetail explains a session that does NOT wait right now (SUP-01 D):
	// "supervising 2 jobs" — the caller behind this session has live work, so the
	// auto rules deliberately stay out of the way. Empty whenever WaitReason is set.
	WaitReasonDetail string `json:"wait_reason_detail,omitempty"`
	// WaitBudgetSec is how long the Stop hook may block for a reply right now
	// (N1 §C: session.relay_on_wait_sec for `on`, relay_auto_wait_sec for an
	// auto-armed wait); the hook uses min(--wait, budget). Omitted when the session
	// does not wait or no cap is configured.
	WaitBudgetSec int `json:"wait_budget_sec,omitempty"`
	// SubagentCount is how many sub-agents are running in the session (N1 §C).
	SubagentCount int `json:"subagent_count,omitempty"`
	// PermissionWaitReason / PermissionWaitBudgetSec are WaitReason / WaitBudgetSec for
	// a terminal permission prompt (sessionrelay.PermissionWaitReason): the supervising
	// gate does not apply, since the dialog blocks the terminal anyway.
	PermissionWaitReason    string `json:"permission_wait_reason,omitempty"`
	PermissionWaitBudgetSec int    `json:"permission_wait_budget_sec,omitempty"`
	// StopJobWait is session.stop_job_wait (gofer-rhux, away|always|never): what this
	// session's Stop hook should do about the session's WATCHED JOBS. The hook owns the
	// decision (it holds the keyboard reading); the server states the operator's
	// preference here so a hook with no config file of its own (a container) still obeys
	// it. A hook whose own config sets the key uses that instead.
	StopJobWait string `json:"stop_job_wait,omitempty"`
	// CallerID is the authenticated caller that registered the session (its
	// owner, SUP-01 D / bd h-aii-esus): who may answer it, and whose live jobs
	// keep it from auto-arming. Empty for a session registered before the column
	// existed or on a server with no token configured.
	CallerID string `json:"caller_id,omitempty"`
	// RefusedCallerID / RefusedAt record the last hook report that was REFUSED
	// because it came from a caller other than the owner (gofer-eh53): the terminal
	// is beating under the wrong identity after a token change, so its state, last
	// message and progress have stopped moving. The web shows it and offers the
	// hand-over; the owner's next successful report clears it.
	RefusedCallerID string `json:"refused_caller_id,omitempty"`
	RefusedAt       int64  `json:"refused_at,omitempty"`
	TurnNo          int64  `json:"turn_no"`
	LastMessage     string `json:"last_message,omitempty"`
	LastEvent       string `json:"last_event,omitempty"`
	LastSeenAt      int64  `json:"last_seen_at"`
	StartedAt       int64  `json:"started_at"`
	EndedAt         int64  `json:"ended_at,omitempty"`
	// AutoArmed reports that the keyboard idle rule alone arms relay for this
	// session (the human has been away for >= session.auto_relay_idle_sec) and
	// IdleSec is the reading that decided it (-1 = unknown). Kept for pre-R2
	// clients; new clients read WaitReason. LastHumanAt is when a human last
	// acted here (unix seconds, 0 = never) — the turn_age anchor, so the web can
	// render "auto (no input 22m)".
	AutoArmed   bool  `json:"auto_armed"`
	IdleSec     int64 `json:"idle_sec"`
	LastHumanAt int64 `json:"last_human_at,omitempty"`
	// HandedOffJobID / HandedOffAt are path B's takeover (design §9.1 B): the
	// interactive pty job that now continues this session, so the web can link to it
	// (and offer the release).
	HandedOffJobID string `json:"handed_off_job_id,omitempty"`
	HandedOffAt    int64  `json:"handed_off_at,omitempty"`
	// Notice is a one-line message for the ORIGINAL terminal, which the hook prints
	// on stderr (design §9.1 B): set while the session is taken over, so the person
	// at the keyboard learns why its relay went quiet and where to continue. Empty
	// for every other state — an ordinary session has nothing to announce.
	Notice         string `json:"notice,omitempty"`
	WatchCount     int    `json:"watch_count,omitempty"`
	PeerName       string `json:"peer_name,omitempty"`
	PeerNameSource string `json:"peer_name_source,omitempty"`
	PeerStatus     string `json:"peer_status,omitempty"`
	PeerMessaging  bool   `json:"peer_messaging"`
	ProgressText   string `json:"progress_text,omitempty"`
	ProgressAt     int64  `json:"progress_at,omitempty"`
	// LastCwd is the directory the hook reported on its latest heartbeat (display
	// only; Cwd stays the registered directory). Empty when it equals Cwd or was
	// never reported.
	LastCwd string `json:"last_cwd,omitempty"`
	// Usage is the session's accumulated token usage read from its transcript (N2 §A):
	// the main conversation, the sub-agents, and the per-model split. Omitted when
	// nothing was ever reported. Tokens only; a cost shows only if the source gave one.
	Usage *sessionUsageView `json:"usage,omitempty"`
	// CanResume / ResumeReason / ResumeMessage are the wake-up verdict (the dry run of
	// POST /v1/sessions/{sid}/resume): the console greys the button out and shows the
	// plain-language reason on hover. An ended session can be woken up.
	CanResume     bool               `json:"can_resume"`
	ResumeReason  string             `json:"resume_reason,omitempty"`
	ResumeMessage string             `json:"resume_message,omitempty"`
	Watches       []sessionWatchView `json:"watches,omitempty"`
	// Topic / Scope / TopicSource: what the session says it works on and the project-relative
	// sub-paths it owns. A session with no topic of its own shows an INFERRED topic (its
	// last_cwd relative to the project root plus peer_name) with topic_source "inferred" —
	// derived on every read, never stored. TopicAt is when it was last set (0 = never).
	Topic       string   `json:"topic,omitempty"`
	Scope       []string `json:"scope,omitempty"`
	TopicSource string   `json:"topic_source,omitempty"`
	TopicAt     int64    `json:"topic_at,omitempty"`
}

// toSessionView projects a stored session. Relay / WaitReason / AutoArmed are
// derived from the relay service's single policy (the store only keeps the raw
// mode and readings).
func (s *Server) toSessionView(a jobstore.AgentSession) sessionView {
	reason, detail, permReason := "", "", ""
	budget, permBudget, subagents := 0, 0, 0
	watchCount := 0
	var watchesView []sessionWatchView
	var resume sessionrelay.ResumePlan
	if s.relay != nil {
		reason, detail = s.relay.WaitDecision(a)
		budget, subagents = s.relay.WaitBudgetSec(reason), s.relay.SubagentCount(a.SessionID)
		permReason = s.relay.PermissionWaitReason(a)
		permBudget = s.relay.WaitBudgetSec(permReason)
		resume = s.relay.PlanResumeFor(a)
		if watches, err := s.relay.JobWatches(a.SessionID); err == nil {
			watchCount = len(watches)
			watchesView = make([]sessionWatchView, 0, len(watches))
			for _, watch := range watches {
				if view, ok := s.sessionWatchView(watch); ok {
					watchesView = append(watchesView, view)
				}
			}
		}
	}
	topic, topicSource := sessionrelay.EffectiveTopic(a, s.projectRoots(a.ProjectKey))
	return sessionView{
		Topic: topic, Scope: a.Scope, TopicSource: topicSource, TopicAt: a.TopicAt,
		SessionID: a.SessionID, Agent: a.Agent, ProjectKey: a.ProjectKey, Runner: s.resolveRunnerName(a.Runner),
		Cwd: a.Cwd, Title: a.Title, Transcript: a.Transcript, TmuxPane: a.TmuxPane,
		State: a.State, RelayMode: a.RelayMode, WaitReason: reason,
		WaitReasonDetail: detail, WaitBudgetSec: budget, SubagentCount: subagents, CallerID: a.CallerID,
		RefusedCallerID: a.RefusedCallerID, RefusedAt: a.RefusedAt,
		PermissionWaitReason: permReason, PermissionWaitBudgetSec: permBudget,
		StopJobWait: s.relay.StopJobWait,
		TurnNo:      a.TurnNo, LastMessage: a.LastMessage,
		LastEvent: a.LastEvent, LastSeenAt: a.LastSeenAt, StartedAt: a.StartedAt, EndedAt: a.EndedAt,
		AutoArmed: reason == sessionrelay.WaitIdleProbe, IdleSec: a.IdleSec, LastHumanAt: a.LastHumanAt,
		HandedOffJobID: a.HandedOffJobID, HandedOffAt: a.HandedOffAt,
		Notice: handedOffNotice(a), WatchCount: watchCount, Watches: watchesView,
		PeerName: a.PeerName, PeerNameSource: a.PeerNameSource, PeerStatus: a.PeerStatus, PeerMessaging: a.PeerMessaging,
		ProgressText: a.ProgressText, ProgressAt: a.ProgressAt, LastCwd: lastCwdForView(a),
		CanResume: resume.Can, ResumeReason: resume.Reason, ResumeMessage: resume.Message,
		Usage: s.withEstimate(sessionUsageViewOf(a.UsageJSON)),
	}
}

// sessionUsageView is the wire shape of a session's usage: main + sub + total, and the
// per-model split of the same tokens.
type sessionUsageView struct {
	Main    runner.Usage            `json:"main"`
	Sub     runner.Usage            `json:"sub"`
	Total   runner.Usage            `json:"total"`
	ByModel map[string]runner.Usage `json:"by_model,omitempty"`
	// EstimatedCostUSD prices the tallies that report no cost with the config's pricing
	// table (gofer-2ldy); UnpricedModels are the models it has no price for.
	EstimatedCostUSD float64  `json:"estimated_cost_usd,omitempty"`
	UnpricedModels   []string `json:"unpriced_models,omitempty"`
}

// priceFor is the config's model price lookup (gofer-2ldy).
func (s *Server) priceFor(model string) (config.ModelPrice, bool) {
	if s.projects == nil {
		return config.ModelPrice{}, false
	}
	return s.projects.Config().PriceFor(model)
}

// withEstimate fills a usage view's cost estimate from the pricing table.
func (s *Server) withEstimate(v *sessionUsageView) *sessionUsageView {
	if v == nil {
		return nil
	}
	v.EstimatedCostUSD, v.UnpricedModels = runner.SessionUsage{Main: v.Main, Sub: v.Sub, ByModel: v.ByModel}.EstimateCost(s.priceFor)
	return v
}

func sessionUsageViewOf(raw string) *sessionUsageView {
	u := jobstore.ParseSessionUsage(raw)
	if u.Empty() {
		return nil
	}
	return &sessionUsageView{Main: u.Main, Sub: u.Sub, Total: u.Total(), ByModel: u.ByModel}
}

// handedOffNotice is what the ORIGINAL terminal is told once its session belongs
// to a takeover job (design §9.1 B): why its relay stopped, where the
// conversation continues, and how to get back — printed by the hook on stderr,
// where the person at the keyboard actually sees it. "" for every other state.
func handedOffNotice(a jobstore.AgentSession) string {
	if a.State != jobstore.SessionHandedOff {
		return ""
	}
	when := "刚刚"
	if a.HandedOffAt > 0 {
		when = time.Unix(a.HandedOffAt, 0).Format("2006-01-02 15:04")
	}
	return fmt.Sprintf(
		"该会话已于 %s 在 web 接管（job %s），继续请在 web 终端或 --resume；本终端的中继已停用",
		when, a.HandedOffJobID)
}

// relayStatus maps sessionrelay sentinel errors to HTTP statuses.
func relayStatus(err error) int {
	switch {
	case errors.Is(err, sessionrelay.ErrSessionHeartbeatOwner):
		return http.StatusForbidden
	case errors.Is(err, sessionrelay.ErrUnknownSession), errors.Is(err, sessionrelay.ErrUnknownTurn):
		return http.StatusNotFound
	case errors.Is(err, sessionrelay.ErrNoOpenTurn), errors.Is(err, sessionrelay.ErrRelayOff),
		errors.Is(err, sessionrelay.ErrNotHandedOff), errors.Is(err, sessionrelay.ErrSessionEnded):
		return http.StatusConflict
	case errors.Is(err, sessionrelay.ErrInvalidInput):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// relayReady answers 503 when no job store (hence no relay service) is wired —
// only reachable for servers built without a store (peer bridges / tests).
func (s *Server) relayReady(c *rux.Context) bool {
	if s.relay == nil {
		writeError(c, http.StatusServiceUnavailable, "session relay unavailable", "no job store wired on this server")
		return false
	}
	return true
}

// registerSessionReq is the POST /v1/sessions body (hook SessionStart / first
// contact). project_key may be omitted: the server then matches cwd against the
// registered projects' host/container paths.
type registerSessionReq struct {
	SessionID      string `json:"session_id"`
	Agent          string `json:"agent"`
	ProjectKey     string `json:"project_key,omitempty"`
	Runner         string `json:"runner,omitempty"`
	Cwd            string `json:"cwd,omitempty"`
	Title          string `json:"title,omitempty"`
	Transcript     string `json:"transcript,omitempty"`
	TmuxPane       string `json:"tmux_pane,omitempty"`
	Event          string `json:"event,omitempty"`
	PeerName       string `json:"peer_name,omitempty"`
	PeerNameSource string `json:"peer_name_source,omitempty"`
	PeerStatus     string `json:"peer_status,omitempty"`
	PeerMessaging  bool   `json:"peer_messaging,omitempty"`
}

// projectKeyForCwd finds the registered project whose host_path or
// container_path contains cwd (longest match wins). "" when none matches. The
// hook may run on the host or inside a container, so BOTH path views are tried
// (this is a lookup for display/filtering, not an execution path — G002 does
// not apply).
func (s *Server) projectKeyForCwd(cwd string) string {
	cwd = filepath.ToSlash(strings.TrimSpace(cwd))
	if cwd == "" || s.projects == nil {
		return ""
	}
	cfg := s.projects.Config()
	if cfg == nil {
		return ""
	}
	best, bestLen := "", 0
	for key, p := range cfg.Projects {
		for _, root := range []string{p.HostPath, p.ContainerPath} {
			root = strings.TrimRight(filepath.ToSlash(root), "/")
			if root == "" {
				continue
			}
			if (cwd == root || strings.HasPrefix(cwd, root+"/")) && len(root) > bestLen {
				best, bestLen = key, len(root)
			}
		}
	}
	return best
}

// handleRegisterSession upserts an agent session (POST /v1/sessions).
func (s *Server) handleRegisterSession(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	var body registerSessionReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if strings.TrimSpace(body.SessionID) == "" {
		writeError(c, http.StatusBadRequest, "session_id required", "a session registration requires session_id")
		return
	}
	projectKey := strings.TrimSpace(body.ProjectKey)
	if projectKey == "" {
		projectKey = s.projectKeyForCwd(body.Cwd)
	}
	a, allowed, err := s.relay.RegisterForOwner(sessionrelay.RegisterInput{
		SessionID: body.SessionID, Agent: body.Agent, ProjectKey: projectKey, Runner: s.resolveRunnerName(body.Runner),
		Cwd: body.Cwd, Title: body.Title, Transcript: body.Transcript, TmuxPane: body.TmuxPane,
		Event: body.Event, CallerID: callerFromCtx(c), PeerName: body.PeerName, PeerNameSource: body.PeerNameSource,
		PeerStatus: body.PeerStatus, PeerMessaging: body.PeerMessaging,
	})
	if err != nil {
		writeError(c, relayStatus(err), "register session failed", err.Error())
		return
	}
	if !allowed {
		writeError(c, http.StatusForbidden, "register session not permitted for this caller",
			"only the authenticated owner may refresh an existing session")
		return
	}
	c.JSON(http.StatusOK, s.toSessionView(a))
}

// handleListSessions lists sessions (GET /v1/sessions?project=&state=&agent=&cwd=&all=1).
func (s *Server) handleListSessions(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	opts := jobstore.ListSessionsOpts{
		Project: strings.TrimSpace(c.Query("project")),
		State:   strings.TrimSpace(c.Query("state")),
		Agent:   strings.TrimSpace(c.Query("agent")),
		Cwd:     strings.TrimSpace(c.Query("cwd")),
	}
	if opts.State != "" && !jobstore.ValidSessionState(opts.State) {
		writeError(c, http.StatusBadRequest, "invalid state",
			"state must be one of running|idle|waiting_reply|needs_attention|ended|offline")
		return
	}
	opts.IncludeEnded = c.Query("all") == "1" || c.Query("all") == "true"
	if l, err := strconv.Atoi(c.Query("limit")); err == nil {
		opts.Limit = l
	}
	q, pathQ := strings.TrimSpace(c.Query("q")), strings.TrimSpace(c.Query("path"))
	if q != "" || pathQ != "" {
		s.findSessions(c, opts, q, pathQ)
		return
	}
	list, err := s.relay.List(opts)
	if err != nil {
		writeError(c, relayStatus(err), "list sessions failed", err.Error())
		return
	}
	// The session list is the FILTER form of session.read.others: without the permission a
	// user-class caller sees only its own sessions (plan T1.5c; a worker token and a job
	// credential keep the whole list, as before the migration).
	all := s.mayListAllSessions(c)
	caller := callerFromCtx(c)
	out := make([]sessionView, 0, len(list))
	for _, a := range list {
		if !all && a.CallerID != caller {
			continue
		}
		out = append(out, s.toSessionView(a))
	}
	c.JSON(http.StatusOK, map[string]any{"sessions": out})
}

// handleGetSession returns one session with its recent turns (GET /v1/sessions/{sid}).
func (s *Server) handleGetSession(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	if !s.mayReadSession(c, s.sessionOwnerOrUnknown(c)) {
		s.denySessionAction(c, authz.PermSessionReadOthers)
		return
	}
	limit := 10
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 {
		limit = l
	}
	d, err := s.relay.Get(c.Param("sid"), limit, c.Query("before"))
	if err != nil {
		writeError(c, relayStatus(err), "get session failed", err.Error())
		return
	}
	turns := make([]decisionView, 0, len(d.Turns))
	for _, t := range d.Turns {
		turns = append(turns, toDecisionView(*t))
	}
	view := s.toSessionView(d.Session)
	view.RelayDemotedAt = s.relay.RelayDemotedAt(d.Session)
	c.JSON(http.StatusOK, map[string]any{"session": view, "turns": turns, "has_more": d.HasMore, "next_before": d.NextBefore})
}

// handleSessionMessages serves the raw bounded Markdown log through the same
// authenticated /v1 session route group as the list and detail endpoints.
func (s *Server) handleSessionMessages(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	a, ok := s.sessionForAccess(c, "get session messages")
	if !ok {
		return
	}
	if !s.mayReadSession(c, a.CallerID) {
		s.denySessionAction(c, authz.PermSessionReadOthers)
		return
	}
	data, err := s.jobs.Meta().ReadSessionMessageLog(c.Param("sid"))
	if errors.Is(err, os.ErrNotExist) {
		writeError(c, http.StatusNotFound, "session message log not found", "")
		return
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "read session message log failed", err.Error())
		return
	}
	c.SetHeader("Content-Type", "text/plain; charset=utf-8")
	c.SetHeader("X-Content-Type-Options", "nosniff")
	c.Resp.WriteHeader(http.StatusOK)
	_, _ = c.Resp.Write(data)
}

// handleSendSessionMessage is the Y6 web-to-agent entry point. A waiting
// session is answered through the existing relay turn (user-authored input);
// every other state is delivered by the sessionrelay messenger seam.
func (s *Server) handleSendSessionMessage(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	a, ok := s.sessionForAccess(c, "send session message")
	if !ok {
		return
	}
	if !s.mayReplyToSession(c, a.CallerID) {
		s.denySessionAction(c, authz.PermSessionReplyOthers)
		return
	}
	var body struct {
		Message string `json:"message"`
		Text    string `json:"text"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid session message", err.Error())
		return
	}
	ctx := c.Req.Context()
	message := body.Message
	if strings.TrimSpace(message) == "" {
		message = body.Text
	}
	m, err := s.relay.SendMessage(ctx, c.Param("sid"), message, callerFromCtx(c))
	if err != nil {
		if m.ID != "" {
			c.JSON(http.StatusConflict, m)
			return
		}
		writeError(c, relayStatus(err), "session message rejected", err.Error())
		return
	}
	// A person reached the session from the web: the conversation follows them there.
	s.relay.FollowWeb(c.Param("sid"), callerFromCtx(c))
	c.JSON(http.StatusOK, m)
}

// handleSessionReply records a session's own answer to a web message (POST
// /v1/sessions/{sid}/replies, gofer-6er0): the target's hook saw it answer the
// messenger with SendMessage and reports the text verbatim. Only the session's
// owner may report (same identity rule as a heartbeat).
func (s *Server) handleSessionReply(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	var body struct {
		Text string `json:"text"`
		To   string `json:"to,omitempty"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid session reply", err.Error())
		return
	}
	m, created, err := s.relay.RecordReply(c.Param("sid"), body.Text, body.To, jobstore.SessionReplySourceSession, callerFromCtx(c))
	if err != nil {
		writeError(c, relayStatus(err), "session reply rejected", err.Error())
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	c.JSON(status, m)
}

func (s *Server) handleSessionOutbox(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	// The outbox holds what the session has NOT sent yet — 读其待发内容 is part of
	// session.reply.others (design §1, the revision that took it back out of
	// session.read.others), not of the read permission.
	a, ok := s.sessionForAccess(c, "read session outbox")
	if !ok {
		return
	}
	if !s.mayReplyToSession(c, a.CallerID) {
		s.denySessionAction(c, authz.PermSessionReplyOthers)
		return
	}
	// gofer-qzv5: a message whose delivery job outlived gofer's wait window is recorded
	// 「送达中」 until that job ends. Settle whatever has ended since (the job's terminal
	// hook normally does it first; this heals a write-back lost with a restart or a
	// missed event) so the list never shows a delivery as pending for ever.
	if err := s.relay.ReconcileDeliveries(c.Param("sid")); err != nil {
		slog.Warn("settle pending deliveries failed", "session_id", c.Param("sid"), "err", err)
	}
	list, err := s.jobs.Meta().ReadSessionOutbox(c.Param("sid"))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "read session outbox failed", err.Error())
		return
	}
	limit := 10
	if raw := c.Query("limit"); raw != "" {
		if parsed, parseErr := strconv.Atoi(raw); parseErr == nil && parsed > 0 {
			limit = parsed
		}
	}
	cursor, err := decodeSessionOutboxCursor(c.Query("before"))
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid outbox cursor", err.Error())
		return
	}
	filtered := list
	if c.Query("before") != "" {
		cut := len(filtered)
		for i, m := range filtered {
			if m.CreatedAt > cursor.CreatedAt || (m.CreatedAt == cursor.CreatedAt && m.ID >= cursor.ID) {
				cut = i
				break
			}
		}
		filtered = filtered[:cut]
	}
	hasMore := len(filtered) > limit
	if hasMore {
		filtered = filtered[len(filtered)-limit:]
	}
	nextBefore := ""
	if hasMore && len(filtered) > 0 {
		nextBefore = encodeSessionOutboxCursor(filtered[0])
	}
	c.JSON(http.StatusOK, map[string]any{"messages": filtered, "has_more": hasMore, "next_before": nextBefore})
}

// handleDeleteSession removes a registration (DELETE /v1/sessions/{sid}).
func (s *Server) handleDeleteSession(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	a, ok := s.sessionForAccess(c, "delete session")
	if !ok {
		return
	}
	if !s.mayManageSession(c, a.CallerID) {
		s.denySessionAction(c, authz.PermSessionManageOthers)
		return
	}
	if err := s.relay.Delete(c.Param("sid")); err != nil {
		writeError(c, relayStatus(err), "delete session failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"deleted": true})
}

type sessionHeartbeatReq struct {
	Event       string `json:"event"`
	State       string `json:"state,omitempty"`
	LastMessage string `json:"last_message,omitempty"`
	Title       string `json:"title,omitempty"`
	Injected    bool   `json:"injected,omitempty"`
	// IdleSec is the OS input idle time in seconds (-1 = unknown), sent by the
	// events that probe for it (Stop, Notification/idle_prompt). Omitted by
	// older hooks: nil then means "no reading", NOT "the human is here".
	IdleSec        *int64 `json:"idle_sec,omitempty"`
	PeerName       string `json:"peer_name,omitempty"`
	PeerNameSource string `json:"peer_name_source,omitempty"`
	PeerStatus     string `json:"peer_status,omitempty"`
	PeerMessaging  *bool  `json:"peer_messaging,omitempty"`
	ProgressText   string `json:"progress_text,omitempty"`
	ProgressAt     int64  `json:"progress_at,omitempty"`
	ClearProgress  bool   `json:"clear_progress,omitempty"`
	// Cwd is the hook's current directory — shown as "current directory", never used
	// to decide where a wake-up runs.
	Cwd string `json:"cwd,omitempty"`
	// SubagentID / SubagentDelta ride the SubagentStart (+1) / SubagentStop (-1)
	// events (N1 §C): the sub-agent's id for dedupe, and the count change.
	SubagentID    string `json:"subagent_id,omitempty"`
	SubagentDelta int    `json:"subagent_delta,omitempty"`
	// UsageDelta is the transcript token usage since the hook's last report (N2 §A).
	UsageDelta *runner.SessionUsage `json:"usage_delta,omitempty"`
}

// handleSessionHeartbeat applies a hook event (POST /v1/sessions/{sid}/heartbeat)
// and returns the session — its `relay` field is the switch the Stop hook keys
// on. 404 for an unknown session (the hook then registers and retries).
func (s *Server) handleSessionHeartbeat(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	var body sessionHeartbeatReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if body.State != "" && !jobstore.ValidSessionState(body.State) {
		writeError(c, http.StatusBadRequest, "invalid state", "unknown session state "+body.State)
		return
	}
	a, err := s.relay.HeartbeatForOwner(c.Param("sid"), sessionrelay.HeartbeatInput{
		Event: body.Event, State: body.State, LastMessage: body.LastMessage, Title: body.Title,
		Injected: body.Injected, IdleSec: body.IdleSec, CallerID: callerFromCtx(c),
		PeerName: body.PeerName, PeerNameSource: body.PeerNameSource, PeerStatus: body.PeerStatus, PeerMessaging: body.PeerMessaging,
		ProgressText: body.ProgressText, ProgressAt: body.ProgressAt, ClearProgress: body.ClearProgress,
		Cwd: body.Cwd, SubagentID: body.SubagentID, SubagentDelta: body.SubagentDelta,
		UsageDelta: body.UsageDelta,
	})
	if err != nil {
		writeError(c, relayStatus(err), "session heartbeat failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, s.toSessionView(a))
}

// sessionRelayReq is the POST /v1/sessions/{sid}/relay body: the three-state
// switch (R1). `auto` (the default) is only reachable through this form.
type sessionRelayReq struct {
	Mode string `json:"mode,omitempty"`
}

// mode resolves the request into one relay mode ("" = malformed: no mode given).
func (b sessionRelayReq) mode() string {
	return strings.ToLower(strings.TrimSpace(b.Mode))
}

// handleSetSessionRelay sets the relay switch (POST /v1/sessions/{sid}/relay).
func (s *Server) handleSetSessionRelay(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	var body sessionRelayReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	mode := body.mode()
	if mode == "" {
		writeError(c, http.StatusBadRequest, "relay mode required",
			`send {"mode":"auto|on|off"}`)
		return
	}
	a, ok := s.sessionForAccess(c, "set relay")
	if !ok {
		return
	}
	if !s.mayReplyToSession(c, a.CallerID) {
		s.denySessionAction(c, authz.PermSessionReplyOthers)
		return
	}
	a, err := s.relay.SetRelayModeBy(c.Param("sid"), mode, callerFromCtx(c))
	if err != nil {
		writeError(c, relayStatus(err), "set relay failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, s.toSessionView(a))
}

type openTurnReq struct {
	Body       string `json:"body"`
	TimeoutSec int64  `json:"timeout_sec,omitempty"`
}

// handleOpenTurn posts the agent's last message as a relay turn
// (POST /v1/sessions/{sid}/turns). 409 when relay is off.
func (s *Server) handleOpenTurn(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	a, ok := s.sessionForAccess(c, "open turn")
	if !ok {
		return
	}
	if !s.mayManageSession(c, a.CallerID) {
		s.denySessionAction(c, authz.PermSessionManageOthers)
		return
	}
	var body openTurnReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	d, err := s.relay.OpenTurn(c.Param("sid"), body.Body, body.TimeoutSec)
	if err != nil {
		writeError(c, relayStatus(err), "open turn failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, toDecisionView(d))
}

// maxTurnWait caps the long-poll window (GET ...?wait=N) so a request never
// outlives the client's HTTP timeout (internal/client uses 30s).
const maxTurnWait = 25 * time.Second

// handleWaitTurn long-polls a turn (GET /v1/sessions/{sid}/turns/{id}?wait=25):
// returns as soon as it is answered / expired / relay switched off, else after
// `wait` seconds with outcome=open.
func (s *Server) handleWaitTurn(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	if !s.mayReadSession(c, s.sessionOwnerOrUnknown(c)) {
		s.denySessionAction(c, authz.PermSessionReadOthers)
		return
	}
	var wait time.Duration
	if w, err := strconv.Atoi(c.Query("wait")); err == nil && w > 0 {
		wait = time.Duration(w) * time.Second
		if wait > maxTurnWait {
			wait = maxTurnWait
		}
	}
	st, err := s.relay.WaitTurn(c.Req.Context(), c.Param("sid"), c.Param("id"), wait)
	if err != nil {
		writeError(c, relayStatus(err), "wait turn failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"outcome":     st.Outcome,
		"relay":       st.Relay,
		"wait_reason": st.Reason,
		// wait_reason_detail explains a NON-waiting session (SUP-01 D): the wait
		// ended because the caller is supervising live jobs.
		"wait_reason_detail": st.Detail,
		"decision":           toDecisionView(st.Decision),
	})
}

// handleAckTurn marks an OPEN relay turn read without answering it.
func (s *Server) handleAckTurn(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	if !s.mayReplyToSession(c, s.sessionOwnerOrUnknown(c)) {
		s.denySessionAction(c, authz.PermSessionReplyOthers)
		return
	}
	d, err := s.relay.AckTurn(c.Param("sid"), c.Param("id"), callerFromCtx(c))
	if err != nil {
		writeError(c, relayStatus(err), "ack turn failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, toDecisionView(d))
}

func (s *Server) handleUnackTurn(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	if !s.mayReplyToSession(c, s.sessionOwnerOrUnknown(c)) {
		s.denySessionAction(c, authz.PermSessionReplyOthers)
		return
	}
	d, err := s.relay.UnackTurn(c.Param("sid"), c.Param("id"))
	if err != nil {
		writeError(c, relayStatus(err), "unack turn failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, toDecisionView(d))
}

type sessionReleaseReq struct {
	IdleSec int64 `json:"idle_sec"`
}

// handleReleaseTurn closes an auto-armed wait because the hook's fresh idle
// reading says the human is back (POST /v1/sessions/{sid}/turns/{id}/release,
// SR-A5). `released:false` is a normal answer meaning "keep waiting" (still
// away, explicit relay, or the turn already settled) — the hook only stops
// blocking when it gets true.
func (s *Server) handleReleaseTurn(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	a, ok := s.sessionForAccess(c, "release turn")
	if !ok {
		return
	}
	if !s.mayManageSession(c, a.CallerID) {
		s.denySessionAction(c, authz.PermSessionManageOthers)
		return
	}
	var body sessionReleaseReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	released, err := s.relay.ReleaseTurn(c.Param("sid"), c.Param("id"), body.IdleSec)
	if err != nil {
		writeError(c, relayStatus(err), "release turn failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"released": released})
}

type sessionSayReq struct {
	Answer string `json:"answer"`
}

// handleSessionSay answers the session's newest OPEN turn
// (POST /v1/sessions/{sid}/say). 409 when nothing is waiting.
func (s *Server) handleSessionSay(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	a, ok := s.sessionForAccess(c, "say")
	if !ok {
		return
	}
	if !s.mayReplyToSession(c, a.CallerID) {
		s.denySessionAction(c, authz.PermSessionReplyOthers)
		return
	}
	var body sessionSayReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	d, err := s.relay.Say(c.Param("sid"), body.Answer, callerFromCtx(c))
	if err != nil {
		writeError(c, relayStatus(err), "say failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, toDecisionView(d))
}

// deliverStatus maps a Deliver failure to its HTTP status. A session that cannot
// be reached is a 409 (the client can act: use tmux, or the takeover path once
// 2-B lands) — except an injection that was dispatched and FAILED, which is the
// server's side of the wire: 502.
func deliverStatus(err error) int {
	reason := sessionrelay.DeliverReason(err)
	switch {
	case reason == "":
		return relayStatus(err)
	case strings.HasPrefix(reason, sessionrelay.InjectFailedPrefix),
		strings.HasPrefix(reason, sessionrelay.DeliverFailedPrefix):
		return http.StatusBadGateway
	default:
		return http.StatusConflict
	}
}

type sessionDeliverReq struct {
	Text string `json:"text"`
	// AllowTakeover is path B's opt-in (design §9.1 B): the reply may start a NEW
	// process (`--resume`) that continues the session when it has no usable tmux
	// pane. Off by default — a takeover moves the session away from the terminal the
	// human left, so it is asked for explicitly (the web confirms first).
	AllowTakeover bool `json:"allow_takeover,omitempty"`
}

// handleSessionDeliver routes a reply to a session that is not waiting
// (POST /v1/sessions/{sid}/deliver, design §9.1): an OPEN turn is answered as
// `say` does, otherwise the text is typed into the session's tmux pane (path A) —
// or, with allow_takeover, into a new `--resume` process when no pane is usable
// (path B). 409 when the session cannot be reached (no runner / no tmux / ended /
// handed_off / no_resume_template / interactive_not_allowed / cwd_outside_project)
// — the response body says which — 502 when the dispatch itself failed, 400 when
// the text is empty or over 8KB.
//
// Only a human caller may drive a terminal: a worker token is refused (a worker
// runs jobs, it does not answer for a person).
func (s *Server) handleSessionDeliver(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	a, ok := s.sessionForAccess(c, "deliver")
	if !ok {
		return
	}
	if !s.mayReplyToSession(c, a.CallerID) {
		s.denySessionAction(c, authz.PermSessionReplyOthers)
		return
	}
	var body sessionDeliverReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	// 允许接管时（allow_takeover）这次请求会另起一个 pty job，所以它同时是 job.submit 的入口
	// （design §1 job.submit：deliver，允许接管时起 pty job）——只在确实会起 job 的这一支查，
	// 普通的 tmux 投递不起 job、不查。
	if body.AllowTakeover && !s.can(c, authz.PermJobSubmit) {
		s.denySessionAction(c, authz.PermJobSubmit)
		return
	}
	res, err := s.relay.Deliver(c.Req.Context(), c.Param("sid"), body.Text, callerFromCtx(c), body.AllowTakeover)
	if err != nil {
		// An undeliverable session reports its reason code in the envelope's short
		// error string, so a client can act on it without parsing the message
		// (the web reads that field as `code`: no_tmux ⇒ offer the takeover path).
		if reason := sessionrelay.DeliverReason(err); reason != "" {
			writeError(c, deliverStatus(err), "deliver failed: "+reason, err.Error())
			return
		}
		writeError(c, deliverStatus(err), "deliver failed", err.Error())
		return
	}
	s.relay.FollowWeb(c.Param("sid"), callerFromCtx(c))
	c.JSON(http.StatusOK, map[string]any{
		"path": res.Path, "job_id": res.JobID, "decision_id": res.DecisionID,
		// gofer-qzv5: the text was handed to a job that is still on its way (an update
		// waiting for idle no longer makes this look like a failure): the caller renders
		// 「送达中」 and the outbox says how it ended.
		"delivering": res.Delivering,
	})
}

// handleSessionReleaseTakeover gives a taken-over session back to the terminal it
// came from (POST /v1/sessions/{sid}/release-takeover, design §9.1 B): the takeover
// job is cancelled and the session returns to idle. 409 when it is not handed off
// (the button is only shown while it is), 404 when the session is unknown, 502 when
// the takeover job could not be stopped — a session two processes write would
// diverge, so a failed release must not look like a successful one.
// The session must be handed back to the person who owns it (or anybody, while it has no
// owner): someone else's takeover needs session.manage.others. A worker token was refused
// before the migration and stays refused (mayReleaseSessionTakeover).
func (s *Server) handleSessionReleaseTakeover(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	a, ok := s.sessionForAccess(c, "release takeover")
	if !ok {
		return
	}
	if !s.mayReleaseSessionTakeover(c, a.CallerID) {
		s.denySessionAction(c, authz.PermSessionManageOthers)
		return
	}
	a, err := s.relay.ReleaseTakeover(c.Req.Context(), c.Param("sid"))
	if err != nil {
		writeError(c, releaseTakeoverStatus(err), "release takeover failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, s.toSessionView(a))
}

// releaseTakeoverStatus maps a failed release: everything the relay reports is a
// client-actionable state (unknown session / not handed off), while a cancel that
// the execution machine refused is the server's side of the wire.
func releaseTakeoverStatus(err error) int {
	if errors.Is(err, sessionrelay.ErrUnknownSession) || errors.Is(err, sessionrelay.ErrNotHandedOff) {
		return relayStatus(err)
	}
	return http.StatusBadGateway
}

// lastCwdForView is what a session shows as its "current directory": the hook's
// latest reported cwd, omitted when it adds nothing over the registered one.
func lastCwdForView(a jobstore.AgentSession) string {
	if a.LastCwd == "" || a.LastCwd == a.Cwd {
		return ""
	}
	return a.LastCwd
}

// projectRoots returns a project's root paths on both sides (host_path / container_path), the
// candidates a session's cwd is matched against (G002: whichever side the session runs on).
func (s *Server) projectRoots(projectKey string) []string {
	if projectKey == "" || s.projects == nil {
		return nil
	}
	cfg := s.projects.Config()
	if cfg == nil {
		return nil
	}
	p, ok := cfg.Projects[projectKey]
	if !ok {
		return nil
	}
	return []string{p.HostPath, p.ContainerPath}
}

// sessionFindView is one find result: the session plus why it matched.
type sessionFindView struct {
	sessionView
	Hits []sessionrelay.FindHit `json:"hits"`
	// MaybeOffline marks an offline session ("可能不在线"): it stopped heart-beating.
	MaybeOffline bool `json:"maybe_offline,omitempty"`
}

// findSessions is GET /v1/sessions with q / path. Matching and ranking are the relay
// service's, run AFTER the session.read.others filter and BEFORE the limit.
func (s *Server) findSessions(c *rux.Context, opts jobstore.ListSessionsOpts, q, pathQ string) {
	all := s.mayListAllSessions(c)
	caller := callerFromCtx(c)
	res, err := s.relay.Find(sessionrelay.FindQuery{
		Q: q, Path: pathQ, Project: opts.Project, Limit: opts.Limit,
		Visible: func(a jobstore.AgentSession) bool {
			return (opts.Agent == "" || a.Agent == opts.Agent) && (opts.State == "" || a.State == opts.State) &&
				(all || a.CallerID == caller)
		},
		Roots: s.projectRoots,
	})
	if err != nil {
		writeError(c, relayStatus(err), "find sessions failed", err.Error())
		return
	}
	out := make([]sessionFindView, 0, len(res))
	for _, r := range res {
		out = append(out, sessionFindView{sessionView: s.toSessionView(r.Session), Hits: r.Hits, MaybeOffline: r.MaybeOffline})
	}
	c.JSON(http.StatusOK, map[string]any{"sessions": out})
}

type patchSessionReq struct {
	Topic *string   `json:"topic"`
	Scope *[]string `json:"scope"`
}

// handlePatchSession edits a session's topic and scope (PATCH /v1/sessions/{sid}). The session
// itself (its own id in X-Gofer-Session-Id) records source "self"; anybody else "human".
func (s *Server) handlePatchSession(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	a, ok := s.sessionForAccess(c, "patch session")
	if !ok {
		return
	}
	if !s.mayEditSessionTopic(c, a.CallerID) {
		s.denySessionAction(c, authz.PermSessionManageOthers)
		return
	}
	var body patchSessionReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	source := jobstore.SessionTopicHuman
	if requestSessionID(c) == a.SessionID {
		source = jobstore.SessionTopicSelf
	}
	updated, err := s.relay.SetTopic(a.SessionID, body.Topic, body.Scope, source)
	if err != nil {
		writeError(c, relayStatus(err), "patch session failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, s.toSessionView(updated))
}

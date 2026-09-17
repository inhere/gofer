package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
)

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
	// toggle writes. Relay is DERIVED from the relay service's rules and reports
	// whether a Stop would wait right now (mode `on`, or an auto rule holding);
	// it is what pre-R1 clients read. WaitReason says WHY (mode_on / idle_probe /
	// turn_age, empty = does not wait).
	RelayMode   string `json:"relay_mode"`
	Relay       bool   `json:"relay"`
	WaitReason  string `json:"wait_reason,omitempty"`
	TurnNo      int64  `json:"turn_no"`
	LastMessage string `json:"last_message,omitempty"`
	LastEvent   string `json:"last_event,omitempty"`
	LastSeenAt  int64  `json:"last_seen_at"`
	StartedAt   int64  `json:"started_at"`
	EndedAt     int64  `json:"ended_at,omitempty"`
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
	Notice string `json:"notice,omitempty"`
}

// toSessionView projects a stored session. Relay / WaitReason / AutoArmed are
// derived from the relay service's single policy (the store only keeps the raw
// mode and readings).
func (s *Server) toSessionView(a jobstore.AgentSession) sessionView {
	reason := ""
	if s.relay != nil {
		reason = s.relay.WaitReason(a)
	}
	return sessionView{
		SessionID: a.SessionID, Agent: a.Agent, ProjectKey: a.ProjectKey, Runner: a.Runner,
		Cwd: a.Cwd, Title: a.Title, Transcript: a.Transcript, TmuxPane: a.TmuxPane,
		State: a.State, RelayMode: a.RelayMode, Relay: reason != "", WaitReason: reason,
		TurnNo: a.TurnNo, LastMessage: a.LastMessage,
		LastEvent: a.LastEvent, LastSeenAt: a.LastSeenAt, StartedAt: a.StartedAt, EndedAt: a.EndedAt,
		AutoArmed: reason == sessionrelay.WaitIdleProbe, IdleSec: a.IdleSec, LastHumanAt: a.LastHumanAt,
		HandedOffJobID: a.HandedOffJobID, HandedOffAt: a.HandedOffAt,
		Notice: handedOffNotice(a),
	}
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
	case errors.Is(err, sessionrelay.ErrUnknownSession), errors.Is(err, sessionrelay.ErrUnknownTurn):
		return http.StatusNotFound
	case errors.Is(err, sessionrelay.ErrNoOpenTurn), errors.Is(err, sessionrelay.ErrRelayOff),
		errors.Is(err, sessionrelay.ErrNotHandedOff):
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
	SessionID  string `json:"session_id"`
	Agent      string `json:"agent"`
	ProjectKey string `json:"project_key,omitempty"`
	Runner     string `json:"runner,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	Title      string `json:"title,omitempty"`
	Transcript string `json:"transcript,omitempty"`
	TmuxPane   string `json:"tmux_pane,omitempty"`
	Event      string `json:"event,omitempty"`
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
	a, err := s.relay.Register(sessionrelay.RegisterInput{
		SessionID: body.SessionID, Agent: body.Agent, ProjectKey: projectKey, Runner: body.Runner,
		Cwd: body.Cwd, Title: body.Title, Transcript: body.Transcript, TmuxPane: body.TmuxPane,
		Event: body.Event,
	})
	if err != nil {
		writeError(c, relayStatus(err), "register session failed", err.Error())
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
			"state must be one of running|idle|waiting_reply|needs_attention|ended")
		return
	}
	opts.IncludeEnded = c.Query("all") == "1" || c.Query("all") == "true"
	if l, err := strconv.Atoi(c.Query("limit")); err == nil {
		opts.Limit = l
	}
	list, err := s.relay.List(opts)
	if err != nil {
		writeError(c, relayStatus(err), "list sessions failed", err.Error())
		return
	}
	out := make([]sessionView, 0, len(list))
	for _, a := range list {
		out = append(out, s.toSessionView(a))
	}
	c.JSON(http.StatusOK, map[string]any{"sessions": out})
}

// handleGetSession returns one session with its recent turns (GET /v1/sessions/{sid}).
func (s *Server) handleGetSession(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	limit := 50
	if l, err := strconv.Atoi(c.Query("turns")); err == nil && l > 0 {
		limit = l
	}
	d, err := s.relay.Get(c.Param("sid"), limit)
	if err != nil {
		writeError(c, relayStatus(err), "get session failed", err.Error())
		return
	}
	turns := make([]decisionView, 0, len(d.Turns))
	for _, t := range d.Turns {
		turns = append(turns, toDecisionView(*t))
	}
	c.JSON(http.StatusOK, map[string]any{"session": s.toSessionView(d.Session), "turns": turns})
}

// handleDeleteSession removes a registration (DELETE /v1/sessions/{sid}).
func (s *Server) handleDeleteSession(c *rux.Context) {
	if !s.relayReady(c) {
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
	IdleSec *int64 `json:"idle_sec,omitempty"`
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
	a, err := s.relay.Heartbeat(c.Param("sid"), sessionrelay.HeartbeatInput{
		Event: body.Event, State: body.State, LastMessage: body.LastMessage, Title: body.Title,
		Injected: body.Injected, IdleSec: body.IdleSec,
	})
	if err != nil {
		writeError(c, relayStatus(err), "session heartbeat failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, s.toSessionView(a))
}

// sessionRelayReq is the POST /v1/sessions/{sid}/relay body: the three-state
// switch (R1). Pre-R1 clients send the boolean form, which maps to on/off —
// exactly what those clients meant; `auto` (the default for everyone else) is
// only reachable through the new form.
type sessionRelayReq struct {
	Mode string `json:"mode,omitempty"`
	// Relay is the LEGACY boolean switch (true → on, false → off).
	Relay *bool `json:"relay,omitempty"`
}

// mode resolves the request into one relay mode ("" = malformed: neither field).
func (b sessionRelayReq) mode() string {
	if strings.TrimSpace(b.Mode) != "" {
		return strings.ToLower(strings.TrimSpace(b.Mode))
	}
	if b.Relay != nil {
		if *b.Relay {
			return jobstore.RelayModeOn
		}
		return jobstore.RelayModeOff
	}
	return ""
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
			`send {"mode":"auto|on|off"} (or the legacy {"relay":true|false})`)
		return
	}
	a, err := s.relay.SetRelayMode(c.Param("sid"), mode)
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
		"decision":    toDecisionView(st.Decision),
	})
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
	case strings.HasPrefix(reason, sessionrelay.InjectFailedPrefix):
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
	if callerKindFromCtx(c) == callerKindWorker {
		writeError(c, http.StatusForbidden, "deliver not permitted for this caller",
			"worker tokens cannot deliver: only a human drives a terminal session")
		return
	}
	var body sessionDeliverReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
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
	c.JSON(http.StatusOK, map[string]any{
		"path": res.Path, "job_id": res.JobID, "decision_id": res.DecisionID,
	})
}

// handleSessionReleaseTakeover gives a taken-over session back to the terminal it
// came from (POST /v1/sessions/{sid}/release-takeover, design §9.1 B): the takeover
// job is cancelled and the session returns to idle. 409 when it is not handed off
// (the button is only shown while it is), 404 when the session is unknown, 502 when
// the takeover job could not be stopped — a session two processes write would
// diverge, so a failed release must not look like a successful one.
func (s *Server) handleSessionReleaseTakeover(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	if callerKindFromCtx(c) == callerKindWorker {
		writeError(c, http.StatusForbidden, "release not permitted for this caller",
			"worker tokens cannot release a session: only a human drives a terminal session")
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

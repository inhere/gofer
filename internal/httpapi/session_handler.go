package httpapi

import (
	"errors"
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
	SessionID   string `json:"session_id"`
	Agent       string `json:"agent"`
	ProjectKey  string `json:"project_key,omitempty"`
	Runner      string `json:"runner,omitempty"`
	Cwd         string `json:"cwd,omitempty"`
	Title       string `json:"title,omitempty"`
	Transcript  string `json:"transcript,omitempty"`
	TmuxPane    string `json:"tmux_pane,omitempty"`
	State       string `json:"state"`
	Relay       bool   `json:"relay"`
	TurnNo      int64  `json:"turn_no"`
	LastMessage string `json:"last_message,omitempty"`
	LastEvent   string `json:"last_event,omitempty"`
	LastSeenAt  int64  `json:"last_seen_at"`
	StartedAt   int64  `json:"started_at"`
	EndedAt     int64  `json:"ended_at,omitempty"`
}

func toSessionView(a jobstore.AgentSession) sessionView {
	return sessionView{
		SessionID: a.SessionID, Agent: a.Agent, ProjectKey: a.ProjectKey, Runner: a.Runner,
		Cwd: a.Cwd, Title: a.Title, Transcript: a.Transcript, TmuxPane: a.TmuxPane,
		State: a.State, Relay: a.Relay, TurnNo: a.TurnNo, LastMessage: a.LastMessage,
		LastEvent: a.LastEvent, LastSeenAt: a.LastSeenAt, StartedAt: a.StartedAt, EndedAt: a.EndedAt,
	}
}

// relayStatus maps sessionrelay sentinel errors to HTTP statuses.
func relayStatus(err error) int {
	switch {
	case errors.Is(err, sessionrelay.ErrUnknownSession), errors.Is(err, sessionrelay.ErrUnknownTurn):
		return http.StatusNotFound
	case errors.Is(err, sessionrelay.ErrNoOpenTurn), errors.Is(err, sessionrelay.ErrRelayOff):
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
	c.JSON(http.StatusOK, toSessionView(a))
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
		out = append(out, toSessionView(a))
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
	c.JSON(http.StatusOK, map[string]any{"session": toSessionView(d.Session), "turns": turns})
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
		Injected: body.Injected,
	})
	if err != nil {
		writeError(c, relayStatus(err), "session heartbeat failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, toSessionView(a))
}

type sessionRelayReq struct {
	Relay bool `json:"relay"`
}

// handleSetSessionRelay flips the relay switch (POST /v1/sessions/{sid}/relay).
func (s *Server) handleSetSessionRelay(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	var body sessionRelayReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	a, err := s.relay.SetRelay(c.Param("sid"), body.Relay)
	if err != nil {
		writeError(c, relayStatus(err), "set relay failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, toSessionView(a))
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
		"outcome":  st.Outcome,
		"relay":    st.Relay,
		"decision": toDecisionView(st.Decision),
	})
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

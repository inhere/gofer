package client

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// AgentSessionEnvKeys are the environment variables an agent terminal session
// exports to the commands it runs, in the order a plan created from that session
// looks for "the current session":
//
//   - GOFER_SESSION_ID: explicit, set by the user or any generic agent wrapper;
//   - CLAUDE_CODE_SESSION_ID: Claude Code (the same id its hooks register);
//   - CODEX_THREAD_ID / CODEX_SESSION_ID: Codex CLI (both carry the thread id its
//     hooks register as session_id).
var AgentSessionEnvKeys = []string{"GOFER_SESSION_ID", "CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "CODEX_SESSION_ID"}

// AgentSessionLookupTimeout bounds the "is this a registered session" probe that
// plan auto-binding runs before creating a plan: binding is a convenience and
// must never make `plan create` hang on a slow server.
const AgentSessionLookupTimeout = 3 * time.Second

// SessionCandidate is one agent-session id found in the environment.
type SessionCandidate struct {
	EnvKey string
	ID     string
}

// CurrentAgentSessionCandidates returns the distinct non-empty session ids of
// AgentSessionEnvKeys in priority order.
func CurrentAgentSessionCandidates(getenv func(string) string) []SessionCandidate {
	var out []SessionCandidate
	seen := map[string]bool{}
	for _, k := range AgentSessionEnvKeys {
		id := strings.TrimSpace(getenv(k))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, SessionCandidate{EnvKey: k, ID: id})
	}
	return out
}

// SupervisorAutoBind reports what plan auto-binding did. Session.SessionID is
// set only when the plan was created bound to it; otherwise Reason says why
// nothing was bound (empty Reason + no candidate = no agent session in env).
type SupervisorAutoBind struct {
	Session AgentSession
	EnvKey  string
	Reason  string
}

// Bound reports whether the plan was bound to the current agent session.
func (b SupervisorAutoBind) Bound() bool { return b.Session.SessionID != "" }

// LookupCurrentAgentSession resolves the agent session the caller runs in from
// the environment and confirms it is a live registered session (one short GET per
// candidate; normally exactly one variable is set). project, when non-empty, must
// match the session's project — the server refuses a cross-project binding — and
// the session must run on the server's own runner (see the runner case below).
// Ownership is not visible to the client; the create call is the check.
func (c *Client) LookupCurrentAgentSession(getenv func(string) string, project string) SupervisorAutoBind {
	cands := CurrentAgentSessionCandidates(getenv)
	if len(cands) == 0 {
		return SupervisorAutoBind{}
	}
	probe := &Client{baseURL: c.baseURL, token: c.token, http: &http.Client{Timeout: AgentSessionLookupTimeout}}
	if c.http != nil {
		probe.http.Transport = c.http.Transport
	}
	var res SupervisorAutoBind
	for _, cand := range cands {
		res.EnvKey = cand.EnvKey
		var d SessionDetail
		err := probe.doJSON(http.MethodGet, "/v1/sessions/"+url.PathEscape(cand.ID)+"?limit=1", nil, &d)
		switch {
		case StatusOf(err) == http.StatusNotFound:
			res.Reason = "session " + shortID(cand.ID) + " ($" + cand.EnvKey + ") is not registered with gofer"
			continue
		case err != nil:
			res.Reason = "session lookup failed: " + err.Error()
			return res
		case d.Session.EndedAt > 0 || d.Session.State == "ended":
			res.Reason = "session " + shortID(cand.ID) + " ($" + cand.EnvKey + ") has ended"
			continue
		case project != "" && d.Session.ProjectKey != project:
			res.Reason = "session " + shortID(cand.ID) + " belongs to project " + quoteOrNone(d.Session.ProjectKey) + ", not " + project
			continue
		case config.NormalizeRunnerName(d.Session.Runner) != config.BuiltinLocalRunner:
			// A bound plan's todo dispatch carries the session as its trusted source,
			// and the server only accepts a source session running on the server's
			// own runner (job.validateSourceSession): binding a worker-side session
			// would make every dispatch of the plan fail.
			res.Reason = "session " + shortID(cand.ID) + " runs on runner " + quoteOrNone(d.Session.Runner) +
				" — a bound plan can only dispatch for a session on the server's own runner"
			continue
		}
		return SupervisorAutoBind{Session: d.Session, EnvKey: cand.EnvKey}
	}
	return res
}

// CreatePlanAutoSupervisor creates a plan bound to the agent session the caller
// runs in (LookupCurrentAgentSession). When no session is found, or the server
// refuses the binding (session not owned by this caller / no authenticated
// caller), the plan is created unbound and the result's Reason says why — the
// binding never fails the plan.
func (c *Client) CreatePlanAutoSupervisor(planID, title, description, project, leader string, tags []string, getenv func(string) string) (Plan, SupervisorAutoBind, error) {
	bind := c.LookupCurrentAgentSession(getenv, project)
	if !bind.Bound() {
		p, err := c.CreatePlanWithSupervisorSession(planID, title, description, project, leader, "", tags)
		return p, bind, err
	}
	p, err := c.CreatePlanWithSupervisorSession(planID, title, description, project, leader, bind.Session.SessionID, tags)
	if err == nil || !supervisorBindRefused(err) {
		return p, bind, err
	}
	unbound := SupervisorAutoBind{EnvKey: bind.EnvKey,
		Reason: "session " + shortID(bind.Session.SessionID) + " was refused as supervisor (" + err.Error() + ")"}
	p, err = c.CreatePlanWithSupervisorSession(planID, title, description, project, leader, "", tags)
	return p, unbound, err
}

// supervisorBindRefused reports a create rejected only because of the
// supervisor_session_id (validatePlanSupervisorSession / missing caller).
func supervisorBindRefused(err error) bool {
	switch StatusOf(err) {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return strings.Contains(err.Error(), "supervisor")
	}
	return false
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func quoteOrNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

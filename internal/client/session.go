package client

import (
	"net/http"
	"strings"
)

// 会话 id 的携带（设计 docs/design/2026-10-14-db-capabilities-and-session-grants-design.md §4
// 「会话 id 的携带」，计划 T2.2）。
//
// A session grant is judged per request: the server needs to know WHICH terminal session a request
// came from, and it takes that from one header. The CLI therefore has exactly ONE place that puts
// credentials on an outgoing request (Client.applyAuth) — the bearer token and the session header
// together — and every request builder of this package goes through it: the plain JSON requests,
// the job submit, the SSE stream, the worker-upgrade upload, the file transfers and the tunnel
// websocket dial. Before this, five files each set an Authorization header of their own, so a new
// credential would have had to be added five times (and would certainly have been forgotten
// somewhere).

// SessionHeader is the request header that names the terminal agent session a request belongs to
// (design §4「会话 id 的携带」).
const SessionHeader = "X-Gofer-Session-Id"

// ExplicitSessionEnv is the environment variable a caller sets to name its session itself. It wins
// over every agent variable below, because a person (or a wrapper) saying which session this is
// knows better than a heuristic.
const ExplicitSessionEnv = "GOFER_SESSION_ID"

// SessionIDFromEnv resolves the terminal session a CLI invocation names and the variable the id
// came from ("" when nothing is attached), following the rule of design §4:
//
//   - $SUGA_SESSION_ID, when set, is used as it is, before everything else (see
//     AgentSessionEnvKeys: suga sets it itself, so it beats an inherited GOFER_SESSION_ID);
//   - an explicitly set $GOFER_SESSION_ID is used as it is;
//   - otherwise the agent session variables (AgentSessionEnvKeys) must agree on exactly ONE id;
//     with no candidate, or with two different ones, NOTHING is attached.
//
// The caution is the point: attaching the wrong session id would hand the request the grants of
// somebody else's terminal, and being silently unattached is always safe — the request then holds
// exactly what its own caller id holds. Identification looks at the environment and nothing else:
// never the working directory, never a marker file, so gofer behaves the same in every descendant
// process of a terminal and a container without an agent session in its environment attaches
// nothing.
func SessionIDFromEnv(getenv func(string) string) (id, source string) {
	if getenv == nil {
		return "", ""
	}
	if v := strings.TrimSpace(getenv(SugaSessionEnv)); v != "" {
		return v, SugaSessionEnv
	}
	if v := strings.TrimSpace(getenv(ExplicitSessionEnv)); v != "" {
		return v, ExplicitSessionEnv
	}
	cands := CurrentAgentSessionCandidates(getenv)
	if len(cands) != 1 {
		return "", ""
	}
	return cands[0].ID, cands[0].EnvKey
}

// applyAuth puts this client's credentials on an outgoing request: the bearer token, and the
// session header when the environment named a terminal session. It is the single injection point
// of the CLI (design §4: 「所有请求、隧道拨号、SSE / WS 握手都经由它加 X-Gofer-Session-Id」), so a
// new credential is one edit here and no request path can forget it.
func (c *Client) applyAuth(h http.Header) {
	if c == nil || h == nil {
		return
	}
	if c.token != "" {
		h.Set("Authorization", "Bearer "+c.token)
	}
	if c.sessionID != "" {
		h.Set(SessionHeader, c.sessionID)
	}
}

// SessionID reports the terminal session every request of this client names, and the environment
// variable the id came from ("" / "" when no session is attached). `gofer auth whoami` shows both,
// so an operator can tell a request that was attached to a session from one that was not.
func (c *Client) SessionID() (id, source string) {
	if c == nil {
		return "", ""
	}
	return c.sessionID, c.sessionSource
}

package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// SessionTopicPatch is the PATCH /v1/sessions/{sid} body: nil leaves a field unchanged,
// an empty non-nil Scope clears it.
type SessionTopicPatch struct {
	Topic *string   `json:"topic,omitempty"`
	Scope *[]string `json:"scope,omitempty"`
}

// PatchSession sets a session's topic and/or scope. The server records the source as
// "self" when this client's session header equals sid, otherwise "human"; an ended session
// is a 409 (StatusError), an unknown one 404.
func (c *Client) PatchSession(sid string, p SessionTopicPatch) (AgentSession, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return AgentSession{}, fmt.Errorf("encode session patch: %w", err)
	}
	var a AgentSession
	err = c.doJSON(http.MethodPatch, "/v1/sessions/"+url.PathEscape(sid), bytes.NewReader(body), &a)
	if StatusOf(err) == http.StatusConflict {
		return a, fmt.Errorf("session %s has ended, its topic cannot be changed (after /clear the new session is a different one; pass --session <id> to name it): %w", sid, err)
	}
	return a, err
}

// SessionHit says why a session matched a find query (kind: scope | scope_weak | topic |
// peer_name | project; Source is the topic source for scope / topic hits).
type SessionHit struct {
	Kind   string `json:"kind"`
	Match  string `json:"match,omitempty"`
	Source string `json:"source,omitempty"`
}

// SessionFound is one find result: the session plus the hit reasons.
type SessionFound struct {
	AgentSession
	Hits         []SessionHit `json:"hits"`
	MaybeOffline bool         `json:"maybe_offline,omitempty"`
}

// SessionFindOpts is one find query (GET /v1/sessions?q=&path=): Q is whitespace-split
// terms, Path must hit a scope; at least one is required.
type SessionFindOpts struct {
	Q, Path, Project string
	Limit            int
}

// FindSessions finds sessions by topic / scope (ranked by the server).
func (c *Client) FindSessions(o SessionFindOpts) ([]SessionFound, error) {
	if strings.TrimSpace(o.Q) == "" && strings.TrimSpace(o.Path) == "" {
		return nil, fmt.Errorf("find needs a keyword or a path")
	}
	q := url.Values{}
	if o.Q != "" {
		q.Set("q", o.Q)
	}
	if o.Path != "" {
		q.Set("path", o.Path)
	}
	if o.Project != "" {
		q.Set("project", o.Project)
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	var out struct {
		Sessions []SessionFound `json:"sessions"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/sessions?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

// ErrNoCurrentSession / ErrAmbiguousCurrentSession are what ResolveCurrentSessionID returns when
// the environment names no session or several different ones.
var (
	ErrNoCurrentSession        = fmt.Errorf("cannot tell which session this is: no GOFER_SESSION_ID / CLAUDE_CODE_SESSION_ID / CODEX_THREAD_ID in the environment; pass --session <id> (see `gofer session ls`)")
	ErrAmbiguousCurrentSession = fmt.Errorf("cannot tell which session this is: the environment names several different sessions; pass --session <id> (see `gofer session ls`)")
)

// ResolveCurrentSessionID names the session the calling process belongs to, from the
// environment only (SessionIDFromEnv's rule; never the working directory, never a marker
// file). No candidate or several different ones is an error that asks for an explicit id.
func ResolveCurrentSessionID(getenv func(string) string) (string, error) {
	if id, _ := SessionIDFromEnv(getenv); id != "" {
		return id, nil
	}
	if len(CurrentAgentSessionCandidates(getenv)) > 1 {
		return "", ErrAmbiguousCurrentSession
	}
	return "", ErrNoCurrentSession
}

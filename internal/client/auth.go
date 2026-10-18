package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// 权限与授权的客户端（设计「接口」一节，计划 T2.1）：`gofer auth whoami` 与 `gofer auth routes`
// 读的就是这里的三条 GET；`gofer auth grant renew` 用这里的一条 POST。授权的新增与撤销只在服务端
// /web 上有入口，CLI 不做（设计：授权在 web 上管理）——CLI 只做续期（`RenewAuthGrant`），因为
// 「快到期了，再给一段」是不想开浏览器时最常要做的事。

// AuthPermission is one effective permission of a caller: its name, where it came from
// (root | config | db) and, when a database grant is behind it, which grant.
type AuthPermission struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	GrantID string `json:"grant_id,omitempty"`
}

// AuthWhoami is GET /v1/auth/whoami: the caller this credential authenticates as, whether it is
// a root administrator, the permissions it holds with their sources, and the session the request
// was attached to. Roots and Hint answer the next question — who can grant me this, and what is
// wrong when nobody can.
type AuthWhoami struct {
	CallerID string `json:"caller_id"`
	// CallerName is the configured display name of the caller (empty when none).
	CallerName string `json:"caller_name,omitempty"`
	// CallerNames maps caller id to display name for every named caller.
	CallerNames map[string]string `json:"caller_names,omitempty"`
	// Kind is user | worker | job.
	Kind string `json:"kind"`
	// Root reports the root administrator: it holds every permission.
	Root bool `json:"root"`
	// Permissions is the effective set, in catalog order.
	Permissions []AuthPermission `json:"permissions"`
	// SessionID is the session the request named, when it named one.
	SessionID string `json:"session_id,omitempty"`
	// GrantsVersion is the generation of the grant table the answer was computed from.
	GrantsVersion int64 `json:"grants_version"`
	// Roots lists the deployment's root administrators (empty: nobody can grant anything).
	Roots []string `json:"roots,omitempty"`
	// Hint explains an empty or unattached answer.
	Hint string `json:"hint,omitempty"`
}

// AuthCatalogEntry is one permission of GET /v1/auth/catalog (design §1: name, what it covers,
// risk, whether it may be granted to a session, scope, source).
type AuthCatalogEntry struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	Risk             string `json:"risk"`
	RiskLabel        string `json:"risk_label"`
	SessionGrantable bool   `json:"session_grantable"`
	Scope            string `json:"scope"`
	ScopeLabel       string `json:"scope_label"`
	Source           string `json:"source"`
	SourceLabel      string `json:"source_label"`
	SeedVersion      int    `json:"seed_version,omitempty"`
}

// AuthPreset is one quick-fill of the design's preset list: the patterns a console fills in and
// what they really hand out.
type AuthPreset struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Patterns    []string `json:"patterns"`
	Permissions []string `json:"permissions"`
}

// AuthCatalog is GET /v1/auth/catalog.
type AuthCatalog struct {
	Permissions []AuthCatalogEntry `json:"permissions"`
	Presets     []AuthPreset       `json:"presets"`
}

// AuthRoute is one route of GET /v1/auth/routes: the route and the declaration it carries —
// User is the user-class category (require | in_handler | public | no_user_surface | pending) and
// Mode is InHandler's mode (deny | filter).
type AuthRoute struct {
	Method        string   `json:"method"`
	Path          string   `json:"path"`
	User          string   `json:"user"`
	Mode          string   `json:"mode,omitempty"`
	Permissions   []string `json:"permissions,omitempty"`
	JobCredential string   `json:"job_credential,omitempty"`
	WorkerSelf    bool     `json:"worker_self,omitempty"`
	External      string   `json:"external,omitempty"`
	Static        bool     `json:"static,omitempty"`
}

// AuthGrant is one grant as the management routes report it — the answer of a renewal is the same
// row with its deadline moved: its id, the subject it grants to, the state it is in, and whether it
// is in force right now (设计 §4「生效条件」).
type AuthGrant struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	// Source is db | config.
	Source string `json:"source"`
	// Pattern is the granted pattern (with its optional scope), e.g. tunnel.*:project-a.
	Pattern string `json:"pattern,omitempty"`
	Scope   string `json:"scope,omitempty"`
	// State is active | orphaned | revoked | expired | unmatched.
	State string `json:"state"`
	// Effective is whether the grant is in force right now: for a session grant that is the state
	// plus the deadline plus the session's own facts (not ended, owner and incarnation unchanged).
	Effective bool `json:"effective"`
	// ExpiresAt is the unix-second deadline. Only a session grant has one: for a users / caller
	// grant the field is absent (0 here).
	ExpiresAt int64  `json:"expires_at,omitempty"`
	GrantedBy string `json:"granted_by,omitempty"`
	GrantedAt int64  `json:"granted_at,omitempty"`
	// Permissions is what the pattern expands to today, in catalog order.
	Permissions []string `json:"permissions,omitempty"`
}

// RenewAuthGrant pushes back the deadline of a session grant that is still in force
// (POST /v1/auth/grants/{id}/renew): the new deadline is now + expiresSec, which the server caps at
// 24 hours. Only the deadline moves — the pattern, its expansion snapshot, the grantor, the session
// owner and the incarnation all stay, so a renewal can neither widen nor move a grant. A grant that
// expired, was revoked, or whose session ended is refused (409) and the error carries the server's
// reason: those need a new grant, not a longer one.
func (c *Client) RenewAuthGrant(id string, expiresSec int64) (AuthGrant, error) {
	body, err := json.Marshal(map[string]int64{"expires_sec": expiresSec})
	if err != nil {
		return AuthGrant{}, fmt.Errorf("encode renew request: %w", err)
	}
	var out AuthGrant
	err = c.doJSON(http.MethodPost, "/v1/auth/grants/"+url.PathEscape(id)+"/renew", bytes.NewReader(body), &out)
	if err != nil {
		return AuthGrant{}, err
	}
	return out, nil
}

// Whoami asks GET /v1/auth/whoami. The request carries the terminal session this client names
// (SessionIDFromEnv, attached by the single injection point of the client package), so the answer
// is the decision the server really makes for a request from this terminal.
func (c *Client) Whoami() (AuthWhoami, error) {
	var out AuthWhoami
	if err := c.doJSON(http.MethodGet, "/v1/auth/whoami", nil, &out); err != nil {
		return AuthWhoami{}, err
	}
	return out, nil
}

// AuthCatalog fetches the permission catalog (GET /v1/auth/catalog).
func (c *Client) AuthCatalog() (AuthCatalog, error) {
	var out AuthCatalog
	if err := c.doJSON(http.MethodGet, "/v1/auth/catalog", nil, &out); err != nil {
		return AuthCatalog{}, err
	}
	return out, nil
}

// AuthRoutes fetches the route declarations of the running server (GET /v1/auth/routes).
func (c *Client) AuthRoutes() ([]AuthRoute, error) {
	var out struct {
		Routes []AuthRoute `json:"routes"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/auth/routes", nil, &out); err != nil {
		return nil, err
	}
	return out.Routes, nil
}

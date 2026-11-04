package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// 用户管理的客户端（配置入库 P1，设计 docs/design/2026-11-03-config-into-db-design.md，计划 T7）：
// `/v1/callers*` 的那八条路由。CLI 是瘦客户端（计划的 T7）：token 由 server 生成，用户行、审计与
// 生效都在 server 侧，所以这里只有请求与响应。

// Caller is one caller as GET /v1/callers lists it: where it comes from (file | db), whether it
// is in force (active | disabled | superseded), the credential hint and what it is limited to.
// The token itself is never part of a list.
type Caller struct {
	ID                string         `json:"id"`
	Name              string         `json:"name,omitempty"`
	Source            string         `json:"source"`
	State             string         `json:"state"`
	TokenSet          bool           `json:"token_set"`
	TokenHint         string         `json:"token_hint,omitempty"`
	MaxConcurrentJobs int            `json:"max_concurrent_jobs,omitempty"`
	RateLimit         float64        `json:"rate_limit,omitempty"`
	RateBurst         int            `json:"rate_burst,omitempty"`
	Ceiling           *CallerCeiling `json:"ceiling,omitempty"`
	CreatedBy         string         `json:"created_by,omitempty"`
	CreatedAt         int64          `json:"created_at,omitempty"`
	RotatedAt         int64          `json:"rotated_at,omitempty"`
	UpdatedAt         int64          `json:"updated_at,omitempty"`
	Warnings          []string       `json:"warnings,omitempty"`
}

// CallerCeiling is a caller's upper bound on the wire. An omitted list does not restrict that
// dimension, EXCEPT runners: an omitted runners list means the machine runner is not allowed
// (worker runners only). A nil switch means the key was not set (the strict default applies).
type CallerCeiling struct {
	Projects     []string `json:"projects,omitempty"`
	Runners      []string `json:"runners,omitempty"`
	Workers      []string `json:"workers,omitempty"`
	Agents       []string `json:"agents,omitempty"`
	AllowExec    *bool    `json:"allow_exec,omitempty"`
	GlobalMemory *bool    `json:"global_memory,omitempty"`
	ReadOnly     *bool    `json:"read_only,omitempty"`
}

// CallerChange is the answer of a caller write: the caller as it is now, the token when the
// write minted one (add / rotate — the only two answers that carry it), and the notices.
type CallerChange struct {
	Caller   Caller   `json:"caller"`
	Token    string   `json:"token,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// CallerAddRequest is the body of POST /v1/callers: exactly one of Ceiling / NoCeiling.
type CallerAddRequest struct {
	ID                string         `json:"id"`
	Name              string         `json:"name,omitempty"`
	MaxConcurrentJobs int            `json:"max_concurrent_jobs,omitempty"`
	RateLimit         float64        `json:"rate_limit,omitempty"`
	RateBurst         int            `json:"rate_burst,omitempty"`
	Ceiling           *CallerCeiling `json:"ceiling,omitempty"`
	NoCeiling         bool           `json:"no_ceiling,omitempty"`
}

// CallerCeilingRequest is the body of PUT /v1/callers/{id}/ceiling.
type CallerCeilingRequest struct {
	Ceiling   *CallerCeiling `json:"ceiling,omitempty"`
	NoCeiling bool           `json:"no_ceiling,omitempty"`
}

// CallerLimitsRequest is the body of PUT /v1/callers/{id}/limits.
type CallerLimitsRequest struct {
	MaxConcurrentJobs int     `json:"max_concurrent_jobs,omitempty"`
	RateLimit         float64 `json:"rate_limit,omitempty"`
	RateBurst         int     `json:"rate_burst,omitempty"`
}

// CallerAuditEntry is one config_audit line of a caller change: who, when, which entry, and the
// masked before / after values (never a token, never its hash).
type CallerAuditEntry struct {
	ID           int64  `json:"id,omitempty"`
	Actor        string `json:"actor"`
	Path         string `json:"path"`
	BeforeMasked string `json:"before_masked,omitempty"`
	AfterMasked  string `json:"after_masked,omitempty"`
	At           int64  `json:"at"`
}

// ListCallers fetches GET /v1/callers.
func (c *Client) ListCallers() ([]Caller, error) {
	var out struct {
		Callers []Caller `json:"callers"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/callers", nil, &out); err != nil {
		return nil, err
	}
	return out.Callers, nil
}

// AddCaller creates a caller (POST /v1/callers) and answers the credential ONCE.
func (c *Client) AddCaller(req CallerAddRequest) (CallerChange, error) {
	return c.postCallerChange("/v1/callers", req)
}

// RotateCaller replaces a caller's token (POST /v1/callers/{id}/rotate).
func (c *Client) RotateCaller(id string) (CallerChange, error) {
	return c.postCallerChange("/v1/callers/"+url.PathEscape(id)+"/rotate", nil)
}

// DisableCaller switches a caller off (POST /v1/callers/{id}/disable).
func (c *Client) DisableCaller(id string) (CallerChange, error) {
	return c.postCallerChange("/v1/callers/"+url.PathEscape(id)+"/disable", nil)
}

// EnableCaller brings a caller back (POST /v1/callers/{id}/enable).
func (c *Client) EnableCaller(id string) (CallerChange, error) {
	return c.postCallerChange("/v1/callers/"+url.PathEscape(id)+"/enable", nil)
}

// SetCallerCeiling replaces (or, with NoCeiling, removes) a caller's ceiling.
func (c *Client) SetCallerCeiling(id string, req CallerCeilingRequest) (CallerChange, error) {
	return c.callersDo(http.MethodPut, "/v1/callers/"+url.PathEscape(id)+"/ceiling", req)
}

// SetCallerLimits replaces a caller's quota overrides.
func (c *Client) SetCallerLimits(id string, req CallerLimitsRequest) (CallerChange, error) {
	return c.callersDo(http.MethodPut, "/v1/callers/"+url.PathEscape(id)+"/limits", req)
}

// ListCallerAudit fetches GET /v1/callers/audit?limit=.
func (c *Client) ListCallerAudit(limit int) ([]CallerAuditEntry, error) {
	path := "/v1/callers/audit"
	if limit > 0 {
		path += fmt.Sprintf("?limit=%d", limit)
	}
	var out struct {
		Entries []CallerAuditEntry `json:"entries"`
	}
	if err := c.doJSON(http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Entries, nil
}

// postCallerChange posts a no-body-parameter caller action.
func (c *Client) postCallerChange(path string, body any) (CallerChange, error) {
	return c.callersDo(http.MethodPost, path, body)
}

// callersDo sends one caller request and decodes the change it answers.
func (c *Client) callersDo(method, path string, body any) (CallerChange, error) {
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return CallerChange{}, fmt.Errorf("encode the caller request: %w", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	var out CallerChange
	if err := c.doJSON(method, path, reader, &out); err != nil {
		return CallerChange{}, err
	}
	return out, nil
}

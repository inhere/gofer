package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// PermissionSuggestionLabel is one "always allow" choice of a permission prompt
// (only its human label travels to the server).
type PermissionSuggestionLabel struct {
	Label string `json:"label"`
}

// SessionPermission is a terminal permission prompt the hook opens on the web
// (POST /v1/sessions/{sid}/permissions).
type SessionPermission struct {
	ToolName    string                      `json:"tool_name"`
	Summary     string                      `json:"summary"`
	Input       string                      `json:"input,omitempty"`
	Suggestions []PermissionSuggestionLabel `json:"suggestions,omitempty"`
	Fingerprint string                      `json:"fp,omitempty"`
	TimeoutSec  int64                       `json:"timeout_sec,omitempty"`
}

// OpenSessionPermission opens a permission decision the hook then long-polls with
// WaitSessionTurn. 409 = the session does not wait for the web right now.
func (c *Client) OpenSessionPermission(sid string, p SessionPermission) (Decision, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return Decision{}, fmt.Errorf("encode permission: %w", err)
	}
	var d Decision
	err = c.doJSON(http.MethodPost, "/v1/sessions/"+url.PathEscape(sid)+"/permissions", bytes.NewReader(body), &d)
	return d, err
}

// ResolveSessionPermission reports that the prompt of tool call fp ("" = every
// pending prompt) was settled in the terminal; it returns how many were closed.
func (c *Client) ResolveSessionPermission(sid, fp string) (int, error) {
	body, err := json.Marshal(map[string]string{"fp": fp})
	if err != nil {
		return 0, fmt.Errorf("encode resolve permission: %w", err)
	}
	var out struct {
		Resolved int `json:"resolved"`
	}
	err = c.doJSON(http.MethodPost, "/v1/sessions/"+url.PathEscape(sid)+"/permissions/resolve", bytes.NewReader(body), &out)
	return out.Resolved, err
}

// AnswerSessionPermission answers a pending prompt: allow | always:<i> | deny[:<reason>].
func (c *Client) AnswerSessionPermission(sid, id, answer string) (Decision, error) {
	body, err := json.Marshal(map[string]string{"answer": answer})
	if err != nil {
		return Decision{}, fmt.Errorf("encode permission answer: %w", err)
	}
	var d Decision
	err = c.doJSON(http.MethodPost, "/v1/sessions/"+url.PathEscape(sid)+"/permissions/"+url.PathEscape(id)+"/answer",
		bytes.NewReader(body), &d)
	return d, err
}

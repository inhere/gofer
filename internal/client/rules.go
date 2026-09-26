package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/inhere/gofer/internal/rule"
)

// rules.go is the JOB-06① client for /v1/rules*: the rule library's list, show, write
// and remove. The library lives on the server next to its config; a client node
// reaches it through these calls, which is what makes `gofer agent rule …` work
// identically over HTTP and against a local config (the dual mode the CLI switches on).

// RuleView is one rule as GET /v1/rules/{name} returns it: the index metadata plus the
// rule text, so `show` needs one request rather than two.
type RuleView struct {
	rule.Rule
	// Content is the stored file verbatim (frontmatter included).
	Content string `json:"content,omitempty"`
}

// RuleList lists the library (GET /v1/rules). The server always answers with an array,
// but a null body is normalised to an empty slice so a caller may range and len()
// without a nil check.
func (c *Client) RuleList() ([]rule.Rule, error) {
	var out struct {
		Rules []rule.Rule `json:"rules"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/rules", nil, &out); err != nil {
		return nil, err
	}
	if out.Rules == nil {
		out.Rules = []rule.Rule{}
	}
	return out.Rules, nil
}

// RuleShow fetches one rule together with its text (GET /v1/rules/{name}).
func (c *Client) RuleShow(name string) (RuleView, error) {
	var out RuleView
	err := c.doJSON(http.MethodGet, "/v1/rules/"+url.PathEscape(name), nil, &out)
	return out, err
}

// RuleSet creates or replaces a rule (PUT /v1/rules/{name}) with the text as the JSON
// `content` field. A rule is small by design (server.rules_max_bytes), so the body is
// buffered rather than streamed.
func (c *Client) RuleSet(name string, content []byte) (RuleView, error) {
	payload, err := json.Marshal(map[string]string{"content": string(content)})
	if err != nil {
		return RuleView{}, fmt.Errorf("encode rule: %w", err)
	}
	var out RuleView
	if err := c.doJSON(http.MethodPut, "/v1/rules/"+url.PathEscape(name), bytes.NewReader(payload), &out); err != nil {
		return RuleView{}, err
	}
	return out, nil
}

// RuleRemove deletes a rule (DELETE /v1/rules/{name}, 204).
func (c *Client) RuleRemove(name string) error {
	return c.doJSON(http.MethodDelete, "/v1/rules/"+url.PathEscape(name), nil, nil)
}

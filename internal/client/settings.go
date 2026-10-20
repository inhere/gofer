package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Extension settings (docs/design/2026-10-20-work-noise-and-db-settings-design.md):
// the runtime-adjustable knobs `GET/PUT/DELETE /v1/settings[/{key}]` serve. The CLI
// (`gofer settings`) is a pure HTTP client of that API — it never opens the store, so
// a `settings set` on a client node reaches the same database a `serve` owns.

// SettingItem is one registered setting: its type and description, the registered
// default, the effective value (stored, else default) and whether it was modified.
// Value stays untyped because the key's registered kind decides what it holds
// (string / number / bool / []string).
type SettingItem struct {
	Key         string `json:"key"`
	Group       string `json:"group,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Description string `json:"description,omitempty"`
	Default     any    `json:"default"`
	Value       any    `json:"value"`
	Modified    bool   `json:"modified"`
	Multiline   bool   `json:"multiline,omitempty"`
	Min         *int   `json:"min,omitempty"`
	Max         *int   `json:"max,omitempty"`
	MaxLen      int    `json:"max_len,omitempty"`
	UpdatedBy   string `json:"updated_by,omitempty"`
	UpdatedAt   int64  `json:"updated_at,omitempty"`
}

// SettingReset is the answer of DELETE /v1/settings/{key}: the value now in effect and
// whether a stored value was actually dropped.
type SettingReset struct {
	Key      string `json:"key"`
	Modified bool   `json:"modified"`
	Value    any    `json:"value"`
	Reset    bool   `json:"reset"`
}

// SettingChange is one key of an import: what the document asks for, what the store
// says, and the reason when the value was refused.
type SettingChange struct {
	Key string `json:"key"`
	// Action is "set", "reset" or "unchanged".
	Action string `json:"action"`
	Old    any    `json:"old"`
	New    any    `json:"new"`
	Error  string `json:"error,omitempty"`
}

// SettingImport is the answer of POST /v1/settings/import: the per-key changes, whether
// they were written, and how many keys were refused.
type SettingImport struct {
	DryRun  bool            `json:"dry_run"`
	Applied bool            `json:"applied"`
	Changes []SettingChange `json:"changes"`
	Errors  int             `json:"errors"`
}

// ListSettings returns every registered setting in registry order.
func (c *Client) ListSettings() ([]SettingItem, error) {
	var out struct {
		Settings []SettingItem `json:"settings"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/settings", nil, &out); err != nil {
		return nil, err
	}
	return out.Settings, nil
}

// GetSetting fetches one setting; an unregistered key is a 404.
func (c *Client) GetSetting(key string) (SettingItem, error) {
	var out SettingItem
	err := c.doJSON(http.MethodGet, "/v1/settings/"+url.PathEscape(key), nil, &out)
	return out, err
}

// SetSetting writes one setting. value is passed through as the JSON value the key's
// registered kind expects (a number, a bool, a []string), so the server validates it as
// the caller meant it rather than as a string.
func (c *Client) SetSetting(key string, value any) (SettingItem, error) {
	body, err := json.Marshal(map[string]any{"value": value})
	if err != nil {
		return SettingItem{}, fmt.Errorf("encode setting value: %w", err)
	}
	var out SettingItem
	err = c.doJSON(http.MethodPut, "/v1/settings/"+url.PathEscape(key), bytes.NewReader(body), &out)
	return out, err
}

// ResetSetting drops a stored value so the registered default applies again.
func (c *Client) ResetSetting(key string) (SettingReset, error) {
	var out SettingReset
	err := c.doJSON(http.MethodDelete, "/v1/settings/"+url.PathEscape(key), nil, &out)
	return out, err
}

// ExportSettings returns the YAML document of the MODIFIED keys only, exactly as the
// server wrote it (the CLI hands it to a file or stdout verbatim).
func (c *Client) ExportSettings() ([]byte, error) {
	resp, err := c.DoRaw(http.MethodGet, "/v1/settings/export", nil, "")
	if err != nil {
		return nil, err
	}
	if err := errorFor(resp.Status, resp.Body); err != nil {
		return nil, c.explainSugaSession(err)
	}
	return resp.Body, nil
}

// ImportSettings posts a YAML document; dryRun reports the per-key differences without
// writing anything.
func (c *Client) ImportSettings(doc []byte, dryRun bool) (SettingImport, error) {
	path := "/v1/settings/import"
	if dryRun {
		path += "?dry_run=1"
	}
	resp, err := c.DoRaw(http.MethodPost, path, doc, "application/yaml")
	if err != nil {
		return SettingImport{}, err
	}
	if err := errorFor(resp.Status, resp.Body); err != nil {
		return SettingImport{}, c.explainSugaSession(err)
	}
	var out SettingImport
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return SettingImport{}, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// APIResponse is one raw answer of the server: the status, the response headers and the
// body verbatim. It exists for callers that must see what the server actually said —
// `gofer tool api` — instead of the decoded value doJSON would give them.
type APIResponse struct {
	Status      int
	Header      http.Header
	Body        []byte
	ContentType string
}

// DoRaw sends one request and returns the answer as it is, treating no status specially:
// a non-2xx answer is data for the caller, not an error of the transport. The token and
// the session header ride along through applyAuth like on every other request, so a raw
// call carries exactly the credentials the rest of the CLI would.
func (c *Client) DoRaw(method, path string, body []byte, contentType string) (APIResponse, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.baseURL+path, rdr)
	if err != nil {
		return APIResponse{}, fmt.Errorf("build request: %w", err)
	}
	c.applyAuth(req.Header)
	if body != nil && contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json, application/yaml, text/plain;q=0.9, */*;q=0.5")
	resp, err := c.http.Do(req)
	if err != nil {
		return APIResponse{}, fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return APIResponse{}, fmt.Errorf("read response: %w", err)
	}
	return APIResponse{
		Status:      resp.StatusCode,
		Header:      resp.Header.Clone(),
		Body:        data,
		ContentType: strings.TrimSpace(resp.Header.Get("Content-Type")),
	}, nil
}

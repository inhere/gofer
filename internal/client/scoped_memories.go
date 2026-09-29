package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

type ScopedMemory struct {
	Scope     string   `json:"scope"`
	ScopeKey  string   `json:"scope_key,omitempty"`
	Key       string   `json:"key"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags,omitempty"`
	UpdatedAt string   `json:"updated_at"`
	UpdatedBy string   `json:"updated_by,omitempty"`
	Deleted   bool     `json:"deleted,omitempty"`
}

type ScopedMemoryListOpts struct {
	Scope    string
	ScopeKey string
	Keyword  string
	Tags     []string
}

func (c *Client) ListScopedMemories(opts ScopedMemoryListOpts) ([]ScopedMemory, error) {
	q := url.Values{}
	q.Set("scope", opts.Scope)
	if opts.ScopeKey != "" {
		q.Set("scope_key", opts.ScopeKey)
	}
	if opts.Keyword != "" {
		q.Set("q", opts.Keyword)
	}
	for _, tag := range opts.Tags {
		if tag != "" {
			q.Add("tag", tag)
		}
	}
	var out struct {
		Memories []ScopedMemory `json:"memories"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/memories?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out.Memories, nil
}

func (c *Client) GetScopedMemory(scope, scopeKey, key string) (ScopedMemory, error) {
	var out ScopedMemory
	err := c.doJSON(http.MethodGet, scopedMemoryPath(scope, scopeKey, key), nil, &out)
	return out, err
}

func (c *Client) PutScopedMemory(scope, scopeKey, key, content string, tags []string) (ScopedMemory, error) {
	body, err := json.Marshal(map[string]any{"content": content, "tags": tags})
	if err != nil {
		return ScopedMemory{}, fmt.Errorf("encode memory: %w", err)
	}
	var out ScopedMemory
	err = c.doJSON(http.MethodPut, scopedMemoryPath(scope, scopeKey, key), bytes.NewReader(body), &out)
	return out, err
}

func (c *Client) CreateScopedMemory(scope, scopeKey, key, content string, tags []string) (ScopedMemory, error) {
	body, err := json.Marshal(map[string]any{"scope": scope, "scope_key": scopeKey, "key": key, "content": content, "tags": tags})
	if err != nil {
		return ScopedMemory{}, fmt.Errorf("encode memory: %w", err)
	}
	var out ScopedMemory
	err = c.doJSON(http.MethodPost, "/v1/memories", bytes.NewReader(body), &out)
	return out, err
}

func (c *Client) DeleteScopedMemory(scope, scopeKey, key string) error {
	return c.doJSON(http.MethodDelete, scopedMemoryPath(scope, scopeKey, key), nil, nil)
}

func scopedMemoryPath(scope, scopeKey, key string) string {
	if scope == "global" {
		scopeKey = "_"
	}
	return "/v1/memories/" + url.PathEscape(scope) + "/" + url.PathEscape(scopeKey) + "/" + url.PathEscape(key)
}

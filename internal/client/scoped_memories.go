package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/inhere/gofer/internal/tracker"
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
	tracker.MemoryMeta
}

// TrackerMemory is the scoped memory in the local memory shape (prime / ls).
func (m ScopedMemory) TrackerMemory() tracker.Memory {
	return tracker.Memory{Key: m.Key, Content: m.Content, Tags: m.Tags, UpdatedAt: m.UpdatedAt, By: m.UpdatedBy, MemoryMeta: m.MemoryMeta}
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

// CreateScopedMemory upserts a scoped memory. A non-nil meta sends every meta
// field (the caller passes the merged record, so empty values clear); nil keeps
// the stored ones.
func (c *Client) CreateScopedMemory(scope, scopeKey, key, content string, tags []string, meta *tracker.MemoryMeta) (ScopedMemory, error) {
	payload := map[string]any{"scope": scope, "scope_key": scopeKey, "key": key, "content": content, "tags": tags}
	if meta != nil {
		when := meta.When
		if when == nil {
			when = &tracker.MemoryWhen{}
		}
		payload["kind"], payload["summary"], payload["when"], payload["expires_at"], payload["source"] = meta.Kind, meta.Summary, when, meta.ExpiresAt, meta.Source
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ScopedMemory{}, fmt.Errorf("encode memory: %w", err)
	}
	var out ScopedMemory
	err = c.doJSON(http.MethodPost, "/v1/memories", bytes.NewReader(body), &out)
	return out, err
}

// FlagScopedMemory reports a scoped memory as out of date (reason required). jobID is
// informational: a job credential is recorded as its own job by the server.
func (c *Client) FlagScopedMemory(scope, scopeKey, key, reason, jobID string) (ScopedMemory, error) {
	body, err := json.Marshal(map[string]string{"reason": reason, "job": jobID})
	if err != nil {
		return ScopedMemory{}, fmt.Errorf("encode flag: %w", err)
	}
	var out ScopedMemory
	err = c.doJSON(http.MethodPost, scopedMemoryPath(scope, scopeKey, key)+"/flag", bytes.NewReader(body), &out)
	return out, err
}

// UnflagScopedMemory clears every flag of a scoped memory (the human review).
func (c *Client) UnflagScopedMemory(scope, scopeKey, key string) (ScopedMemory, error) {
	var out ScopedMemory
	err := c.doJSON(http.MethodDelete, scopedMemoryPath(scope, scopeKey, key)+"/flag", nil, &out)
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

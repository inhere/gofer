package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestScopedMemoryCRUDAndPermissions(t *testing.T) {
	t.Parallel()
	s := newCredentialServer(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: "user"}}}, nil, nil)
	meta, err := jobstore.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	s.SetTrackerStore(meta)
	request := func(method, path, token string, body any) *httptest.ResponseRecorder {
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := request(http.MethodPost, "/v1/memories", "user", map[string]any{"scope": "global", "key": "shared", "content": "all", "tags": []string{"agent:claude"}}); rec.Code != http.StatusOK {
		t.Fatalf("set status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodGet, "/v1/memories?scope=global", "user", nil); rec.Code != http.StatusOK {
		t.Fatalf("list status=%d", rec.Code)
	}
	if rec := request(http.MethodDelete, "/v1/memories/global/_/shared", "user", nil); rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d", rec.Code)
	}
	jobToken := "gjt_unknown" // auth middleware rejects an unknown job credential; the route remains user-write only.
	if rec := request(http.MethodPost, "/v1/memories", jobToken, map[string]any{"scope": "global", "key": "blocked", "content": "no"}); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("job write status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestScopedMemoryFlagJobMayFlagNotUnflag: a job credential may flag (recorded as its own
// job, whatever the body claims) but not clear flags; a user may do both.
func TestScopedMemoryFlagJobMayFlagNotUnflag(t *testing.T) {
	t.Parallel()
	const userTok = "tok-user"
	s := newCredentialServer(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: userTok}}},
		map[string]config.AgentConfig{"exec": {Type: agent.TypeExec}}, nil)
	s.SetTrackerStore(s.jobs.Meta())
	member := submitExecJob(t, s, userTok)
	jobTok := seedJobToken(t, s, member.ID, jobstore.JobCredentialMember, "")
	if resp := do(t, s, http.MethodPost, "/v1/memories", userTok, map[string]any{"scope": "project", "scope_key": "self", "key": "verify", "content": "make test"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("set status=%d", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodPost, "/v1/memories", userTok, map[string]any{"scope": "project", "scope_key": "other", "key": "verify", "content": "x"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("set other status=%d", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodPost, "/v1/memories/project/other/verify/flag", jobTok, map[string]any{"reason": "x"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("job flag of another project status=%d, want 403", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodPost, "/v1/memories/project/self/verify/flag", jobTok, map[string]any{"reason": ""}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty reason status=%d", resp.StatusCode)
	}
	resp := do(t, s, http.MethodPost, "/v1/memories/project/self/verify/flag", jobTok, map[string]any{"reason": "renamed to make check", "job": "someone-else"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("job flag status=%d", resp.StatusCode)
	}
	var got jobstore.ScopedMemory
	decode(t, resp, &got)
	if len(got.Flags) != 1 || got.Flags[0].Job != member.ID || got.Flags[0].Reason != "renamed to make check" {
		t.Fatalf("flag = %+v", got.Flags)
	}
	if resp := do(t, s, http.MethodDelete, "/v1/memories/project/self/verify/flag", jobTok, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("job unflag status=%d, want 403", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodPost, "/v1/memories/project/self/nope/flag", userTok, map[string]any{"reason": "x"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing flag status=%d", resp.StatusCode)
	}
	resp = do(t, s, http.MethodDelete, "/v1/memories/project/self/verify/flag", userTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("user unflag status=%d", resp.StatusCode)
	}
	got = jobstore.ScopedMemory{}
	decode(t, resp, &got)
	if len(got.Flags) != 0 {
		t.Fatalf("flags not cleared: %+v", got.Flags)
	}
}

// TestScopedMemoryTitleHTTP: title is accepted by POST and PUT, returned by GET and list,
// kept by a write that omits it and rejected when too long.
func TestScopedMemoryTitleHTTP(t *testing.T) {
	t.Parallel()
	const tok = "user"
	s := newCredentialServer(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: tok}}}, nil, nil)
	meta, err := jobstore.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	s.SetTrackerStore(meta)
	decode := func(rec *http.Response, v any) {
		t.Helper()
		if err := json.NewDecoder(rec.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
	if resp := do(t, s, http.MethodPost, "/v1/memories", tok, map[string]any{"scope": "global", "key": "k", "content": "c", "title": "显示名", "summary": "一句话"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("post status=%d", resp.StatusCode)
	}
	var got jobstore.ScopedMemory
	decode(do(t, s, http.MethodGet, "/v1/memories/global/_/k", tok, nil), &got)
	if got.Title != "显示名" || got.Summary != "一句话" {
		t.Fatalf("get: %+v", got)
	}
	// PUT without title keeps it; with title replaces it.
	if resp := do(t, s, http.MethodPut, "/v1/memories/global/_/k", tok, map[string]any{"content": "c2"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("put status=%d", resp.StatusCode)
	}
	decode(do(t, s, http.MethodGet, "/v1/memories/global/_/k", tok, nil), &got)
	if got.Title != "显示名" {
		t.Fatalf("title dropped by a write without it: %+v", got)
	}
	if resp := do(t, s, http.MethodPut, "/v1/memories/global/_/k", tok, map[string]any{"content": "c3", "title": "新名"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("put title status=%d", resp.StatusCode)
	}
	var list struct {
		Memories []jobstore.ScopedMemory `json:"memories"`
	}
	decode(do(t, s, http.MethodGet, "/v1/memories?scope=global", tok, nil), &list)
	if len(list.Memories) != 1 || list.Memories[0].Title != "新名" {
		t.Fatalf("list: %+v", list.Memories)
	}
	if resp := do(t, s, http.MethodPost, "/v1/memories", tok, map[string]any{"scope": "global", "key": "bad", "content": "c", "title": strings.Repeat("长", 81)}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("long title status=%d, want 400", resp.StatusCode)
	}
}

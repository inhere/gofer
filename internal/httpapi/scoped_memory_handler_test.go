package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

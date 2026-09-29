package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestScopedMemoryCRUDAndPermissions(t *testing.T) {
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

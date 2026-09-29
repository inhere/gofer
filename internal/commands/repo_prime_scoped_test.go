package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrimeInjectsGlobalAndProjectMemories(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/memories" {
			items := []map[string]any{{"scope": "global", "key": "shared", "content": "visible"}, {"scope": "global", "key": "claude-only", "content": "claude", "tags": []string{"agent:claude"}}, {"scope": "global", "key": "codex-only", "content": "codex", "tags": []string{"agent:codex"}}}
			if r.URL.Query().Get("scope") == "project" {
				items = []map[string]any{{"scope": "project", "scope_key": "proj", "key": "project-note", "content": "project"}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"memories": items})
			return
		}
		if r.URL.Path == "/v1/plans" {
			_ = json.NewEncoder(w).Encode(map[string]any{"plans": []any{}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	t.Setenv("GOFER_SERVER_ADDR", srv.URL)
	body, err := primeWithServerContext(nil, "", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "shared") || !strings.Contains(body, "claude-only") || strings.Contains(body, "codex-only") {
		t.Fatalf("prime body=%q", body)
	}
}

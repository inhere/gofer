package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
)

func TestPrimeInjectsGlobalAndProjectMemories(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/memories" {
			items := []map[string]any{{"scope": "global", "key": "shared", "content": "visible first\nhidden second"}, {"scope": "global", "key": "claude-only", "content": "claude first\nclaude second", "tags": []string{"agent:claude"}}, {"scope": "global", "key": "codex-only", "content": "codex", "tags": []string{"agent:codex"}}}
			if r.URL.Query().Get("scope") == "project" {
				items = []map[string]any{{"scope": "project", "scope_key": "proj", "key": "project-note", "content": "project first\nproject second", "tags": []string{"prime"}}}
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
	if !strings.Contains(body, "shared: visible first") || strings.Contains(body, "hidden second") || !strings.Contains(body, "claude first\nclaude second") || strings.Contains(body, "codex-only") || !strings.Contains(body, "全文：`gofer memory show <key>`") {
		t.Fatalf("prime body=%q", body)
	}
}

func TestScopedPrimeTagAndSummaryLimit(t *testing.T) {
	items := []client.ScopedMemory{
		{Key: "project-prime", Content: "first\nsecond", Tags: []string{"prime"}},
		{Key: "for-claude", Content: "one\ntwo", Tags: []string{"agent:claude"}},
		{Key: "summary", Content: "short\nprivate"},
		{Key: "omitted", Content: "too many summaries"},
		{Key: "for-codex", Content: "wrong agent", Tags: []string{"agent:codex"}},
	}
	got := scopedPrimeSection("## 项目记忆\n\n", items, "claude", 1)
	if !strings.Contains(got, "project-prime: first\nsecond") || !strings.Contains(got, "for-claude: one\ntwo") || !strings.Contains(got, "summary: short") || strings.Contains(got, "private") || strings.Contains(got, "omitted") || strings.Contains(got, "for-codex") {
		t.Fatalf("scoped prime tag/summary rule: %q", got)
	}
}

func TestPrimeKeepsMemoriesWhenPlanEndpointUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/memories" {
			_ = json.NewEncoder(w).Encode(map[string]any{"memories": []map[string]any{{"scope": "global", "key": "shared", "content": "visible"}}})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	t.Setenv("GOFER_SERVER_ADDR", srv.URL)
	body, err := primeWithServerContext(nil, "", "claude")
	if err != nil || !strings.Contains(body, "shared") {
		t.Fatalf("prime body=%q err=%v", body, err)
	}
}

package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/tracker"
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
	if !strings.Contains(body, "] shared · visible first") || strings.Contains(body, "hidden second") || !strings.Contains(body, "claude first\nclaude second") || strings.Contains(body, "codex-only") || !strings.Contains(body, "按需 `gofer memory show <key>`") {
		t.Fatalf("prime body=%q", body)
	}
}

func TestScopedPrimeTagAndSummaryLimit(t *testing.T) {
	items := []client.ScopedMemory{
		{Key: "project-prime", Content: "first\nsecond", Tags: []string{"prime"}},
		{Key: "for-claude", Content: "one\ntwo", Tags: []string{"agent:claude"}},
		{Key: "summary", Content: "short\nprivate", UpdatedAt: "2026-09-30T00:00:00Z"},
		{Key: "omitted", Content: "too many summaries", UpdatedAt: "2026-09-01T00:00:00Z"},
		{Key: "for-codex", Content: "wrong agent", Tags: []string{"agent:codex"}},
	}
	got := tracker.RenderScopedPrimeSection("项目记忆", scopedTrackerMemories(items), tracker.ScopedPrimeOptions{AgentName: "claude", SummaryLimit: 1, Budget: -1, LsHint: "gofer memory ls --project p"})
	if !strings.Contains(got, "project-prime: first\nsecond") || !strings.Contains(got, "for-claude: one\ntwo") || !strings.Contains(got, "] summary · short") || strings.Contains(got, "private") || strings.Contains(got, "omitted") || strings.Contains(got, "for-codex") || !strings.Contains(got, "另有 1 条：`gofer memory ls --project p <关键字>`") {
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

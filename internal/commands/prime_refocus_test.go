package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/tracker"
)

func TestPrimeRefocusSource(t *testing.T) {
	cases := []struct {
		source           string
		enabled, resumed bool
	}{
		{"compact", true, false},
		{" Compact ", true, false},
		{"resume", true, true},
		{"startup", false, false},
		{"clear", false, false},
		{"", false, false},
		{"something-new", false, false},
	}
	for _, tc := range cases {
		enabled, resumed := primeRefocusSource(tc.source)
		if enabled != tc.enabled || resumed != tc.resumed {
			t.Fatalf("source %q: got enabled=%v resumed=%v, want %v/%v", tc.source, enabled, resumed, tc.enabled, tc.resumed)
		}
	}
}

func TestParsePrimeHookStdin(t *testing.T) {
	sid, source := parsePrimeHookStdin([]byte(`{"session_id":"sess-1","source":"compact","hook_event_name":"SessionStart"}`))
	if sid != "sess-1" || source != "compact" {
		t.Fatalf("got sid=%q source=%q", sid, source)
	}
	for _, bad := range [][]byte{nil, []byte(""), []byte("not json")} {
		if sid, source := parsePrimeHookStdin(bad); sid != "" || source != "" {
			t.Fatalf("malformed stdin %q gave sid=%q source=%q", bad, sid, source)
		}
	}
}

// refocusStore builds a tracker store holding one issue per entry.
func refocusStore(t *testing.T, issues ...tracker.Issue) *tracker.Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".gofer", "tracker")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := tracker.NewStore(dir)
	if err := s.WriteIssues(issues); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRefocusClaims(t *testing.T) {
	// The session started at 10:00; the caller is alice.
	started := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC).Unix()
	s := refocusStore(t,
		tracker.Issue{ID: "g-1", Title: "assigned to me", Status: "in_progress", Assignee: "alice", UpdatedAt: "2026-10-22T09:00:00Z"},
		tracker.Issue{ID: "g-2", Title: "touched while running", Status: "in_progress", UpdatedAt: "2026-10-22T11:00:00Z"},
		tracker.Issue{ID: "g-3", Title: "stale claim", Status: "in_progress", UpdatedAt: "2026-10-20T11:00:00Z"},
		tracker.Issue{ID: "g-4", Title: "not claimed", Status: "open", UpdatedAt: "2026-10-22T11:00:00Z"},
		tracker.Issue{ID: "g-5", Title: "closed", Status: "closed", UpdatedAt: "2026-10-22T11:00:00Z"},
	)
	claims := refocusClaims(s, client.SessionFocus{Caller: "alice", StartedAt: started})
	if len(claims) != 2 {
		t.Fatalf("expected the direct claim and the recent one, got %+v", claims)
	}
	if claims[0].ID != "g-1" || claims[0].Inferred {
		t.Fatalf("caller-assigned issue must be a direct claim: %+v", claims[0])
	}
	if claims[1].ID != "g-2" || !claims[1].Inferred {
		t.Fatalf("issue touched after the session start must be marked inferred: %+v", claims[1])
	}
}

func TestRefocusClaimsWithoutStore(t *testing.T) {
	if claims := refocusClaims(nil, client.SessionFocus{StartedAt: 1}); len(claims) != 0 {
		t.Fatalf("no tracker must yield no claims, got %+v", claims)
	}
}

func TestPrimeRefocusSectionRendersSessionFocus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/sess-a/focus" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session_id": "sess-a", "caller": "alice", "topic": "gofer 开发",
			"plans": []map[string]any{{"plan_id": "p-1", "title": "会话收尾", "status": "open", "progress": 1, "total": 2, "next": "写文档"}},
			"mail":  []map[string]any{{"id": 7, "from": "bob", "subject": "先看这个"}},
		})
	}))
	defer srv.Close()
	t.Setenv("GOFER_SERVER_ADDR", srv.URL)

	s := refocusStore(t, tracker.Issue{ID: "g-1", Title: "会话压缩后重注入", Status: "in_progress", Assignee: "alice", UpdatedAt: "2026-10-22T09:00:00Z"})
	section := primeRefocusSection(s, "", "sess-a", "compact", 4000)
	for _, want := range []string{
		"[gofer · 提示] 上下文刚被压缩",
		"主题：gofer 开发",
		"→ gofer issue brief g-1",
		"→ gofer plan brief p-1",
		"→ gofer mail show 7",
	} {
		if !strings.Contains(section, want) {
			t.Fatalf("section missing %q: %s", want, section)
		}
	}
	if strings.Contains(section, "[可能]") {
		t.Fatalf("a caller-assigned claim must not be marked probable: %s", section)
	}

	if resumed := primeRefocusSection(s, "", "sess-a", "resume", 4000); !strings.Contains(resumed, "会话刚恢复") {
		t.Fatalf("resume wording missing: %s", resumed)
	}
}

func TestPrimeRefocusSectionSkips(t *testing.T) {
	for _, source := range []string{"startup", "clear", "", "unknown"} {
		if out := primeRefocusSection(nil, "", "sess-a", source, 4000); out != "" {
			t.Fatalf("source %q must not add a section, got: %s", source, out)
		}
	}
	// No server address at all: the re-injection is skipped, the rest of prime is not.
	t.Setenv("GOFER_SERVER_ADDR", "")
	if out := primeRefocusSection(nil, "", "sess-a", "compact", 4000); out != "" {
		t.Fatalf("an unreachable server must skip the section, got: %s", out)
	}
}

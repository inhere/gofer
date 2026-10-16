package mcpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
)

func resultText(t *testing.T, args map[string]any, name string, srvURL string) (bool, string) {
	t.Helper()
	s := connectTo(t, newServer(NewClientBackend(client.New(srvURL, "tok")), ""))
	res := callTool(t, s, name, args)
	var sb strings.Builder
	for _, c := range res.Content {
		b, _ := json.Marshal(c)
		sb.Write(b)
	}
	return res.IsError, sb.String()
}

// Standalone mode has no relay: both tools answer the needs-server error.
func TestSessionTopicToolsStandaloneNeedServer(t *testing.T) {
	s := connectScoped(t, "")
	res, err := s.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range res.Tools {
		have[tl.Name] = true
	}
	if !have["gofer_session_set_topic"] || !have["gofer_session_find"] {
		t.Fatalf("tools missing: %v", have)
	}
	for name, args := range map[string]map[string]any{
		"gofer_session_set_topic": {"topic": "x", "session_id": "s1"},
		"gofer_session_find":      {"query": "x"},
	} {
		r := callTool(t, s, name, args)
		b, _ := json.Marshal(r.Content)
		if !r.IsError || !strings.Contains(string(b), "needs a running gofer server") {
			t.Fatalf("%s: isErr=%v %s", name, r.IsError, b)
		}
	}
}

func TestSessionSetTopicToolUsesEnvAndSurfacesEnded(t *testing.T) {
	var gotPath, gotBody, gotHeader string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotHeader = r.Method+" "+r.URL.Path, r.Header.Get(client.SessionHeader)
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"session_id":"sess-env","agent":"claude","state":"running","topic":"t","topic_source":"self","relay_mode":"auto","peer_messaging":false,"turn_no":0,"last_seen_at":1,"started_at":1,"auto_armed":false,"idle_sec":0}`))
			return
		}
		_, _ = w.Write([]byte(`{"error":"patch session failed","detail":"session ended"}`))
	}))
	defer srv.Close()

	t.Setenv("GOFER_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("CODEX_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-env")
	isErr, text := resultText(t, map[string]any{"topic": "修复登录", "scope": []string{"web/src"}}, "gofer_session_set_topic", srv.URL)
	if isErr || gotPath != "PATCH /v1/sessions/sess-env" || gotHeader != "sess-env" ||
		gotBody != `{"topic":"修复登录","scope":["web/src"]}` {
		t.Fatalf("isErr=%v %s path=%q hdr=%q body=%q", isErr, text, gotPath, gotHeader, gotBody)
	}

	// clear_scope sends an empty array.
	resultText(t, map[string]any{"clear_scope": true}, "gofer_session_set_topic", srv.URL)
	if gotBody != `{"scope":[]}` {
		t.Fatalf("clear body = %q", gotBody)
	}

	// Several different candidates: refuse and ask for session_id.
	t.Setenv("CODEX_THREAD_ID", "other")
	gotPath = ""
	isErr, text = resultText(t, map[string]any{"topic": "x"}, "gofer_session_set_topic", srv.URL)
	if !isErr || !strings.Contains(text, "--session") || gotPath != "" {
		t.Fatalf("ambiguous env: isErr=%v %s path=%q", isErr, text, gotPath)
	}

	// An ended session (409) is reported as such.
	t.Setenv("CODEX_THREAD_ID", "")
	status = http.StatusConflict
	isErr, text = resultText(t, map[string]any{"topic": "x"}, "gofer_session_set_topic", srv.URL)
	if !isErr || !strings.Contains(text, "has ended") || !strings.Contains(text, "409") {
		t.Fatalf("ended: isErr=%v %s", isErr, text)
	}
}

func TestSessionFindToolReturnsHits(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"sessions":[{"session_id":"abcdef123456","agent":"claude","state":"offline","topic":"登录","topic_source":"self","maybe_offline":true,"hits":[{"kind":"topic","match":"登录","source":"self"}]}]}`))
	}))
	defer srv.Close()
	isErr, text := resultText(t, map[string]any{"query": "登录", "limit": 5}, "gofer_session_find", srv.URL)
	if isErr || !strings.Contains(text, "maybe_offline") || !strings.Contains(text, "abcdef123456") ||
		!strings.Contains(gotQuery, "q=%E7%99%BB%E5%BD%95") || !strings.Contains(gotQuery, "limit=5") {
		t.Fatalf("isErr=%v %s query=%q", isErr, text, gotQuery)
	}
	if isErr, _ := resultText(t, map[string]any{}, "gofer_session_find", srv.URL); !isErr {
		t.Fatal("empty find must be refused")
	}
}

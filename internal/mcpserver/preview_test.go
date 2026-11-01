package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/i18n"
)

// previewSecret stands for the token inside the bearer URL the server hands out.
const previewSecret = "tok-SECRET-preview"

// previewMock is a stand-in for the HTTP control plane's four preview endpoints. It
// records the bodies it was sent and answers fail (when set) instead of the happy path.
type previewMock struct {
	mu     sync.Mutex
	bodies map[string]map[string]any
	fail   func(r *http.Request) (status int, body any, ok bool)
}

func (m *previewMock) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/jobs/", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		m.mu.Lock()
		if m.bodies == nil {
			m.bodies = map[string]map[string]any{}
		}
		m.bodies[r.Method+" "+r.URL.Path] = body
		m.mu.Unlock()
		reply := func(status int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(v)
		}
		if m.fail != nil {
			if status, v, ok := m.fail(r); ok {
				reply(status, v)
				return
			}
		}
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/previews"):
			reply(http.StatusCreated, map[string]any{"id": "pv1", "url": "http://srv:18100/?token=" + previewSecret, "port": 18100, "target_port": 3000, "expires_at": 1900000000, "worker_id": "local"})
		case r.Method == http.MethodGet:
			reply(http.StatusOK, map[string]any{"previews": []map[string]any{{"id": "pv1", "job_id": "j1", "port": 18100, "state": "active", "created_at": 1800000000, "expires_at": 1900000000}}})
		case r.Method == http.MethodDelete:
			reply(http.StatusOK, map[string]any{"id": "pv1", "state": "closed"})
		case strings.HasSuffix(r.URL.Path, "/renew"):
			reply(http.StatusOK, map[string]any{"id": "pv1", "state": "active", "expires_at": 1900003600, "port": 18100})
		default:
			http.NotFound(w, r)
		}
	})
	return mux
}

// previewSession serves the MCP surface over the mock in lang and returns a session.
func previewSession(t *testing.T, m *previewMock, lang i18n.Lang) *mcp.ClientSession {
	t.Helper()
	return connectTo(t, newServer(langBackend{Backend: previewBackend(t, m), lang: lang}, ""))
}

func previewBackend(t *testing.T, m *previewMock) Backend {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/", m.handler())
	return mockBackend(t, mux)
}

func callPreview(t *testing.T, s *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", tool, err)
	}
	return res
}

func previewResultText(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// TestPreviewToolsReturnStructuredFields: the four tools forward to the HTTP calls with the
// right bodies and answer the snake_case fields of the endpoints.
func TestPreviewToolsReturnStructuredFields(t *testing.T) {
	m := &previewMock{}
	s := previewSession(t, m, i18n.EN)

	var created client.JobPreview
	structured(t, callPreview(t, s, "gofer_job_preview_create", map[string]any{"job_id": "j1", "port": 3000, "ttl_sec": 600}), &created)
	if created.ID != "pv1" || created.JobID != "j1" || created.Port != 18100 || created.TargetPort != 3000 || created.ExpiresAt != 1900000000 || created.WorkerID != "local" || !strings.Contains(created.URL, previewSecret) {
		t.Fatalf("create = %+v", created)
	}
	if got := m.bodies["POST /v1/jobs/j1/previews"]; got["port"] != float64(3000) || got["ttl_sec"] != float64(600) {
		t.Fatalf("create body = %v", got)
	}

	var listed previewsView
	listRes := callPreview(t, s, "gofer_job_preview_list", map[string]any{"job_id": "j1"})
	structured(t, listRes, &listed)
	if len(listed.Previews) != 1 || listed.Previews[0].State != "active" || listed.Previews[0].JobID != "j1" || listed.Previews[0].URL != "" {
		t.Fatalf("list = %+v", listed)
	}
	if strings.Contains(previewResultText(listRes), previewSecret) {
		t.Fatal("list result carries the token")
	}

	var revoked client.JobPreview
	structured(t, callPreview(t, s, "gofer_job_preview_revoke", map[string]any{"job_id": "j1", "preview_id": "pv1"}), &revoked)
	if revoked.ID != "pv1" || revoked.State != "closed" {
		t.Fatalf("revoke = %+v", revoked)
	}

	var renewed client.JobPreview
	structured(t, callPreview(t, s, "gofer_job_preview_renew", map[string]any{"job_id": "j1", "preview_id": "pv1", "ttl_sec": 3600}), &renewed)
	if renewed.ID != "pv1" || renewed.State != "active" || renewed.ExpiresAt != 1900003600 {
		t.Fatalf("renew = %+v", renewed)
	}
	if got := m.bodies["POST /v1/jobs/j1/previews/pv1/renew"]; got["ttl_sec"] != float64(3600) {
		t.Fatalf("renew body = %v", got)
	}
}

// TestPreviewURLStaysOutOfLogs: the bearer URL is in the create result and nowhere else —
// not in slog, not in the standard logger, not in the answer of any other tool.
func TestPreviewURLStaysOutOfLogs(t *testing.T) {
	var logs bytes.Buffer
	prevSlog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	prevOut := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { slog.SetDefault(prevSlog); log.SetOutput(prevOut) })

	m := &previewMock{}
	s := previewSession(t, m, i18n.EN)
	res := callPreview(t, s, "gofer_job_preview_create", map[string]any{"job_id": "j1", "port": 3000})
	if !strings.Contains(previewResultText(res), previewSecret) {
		t.Fatalf("create result lost the URL: %s", previewResultText(res))
	}
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"gofer_job_preview_list", map[string]any{"job_id": "j1"}},
		{"gofer_job_preview_renew", map[string]any{"job_id": "j1", "preview_id": "pv1"}},
		{"gofer_job_preview_revoke", map[string]any{"job_id": "j1", "preview_id": "pv1"}},
	} {
		if r := callPreview(t, s, tc.tool, tc.args); strings.Contains(previewResultText(r), previewSecret) {
			t.Fatalf("%s result carries the token", tc.tool)
		}
	}
	if strings.Contains(logs.String(), previewSecret) {
		t.Fatalf("the preview token reached a log: %s", logs.String())
	}
}

// TestPreviewErrorsFollowLanguage: a refusal with a stable code is worded from the err.*
// catalog in the MCP's language (not the server's), params filled; a code the catalog does
// not know keeps the server message; a permission refusal passes through with its status.
func TestPreviewErrorsFollowLanguage(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     map[string]any
		wantZh   string
		wantEn   string
		wantSame string // when set, both languages show this text instead
	}{
		{"not running", 409, map[string]any{"error": "job is not running", "code": "preview_job_not_running"}, "不在运行", "not running", ""},
		{"remote runner", 409, map[string]any{"error": "preview runner unsupported", "code": "preview_runner_unsupported", "params": map[string]any{"runner": "w-box"}, "detail": "server text"}, "暂不支持 runner w-box", "runner w-box is not supported", ""},
		{"disabled", 404, map[string]any{"error": "preview disabled", "code": "preview_disabled"}, "没有启用", "not enabled", ""},
		{"pool full", 409, map[string]any{"error": "preview unavailable", "code": "preview_pool_exhausted"}, "端口池已满", "port pool is full", ""},
		{"not active", 409, map[string]any{"error": "preview is not active", "code": "preview_not_active"}, "已失效", "no longer active", ""},
		{"permission", 403, map[string]any{"error": "forbidden", "detail": "missing job.read.others"}, "", "", "server 403: forbidden: missing job.read.others"},
		{"unknown code", 409, map[string]any{"error": "x", "code": "no_such_code", "detail": "kept"}, "", "", "server 409: x: kept"},
	}
	for _, lang := range []i18n.Lang{i18n.ZhCN, i18n.EN} {
		for _, tc := range cases {
			t.Run(string(lang)+"/"+tc.name, func(t *testing.T) {
				m := &previewMock{fail: func(*http.Request) (int, any, bool) { return tc.status, tc.body, true }}
				s := previewSession(t, m, lang)
				for _, call := range []struct {
					tool string
					args map[string]any
				}{
					{"gofer_job_preview_create", map[string]any{"job_id": "j1", "port": 3000}},
					{"gofer_job_preview_renew", map[string]any{"job_id": "j1", "preview_id": "pv1"}},
				} {
					res := callPreview(t, s, call.tool, call.args)
					if !res.IsError {
						t.Fatalf("%s: want an error result, got %s", call.tool, previewResultText(res))
					}
					got := previewResultText(res)
					want := tc.wantEn
					if lang == i18n.ZhCN {
						want = tc.wantZh
					}
					if tc.wantSame != "" {
						want = tc.wantSame
					}
					if !strings.Contains(got, want) {
						t.Fatalf("%s error = %q, want it to contain %q", call.tool, got, want)
					}
					if lang == i18n.EN && tc.wantSame == "" && han.MatchString(got) {
						t.Fatalf("English error carries CJK: %q", got)
					}
					if strings.Contains(got, "server text") {
						t.Fatalf("a coded error should be worded from the catalog, got the server text: %q", got)
					}
				}
			})
		}
	}
}

// TestPreviewInputValidationFollowsLanguage: bad arguments are refused before any HTTP call,
// in the MCP's language.
func TestPreviewInputValidationFollowsLanguage(t *testing.T) {
	cases := []struct {
		tool   string
		args   map[string]any
		wantZh string
		wantEn string
	}{
		{"gofer_job_preview_create", map[string]any{"job_id": "", "port": 3000}, "必须提供 job_id", "job_id is required"},
		{"gofer_job_preview_create", map[string]any{"job_id": "j1", "port": 0}, "1 到 65535", "between 1 and 65535"},
		{"gofer_job_preview_create", map[string]any{"job_id": "j1", "port": 70000}, "1 到 65535", "between 1 and 65535"},
		{"gofer_job_preview_create", map[string]any{"job_id": "j1", "port": 3000, "ttl_sec": -1}, "不能为负数", "must not be negative"},
		{"gofer_job_preview_list", map[string]any{"job_id": " "}, "必须提供 job_id", "job_id is required"},
		{"gofer_job_preview_revoke", map[string]any{"job_id": "j1", "preview_id": " "}, "必须提供 job_id 和 preview_id", "job_id and preview_id are required"},
		{"gofer_job_preview_renew", map[string]any{"job_id": "j1", "preview_id": "pv1", "ttl_sec": -5}, "不能为负数", "must not be negative"},
	}
	for _, lang := range []i18n.Lang{i18n.ZhCN, i18n.EN} {
		m := &previewMock{}
		s := previewSession(t, m, lang)
		for _, tc := range cases {
			res := callPreview(t, s, tc.tool, tc.args)
			want := tc.wantEn
			if lang == i18n.ZhCN {
				want = tc.wantZh
			}
			if !res.IsError || !strings.Contains(previewResultText(res), want) {
				t.Fatalf("%s %s %v: result %q (error=%v), want an error containing %q", lang, tc.tool, tc.args, previewResultText(res), res.IsError, want)
			}
		}
		if len(m.bodies) != 0 {
			t.Fatalf("%s: invalid input reached the server: %v", lang, m.bodies)
		}
	}
}

// TestPreviewToolsNeedAServerLocally: the standalone backend has no lease proxy and says so.
func TestPreviewToolsNeedAServerLocally(t *testing.T) {
	jobs, projects, agents := testCore(t)
	s := connectTo(t, newServer(newLocalBackend(jobs, projects, agents), ""))
	res := callPreview(t, s, "gofer_job_preview_create", map[string]any{"job_id": "j1", "port": 3000})
	if !res.IsError || !strings.Contains(previewResultText(res), errNeedsServer.Error()) {
		t.Fatalf("local create = %q (error=%v), want the needs-a-server refusal", previewResultText(res), res.IsError)
	}
}

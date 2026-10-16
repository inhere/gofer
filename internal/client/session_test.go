package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/job"
)

// 会话头的测试（设计 §4「会话 id 的携带」，计划 T2.2）：附带的规则，以及「所有请求都经由同一个
// 注入点」这件事 —— 每一条请求构建路径都要带上会话头，不能有一条路径漏掉。

// TestSessionIDFromEnv: 明确设置优先；否则其余变量去重后恰好一个才用；没有、或两个不同的取值
// 都不附带（附错会让请求拿到别人终端的授权）。
func TestSessionIDFromEnv(t *testing.T) {
	const explicit = "GOFER_SESSION_ID"
	cases := []struct {
		name    string
		env     map[string]string
		wantID  string
		wantSRC string
	}{
		{"no variable", nil, "", ""},
		{"explicit", map[string]string{explicit: "s-exp"}, "s-exp", explicit},
		{"explicit wins over the agent variables", map[string]string{
			explicit: "s-exp", "CLAUDE_CODE_SESSION_ID": "s-cc", "CODEX_THREAD_ID": "s-codex",
		}, "s-exp", explicit},
		{"one agent variable", map[string]string{"CLAUDE_CODE_SESSION_ID": "s-cc"}, "s-cc", "CLAUDE_CODE_SESSION_ID"},
		{"codex thread id", map[string]string{"CODEX_THREAD_ID": "s-codex"}, "s-codex", "CODEX_THREAD_ID"},
		{"the same id twice is one candidate", map[string]string{
			"CODEX_THREAD_ID": "s-same", "CODEX_SESSION_ID": "s-same",
		}, "s-same", "CODEX_THREAD_ID"},
		{"two different ids attach nothing", map[string]string{
			"CLAUDE_CODE_SESSION_ID": "s-cc", "CODEX_THREAD_ID": "s-codex",
		}, "", ""},
		{"blank values count as unset", map[string]string{
			explicit: "   ", "CLAUDE_CODE_SESSION_ID": " ",
		}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(k string) string { return tc.env[k] }
			id, source := SessionIDFromEnv(getenv)
			if id != tc.wantID || source != tc.wantSRC {
				t.Fatalf("SessionIDFromEnv = (%q, %q), want (%q, %q)", id, source, tc.wantID, tc.wantSRC)
			}
		})
	}
	// The environment of the process, read through the same rule.
	t.Setenv(explicit, "s-process")
	c := NewWithTimeout("http://127.0.0.1:1", "tok", 0)
	if id, source := c.SessionID(); id != "s-process" || source != explicit {
		t.Fatalf("the client's session = (%q, %q), want the process environment's", id, source)
	}
}

// TestClientSessionHeaderOnEveryRequestPath: 每一条请求构建路径都经由同一个注入点，所以会话头
// 在每条路径上都出现 —— 普通 JSON 请求、job 提交、SSE 流、文件传输、以及隧道 websocket 握手
// （设计 §4：「所有请求、隧道拨号、SSE / WS 握手都经由它加 X-Gofer-Session-Id」）。
func TestClientSessionHeaderOnEveryRequestPath(t *testing.T) {
	const sid = "sess-every-path"
	t.Setenv(ExplicitSessionEnv, sid)

	var mu sync.Mutex
	seen := map[string]string{} // path -> the session header the server saw
	var wsHeader string
	var wsPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.Header.Get(SessionHeader)
		wsPath = r.URL.Path
		wsHeader = r.Header.Get(SessionHeader)
		mu.Unlock()
		switch {
		case r.Header.Get("Upgrade") == "websocket":
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			conn.Close(websocket.StatusNormalClosure, "")
		case r.URL.Path == "/v1/jobs/x/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "event: end\ndata: {}\n\n")
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "{}")
		}
	}))
	defer ts.Close()

	c := New(ts.URL, "tok")
	if _, err := c.Whoami(); err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if _, err := c.SubmitJob(job.JobRequest{}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	resp, err := c.OpenStream(context.Background(), "x")
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	resp.Body.Close()
	if _, err := c.XferGet(context.Background(), "local", "self", "/tmp/x"); err != nil {
		t.Fatalf("xfer: %v", err)
	}
	if _, _, err := c.StageWorkerUpgrade("w1", writeTempFile(t)); err != nil {
		t.Fatalf("stage worker upgrade: %v", err)
	}
	conn, err := c.DialTunnel(context.Background(), "w1", "127.0.0.1:80")
	if err != nil {
		t.Fatalf("dial tunnel: %v", err)
	}
	conn.Conn.Close(websocket.StatusNormalClosure, "")

	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{"/v1/auth/whoami", "/v1/jobs", "/v1/jobs/x/stream", "/v1/xfer", "/v1/workers/w1/upgrade/file"} {
		if got, ok := seen[path]; !ok {
			t.Errorf("%s was never requested", path)
		} else if got != sid {
			t.Errorf("%s carried %s=%q, want %q", path, SessionHeader, got, sid)
		}
	}
	if wsHeader != sid {
		t.Errorf("the tunnel websocket handshake (%s) carried %s=%q, want %q", wsPath, SessionHeader, wsHeader, sid)
	}
}

// isolateAgentSessionEnv clears the agent-session variables the terminal running the tests may
// export (Claude Code and Codex both do), so a test asserting "no session header" cannot be
// satisfied by the runner's own environment (G053).
func isolateAgentSessionEnv(t *testing.T) {
	t.Helper()
	for _, k := range AgentSessionEnvKeys {
		t.Setenv(k, "")
	}
}

// TestClientWithoutASessionSendsNoHeader: 环境里没有可用的会话 id 时什么都不带 —— 一个普通请求
// 不会凭空声称自己属于某个终端。
func TestClientWithoutASessionSendsNoHeader(t *testing.T) {
	isolateAgentSessionEnv(t)
	var got string
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, gotAuth = r.Header.Get(SessionHeader), r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}")
	}))
	defer ts.Close()

	if _, err := New(ts.URL, "tok").Whoami(); err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if got != "" {
		t.Errorf("%s = %q, want no session header", SessionHeader, got)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want the bearer token", gotAuth)
	}
}

// writeTempFile puts a one-byte file in the test's temp dir for the worker-upgrade upload path.
func writeTempFile(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/upgrade.bin"
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

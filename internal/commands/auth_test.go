package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
)

// `gofer auth` 的命令层测试（设计「接口」一节，计划 T2.1）：服务端返回什么，命令就显示什么，
// 而且 403 提示里那句「`gofer auth whoami` 查看当前权限」真的能看到权限与来源。

// authStub serves the read endpoints of the permission model the `auth` commands use, and records
// the session header the client attached.
type authStub struct {
	whoami            map[string]any
	routes            []map[string]any
	lastSessionHeader string
	lastAuthHeader    string
}

func (a *authStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.lastAuthHeader = r.Header.Get("Authorization")
		a.lastSessionHeader = r.Header.Get(client.SessionHeader)
		switch r.URL.Path {
		case "/v1/auth/whoami":
			_ = json.NewEncoder(w).Encode(a.whoami)
		case "/v1/auth/routes":
			_ = json.NewEncoder(w).Encode(map[string]any{"routes": a.routes})
		default:
			http.NotFound(w, r)
		}
	})
}

// stubAuthServer points the shared connection flags at a stub server for one case (the same
// pattern stubAgentServer uses for the agent commands).
func stubAuthServer(t *testing.T, h http.Handler) {
	t.Helper()
	t.Setenv("GOFER_CONFIG_DIR", t.TempDir())
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	jobConnOpts.server = srv.URL
	jobConnOpts.token = ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
}

// TestAuthWhoamiPrintsPermissionsAndSources: `auth whoami` 显示调用方身份、是否根管理员、每一项权限
// 与它的来源（root / config / db），以及数据库授权是哪条 grant 给的 —— 这就是 403 提示让人去看的
// 东西（「看不到权限从哪来」是个无法自助回答的问题）。
func TestAuthWhoamiPrintsPermissionsAndSources(t *testing.T) {
	// The runner's own terminal may export a session id (Claude Code / Codex do): this case
	// asserts whoami attaches NOTHING, so the environment must not supply a candidate.
	isolateAgentSessionEnv(t)
	stub := &authStub{whoami: map[string]any{
		"caller_id": "alice", "kind": "user", "root": false,
		"grants_version": 7, "roots": []string{"root"},
		"permissions": []map[string]any{
			{"name": "job.submit", "source": "db", "grant_id": "seed:users:job.submit"},
			{"name": "config.write", "source": "config"},
			{"name": "auth.manage", "source": "root"},
		},
	}}
	stubAuthServer(t, stub.handler())

	c := bindCmd(findSub(t, NewAuthCmd(), "whoami"))
	out := captureOutput(t, func() {
		if err := runWhoami(c, mustAuthClient(t)); err != nil {
			t.Fatalf("auth whoami: %v", err)
		}
	})
	for _, want := range []string{
		"caller:      alice", "kind:        user", "root:        no",
		"root admins: root", "grants:      version 7", "permissions (3):",
		"job.submit", "db", "seed:users:job.submit",
		"config.write", "config",
		"auth.manage", "root",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("auth whoami output missing %q:\n%s", want, out)
		}
	}
	if stub.lastSessionHeader != "" {
		t.Fatalf("whoami sent a session header without one being set: %q", stub.lastSessionHeader)
	}
}

// TestAuthWhoamiAttachesTheSessionID: 显式设置了 `GOFER_SESSION_ID` 时，请求带上会话头，输出注明
// 来源；服务端报出的会话 id 与命令自己带的那个一致。
func TestAuthWhoamiAttachesTheSessionID(t *testing.T) {
	// Only the explicit variable may be a candidate (see isolateAgentSessionEnv).
	isolateAgentSessionEnv(t)
	t.Setenv("GOFER_SESSION_ID", "sess-abc")
	stub := &authStub{whoami: map[string]any{
		"caller_id": "alice", "kind": "user", "root": false,
		"session_id": "sess-abc", "grants_version": 1,
		"permissions": []map[string]any{{"name": "tunnel.open", "source": "db"}},
	}}
	stubAuthServer(t, stub.handler())

	c := bindCmd(findSub(t, NewAuthCmd(), "whoami"))
	var out string
	out = captureOutput(t, func() {
		if err := runWhoami(c, mustAuthClient(t)); err != nil {
			t.Fatalf("auth whoami: %v", err)
		}
	})
	if stub.lastSessionHeader != "sess-abc" {
		t.Fatalf("session header = %q, want sess-abc", stub.lastSessionHeader)
	}
	if !strings.Contains(out, "session:     sess-abc (from GOFER_SESSION_ID)") {
		t.Fatalf("auth whoami did not name the session and its source:\n%s", out)
	}
}

// TestAuthWhoamiReportsTheAgentSessionVariable: 没有显式设置 GOFER_SESSION_ID 时，唯一那个 agent
// 会话变量也会被附上，输出注明是哪个变量给的（设计 §4：会话 id 的来源规则）。
func TestAuthWhoamiReportsTheAgentSessionVariable(t *testing.T) {
	// The single agent variable has to BE the only candidate: with the runner's own session id
	// still set beside it, the two would disagree and nothing would be attached.
	isolateAgentSessionEnv(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-cc")
	stub := &authStub{whoami: map[string]any{
		"caller_id": "alice", "kind": "user", "root": false,
		"session_id": "sess-cc", "grants_version": 1,
		"permissions": []map[string]any{{"name": "tunnel.open", "source": "db"}},
	}}
	stubAuthServer(t, stub.handler())

	c := bindCmd(findSub(t, NewAuthCmd(), "whoami"))
	out := captureOutput(t, func() {
		if err := runWhoami(c, mustAuthClient(t)); err != nil {
			t.Fatalf("auth whoami: %v", err)
		}
	})
	if stub.lastSessionHeader != "sess-cc" {
		t.Fatalf("session header = %q, want the single agent session variable", stub.lastSessionHeader)
	}
	if !strings.Contains(out, "session:     sess-cc (from CLAUDE_CODE_SESSION_ID)") {
		t.Fatalf("auth whoami did not name the variable the session came from:\n%s", out)
	}
}

// TestAuthWhoamiAttachesNothingWhenSessionsDisagree: 两个变量给出不同的会话 id 时不附带任何会话头
// （设计 §4：去重后恰好一个才附带）——附错会让请求拿到别人终端的授权，宁可不附。
func TestAuthWhoamiAttachesNothingWhenSessionsDisagree(t *testing.T) {
	isolateAgentSessionEnv(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-cc")
	t.Setenv("CODEX_THREAD_ID", "sess-codex")
	stub := &authStub{whoami: map[string]any{
		"caller_id": "alice", "kind": "user", "root": false,
		"grants_version": 1, "permissions": []map[string]any{},
	}}
	stubAuthServer(t, stub.handler())

	c := bindCmd(findSub(t, NewAuthCmd(), "whoami"))
	var out string
	out = captureOutput(t, func() {
		if err := runWhoami(c, mustAuthClient(t)); err != nil {
			t.Fatalf("auth whoami: %v", err)
		}
	})
	if stub.lastSessionHeader != "" {
		t.Fatalf("session header = %q, want none when the candidates disagree", stub.lastSessionHeader)
	}
	if strings.Contains(out, "session:") {
		t.Fatalf("whoami printed a session it did not attach:\n%s", out)
	}
}

// TestAuthWhoamiReportsADeploymentWithoutRootAdmin: 没有根管理员的部署（allow_empty_token 且没有
// callers）里没人能授权，whoami 必须把服务端的那句话原样显示出来，否则调用方只会一直看到「没有权限」。
func TestAuthWhoamiReportsADeploymentWithoutRootAdmin(t *testing.T) {
	stub := &authStub{whoami: map[string]any{
		"caller_id": "", "kind": "user", "root": false, "grants_version": 0,
		"permissions": []map[string]any{},
		"hint":        "this deployment has no root administrator, so nobody can grant permissions",
	}}
	stubAuthServer(t, stub.handler())

	c := bindCmd(findSub(t, NewAuthCmd(), "whoami"))
	out := captureOutput(t, func() {
		if err := runWhoami(c, mustAuthClient(t)); err != nil {
			t.Fatalf("auth whoami: %v", err)
		}
	})
	for _, want := range []string{"caller:      -", "no permissions", "note: this deployment has no root administrator"} {
		if !strings.Contains(out, want) {
			t.Fatalf("auth whoami output missing %q:\n%s", want, out)
		}
	}
}

// TestAuthRoutesPrintsDeclarations: `auth routes` 列出每条路由的声明、需要的权限与其他凭据 —— 与
// docs/reference/permissions.md 同一份数据，但来自正在运行的那个二进制。
func TestAuthRoutesPrintsDeclarations(t *testing.T) {
	stub := &authStub{routes: []map[string]any{
		{"method": "GET", "path": "/v1/jobs", "user": "in_handler", "mode": "filter", "permissions": []string{"job.read.others"}},
		{"method": "POST", "path": "/v1/jobs/{id}/comments", "user": "in_handler", "mode": "deny",
			"permissions": []string{"job.submit"}, "job_credential": "POST /v1/jobs/*/comments"},
		{"method": "GET", "path": "/v1/auth/whoami", "user": "public"},
		{"method": "POST", "path": "/v1/schedules/{id}/trigger", "user": "no_user_surface", "external": "trigger_token"},
	}}
	stubAuthServer(t, stub.handler())

	c := bindCmd(findSub(t, NewAuthCmd(), "routes"))
	out := captureOutput(t, func() {
		if err := runRoutes(c, mustAuthClient(t)); err != nil {
			t.Fatalf("auth routes: %v", err)
		}
	})
	for _, want := range []string{
		"METHOD", "PATH", "DECLARATION", "PERMISSIONS", "OTHER CREDENTIALS",
		"GET", "/v1/jobs", "in_handler(filter)", "job.read.others",
		"/v1/jobs/{id}/comments", "in_handler(deny)", "job.submit", "job",
		"/v1/auth/whoami", "public", "-",
		"no_user_surface", "external",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("auth routes output missing %q:\n%s", want, out)
		}
	}
}

// mustAuthClient builds the client the commands build, against the stub server of this case.
func mustAuthClient(t *testing.T) *client.Client {
	t.Helper()
	cli, err := newClient("", jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return cli
}

// TestAuthWhoamiPrintsTheCeiling: 受限调用方的 `auth whoami` 列出它的上限，空列表按语义显示
// （项目 / agent 不限显示 any，runners 缺省只允许 worker）。
func TestAuthWhoamiPrintsTheCeiling(t *testing.T) {
	isolateAgentSessionEnv(t)
	stub := &authStub{whoami: map[string]any{
		"caller_id": "ci-bot", "kind": "user", "root": false, "grants_version": 1,
		"permissions": []map[string]any{{"name": "job.submit", "source": "config"}},
		"ceiling": map[string]any{"projects": []string{"app-*"}, "agents": []string{"codex"},
			"allow_exec": false, "global_memory": false, "read_only": true},
	}}
	stubAuthServer(t, stub.handler())

	c := bindCmd(findSub(t, NewAuthCmd(), "whoami"))
	out := captureOutput(t, func() {
		if err := runWhoami(c, mustAuthClient(t)); err != nil {
			t.Fatalf("auth whoami: %v", err)
		}
	})
	for _, want := range []string{
		"ceiling:", "projects:      app-*", "runners:       workers only", "workers:       any",
		"agents:        codex", "exec:          no", "read only:     yes",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("auth whoami output missing %q:\n%s", want, out)
		}
	}
}

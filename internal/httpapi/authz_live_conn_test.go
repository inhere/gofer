package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/tunnel"
)

// 长连接按依赖撤销的测试（设计 docs/design/2026-10-14-db-capabilities-and-session-grants-design.md
// §6「长连接的撤销」，计划 T2.3）：
//
//   - 授权被撤销 → 已经建立的隧道断开；
//   - 配置重载去掉能力位 → 断开；
//   - 仍然有权限的连接保留（同一个权限还有别的来源）；
//   - 会话授权到期（没有人通知的那类触发）→ 周期重新判定时断开；
//   - 托管转发失去那对权限 → 监听被停掉；
//   - ws 推送按 job.read.others / session.read.others 过滤 topic。

// gatesOpenServer is the fixture these cases share: every governance gate OPEN and only the
// `can_admin` root holding anything, so a caller holds EXACTLY what the database grants say and
// nothing is derived from the configuration — the shape that makes a revocation observable.
func gatesOpenServer(t *testing.T) *Server {
	t.Helper()
	return newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "root", Token: "tok-root", CanAdmin: true},
			{ID: "alice", Token: "tok-alice"},
		},
		Governance: config.GovernanceConfig{
			RequireAnswerCapability: true, RequireAdminCapability: true,
			RequireAttachCapability: true, RequireTunnelCapability: true,
		},
	})
}

// grantCaller creates a `caller:<id>` grant of pattern as the root administrator. expiresSec 0 means
// no expiry.
func grantCaller(t *testing.T, s *Server, caller, pattern string, expiresSec int64) authGrantView {
	t.Helper()
	body := map[string]any{"subject": "caller:" + caller, "pattern": pattern}
	if expiresSec > 0 {
		body["expires_sec"] = expiresSec
	}
	resp := do(t, s, http.MethodPost, "/v1/auth/grants", "tok-root", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("grant %s to %s: status=%d, body %s", pattern, caller, resp.StatusCode, readBody(t, resp))
	}
	var created authGrantView
	decode(t, resp, &created)
	return created
}

// revokeGrant revokes one grant as the root administrator and asserts the answer.
func revokeGrant(t *testing.T, s *Server, id string) {
	t.Helper()
	resp := do(t, s, http.MethodDelete, "/v1/auth/grants/"+id, "tok-root", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke %s: status=%d, body %s", id, resp.StatusCode, readBody(t, resp))
	}
	_ = resp.Body.Close()
}

// waitLiveConn waits until the long-connection table holds want entries. A count that never arrives
// — or one that stays too high — fails, so both "it was dropped" and "it was kept" are asserted on
// the real table.
func waitLiveConn(t *testing.T, s *Server, want int, what string) {
	t.Helper()
	wait.For(t, 5*time.Second, what, func() (bool, any) {
		got := len(s.liveConns.snapshot())
		return got == want, got
	})
}

// waitLiveConnClosed waits until the server has dropped the connection AND the client end really
// sees it end: the first half is the registry re-judging it away, the second is a read that stops
// working. The teardown arrives either as a close frame or as a bare EOF — Splice cancels the
// splice context before running the close handshake (splice.go's closeBoth), so both are the
// server closing it — while a read that stays alive until the deadline means it was NOT closed.
func waitLiveConnClosed(t *testing.T, s *Server, conn *websocket.Conn, what string) {
	t.Helper()
	waitLiveConn(t, s, 0, what+" (server side)")
	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 5*time.Second))
	defer cancel()
	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatalf("%s: the connection is still readable", what)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		t.Fatalf("%s: the connection was not closed by the server (err=%v)", what, err)
	}
}

// startTunnel opens a tunnel as token (with an optional session header) against a server whose hub
// delivers a worker data connection, waits until the splice is registered as a long connection
// (design §6) and proves it carries traffic with one echo round trip.
func startTunnel(t *testing.T, s *Server, token, session string) *websocket.Conn {
	t.Helper()
	echo := wsEcho(t)
	t.Cleanup(echo.Close)
	s.hub = &deliveringHub{s: s, echo: echo, opened: make(chan string, 1)}
	s.router = s.buildRouter()
	api := httptest.NewServer(s.Handler())
	t.Cleanup(api.Close)

	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 5*time.Second))
	defer cancel()
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	if session != "" {
		h.Set(sessionHeader, session)
	}
	conn, _, err := websocket.Dial(ctx,
		"ws"+strings.TrimPrefix(api.URL, "http")+"/v1/tunnels/connect?worker=w1&target=127.0.0.1:80",
		&websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatalf("dial the tunnel as %s (session %q): %v", token, session, err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	waitLiveConn(t, s, 1, "the tunnel is registered as a long connection")
	echoTunnelOnce(t, conn)
	return conn
}

// echoTunnelOnce proves a tunnel still carries traffic (the caller is still allowed on it).
func echoTunnelOnce(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 5*time.Second))
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("ping")); err != nil {
		t.Fatalf("tunnel write: %v", err)
	}
	_, b, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("tunnel read: %v", err)
	}
	if string(b) != "ping" {
		t.Fatalf("tunnel echoed %q, want ping", b)
	}
}

// TestTunnelClosesWhenItsGrantIsRevoked is the acceptance case of plan T2.3: alice's tunnel.open
// comes from ONE database grant, and revoking it ends the tunnel that grant was keeping open.
func TestTunnelClosesWhenItsGrantIsRevoked(t *testing.T) {
	t.Parallel()
	s := gatesOpenServer(t)
	grant := grantCaller(t, s, "alice", authz.PermTunnelOpen, 0)
	conn := startTunnel(t, s, "tok-alice", "")

	revokeGrant(t, s, grant.ID)
	waitLiveConnClosed(t, s, conn, "revoking the grant must close the tunnel it kept open")
}

// TestTunnelStaysWhenAnotherSourceStillGrantsIt: 断开时重新判定一次（设计 §6「仍然有权限的连接保留」）
// — alice holds tunnel.open from the configuration AND from a grant, so revoking the grant changes
// nothing for the tunnel that is already open.
func TestTunnelStaysWhenAnotherSourceStillGrantsIt(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "root", Token: "tok-root", CanAdmin: true},
			{ID: "alice", Token: "tok-alice", CanTunnel: true},
		},
		Governance: config.GovernanceConfig{RequireTunnelCapability: true},
	})
	grant := grantCaller(t, s, "alice", authz.PermTunnelOpen, 0)
	conn := startTunnel(t, s, "tok-alice", "")
	key := s.liveConns.snapshot()[0].key

	revokeGrant(t, s, grant.ID)
	// The config-derived floor still holds tunnel.open, so the tunnel must still be there and must
	// still carry traffic — the SAME registration, not one that came back.
	echoTunnelOnce(t, conn)
	waitLiveConn(t, s, 1, "the tunnel that still holds tunnel.open stays registered")
	if got := s.liveConns.snapshot()[0].key; got != key {
		t.Fatalf("the registration changed: key=%q, want %q", got, key)
	}
}

// TestTunnelClosesWhenAConfigReloadDropsTheCapability: 配置重载后「配置推出的授权变了」也是断开条件
// （设计 §6）— the reload takes can_tunnel away, so the derived tunnel.open is gone and the open
// tunnel goes with it.
func TestTunnelClosesWhenAConfigReloadDropsTheCapability(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "root", Token: "tok-root", CanAdmin: true},
			{ID: "alice", Token: "tok-alice", CanTunnel: true},
		},
		Governance: config.GovernanceConfig{RequireTunnelCapability: true},
	})
	conn := startTunnel(t, s, "tok-alice", "")

	reloaded := config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "root", Token: "tok-root", CanAdmin: true},
			{ID: "alice", Token: "tok-alice"}, // no can_tunnel any more
		},
		Governance: config.GovernanceConfig{RequireTunnelCapability: true},
	}
	s.SetServerConfig(&reloaded)
	waitLiveConnClosed(t, s, conn, "a reload that drops can_tunnel must close the tunnel it derived")
}

// TestTunnelClosesWhenItsSessionGrantExpires: 会话授权到期是没有人通知的那类触发，由周期重新判定接住
// （设计 §6「授权被撤销、到期」/§4「会话授权」）—— the tunnel is opened with the session header of the
// session the grant belongs to, and the grant's one second runs out while it is open.
func TestTunnelClosesWhenItsSessionGrantExpires(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "root", Token: "tok-root", CanAdmin: true},
			{ID: "alice", Token: "tok-alice"},
		},
		Governance: config.GovernanceConfig{RequireTunnelCapability: true},
	})
	registerSessionAs(t, s, "tok-alice", "sess-tun")
	// Nobody announces an expiry, so the PERIODIC re-judgment is what catches it (the sweeper reads
	// its interval when it starts, at the first registration — hence before the tunnel is opened).
	// Two seconds of validity: enough for the setup on a loaded machine, short enough to wait for.
	s.connSweepEvery = 50 * time.Millisecond
	// The grant is the ONLY source of alice's tunnel.open here (the tunnel gate is open and she has
	// no can_tunnel), so the tunnel can only exist while it is in force.
	createSessionGrant(t, s, "sess-tun", authz.PermTunnelOpen, 2)
	conn := startTunnel(t, s, "tok-alice", "sess-tun")

	waitLiveConnClosed(t, s, conn, "the tunnel must be closed once its session grant expires")
}

// TestHostedForwarderStopsWhenItsGrantIsRevoked: 托管转发按启动者登记（设计 §6「判定与登记都按启动
// 者」），失去 tunnel.hosted / tunnel.open 时监听被停掉，而不是只拒绝下一次请求。
func TestHostedForwarderStopsWhenItsGrantIsRevoked(t *testing.T) {
	t.Parallel()
	s := gatesOpenServer(t)
	st := openTestStore(t, t.TempDir())
	s.SetTunnelPresets(st)
	if err := st.UpsertTunnelPreset(jobstore.TunnelPresetRecord{
		Name: "demo", Worker: "w-case", SpecsJSON: `["0:127.0.0.1:1"]`, UpdatedAt: time.Now().Unix(), UpdatedBy: "alice",
	}); err != nil {
		t.Fatalf("seed preset: %v", err)
	}
	t.Cleanup(s.StopHostedForwarders)
	// The dial is never reached: a forwarder only dials when a local connection arrives.
	s.SetHostedForwarderDial(func(context.Context, string, string, string, string) (tunnel.DialResult, error) {
		return tunnel.DialResult{}, nil
	})

	grant := grantCaller(t, s, "alice", "tunnel.*", 0)
	resp := do(t, s, http.MethodPost, "/v1/tunnels/hosted/demo", "tok-alice", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("hosted start: status=%d, body %s", resp.StatusCode, readBody(t, resp))
	}
	_ = resp.Body.Close()
	if got := listForwarders(t, s, "tok-alice"); len(got) != 1 {
		t.Fatalf("hosted registrations = %+v, want one", got)
	}
	waitLiveConn(t, s, 1, "the hosted forwarder is registered as a long connection")

	revokeGrant(t, s, grant.ID)
	waitLiveConn(t, s, 0, "the hosted forwarder leaves the table once its grant is revoked")
	wait.Until(t, 5*time.Second, "the hosted forwarder's listener is stopped", func() bool {
		return len(listForwarders(t, s, "tok-alice")) == 0
	})
}

// TestWSTopicsAreFilteredByReadPermission: /v1/ws 的推送按 job.read.others / session.read.others
// 过滤 topic（设计 §6）—— 自己的 job 不需要权限，别人的 job 与两个列表主题需要；被拒的 topic 在
// 错误帧里报出来，允许的照常订阅。
func TestWSTopicsAreFilteredByReadPermission(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "bob", Token: "tok-bob"},
		},
		// Gates open: nothing is derived for alice, and the database picture is replaced below.
		Governance: config.GovernanceConfig{
			RequireAnswerCapability: true, RequireAdminCapability: true,
			RequireAttachCapability: true, RequireTunnelCapability: true,
		},
	})
	own, status := createJob(t, s, "tok-alice")
	if status != http.StatusOK {
		t.Fatalf("create alice's job = %d", status)
	}
	other, status := createJob(t, s, "tok-bob")
	if status != http.StatusOK {
		t.Fatalf("create bob's job = %d", status)
	}
	// alice holds the database half of nothing: job.read.others and session.read.others are the
	// question here, and the seeded `users` grants would answer it for her.
	s.setDBGrants(nil)

	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	_, ticket := postWSTicket(t, s, "tok-alice")
	conn, _, err := dialWS(t, srv, ticket, "")
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer conn.CloseNow()
	if hello := readWSFrame(t, conn); hello["t"] != "hello" {
		t.Fatalf("want hello, got %v", hello)
	}

	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 5*time.Second))
	defer cancel()
	sub := `{"t":"sub","topics":["stats","job:` + own.ID + `","job:` + other.ID + `","jobs","sessions"]}`
	if err := conn.Write(ctx, websocket.MessageText, []byte(sub)); err != nil {
		t.Fatal(err)
	}
	rejected := map[string]bool{}
	frames := 0
	wait.Until(t, 5*time.Second, "the refused topics are reported", func() bool {
		m := readWSFrame(t, conn)
		frames++
		if m["t"] != "error" {
			return false // the stats snapshot
		}
		for _, raw := range m["topics"].([]any) {
			rejected[raw.(string)] = true
		}
		return true
	})
	for _, want := range []string{"job:" + other.ID, "jobs", "sessions"} {
		if !rejected[want] {
			t.Errorf("%s must be refused without its read permission; rejected=%v", want, rejected)
		}
	}
	if rejected["job:"+own.ID] || rejected["stats"] {
		t.Errorf("the caller's own job and stats must be allowed; rejected=%v", rejected)
	}
	if frames == 0 {
		t.Fatal("no frame arrived")
	}
}

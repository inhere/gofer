// tunnel_e2e_test.go is the TUN-01 end-to-end matrix: a real httptest serve
// (hub + httpapi tunnel endpoints) + a real worker.Client over the hub websocket +
// an in-process echo server, all on loopback. It sinks the scenarios of the manual
// shell smoke (scripts/smoke/tunnel/run-smoke.sh) into `go test`, so no external
// process is needed.
package worker_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tunnel"
	"github.com/inhere/gofer/internal/worker"
	"github.com/inhere/gofer/internal/wsproto"
)

const tunServerToken = "server-default-token"

// tunEnv is one running serve + worker pair plus a user-token client.
type tunEnv struct {
	hub *hubSide
	cli *client.Client
}

// startTunnelWorker builds the hub, starts a worker with the given tunnel policy (and
// optional reload seam), and waits until it is online.
func startTunnelWorker(t *testing.T, tun config.WorkerTunnelConfig, reload worker.ReloadFunc) *tunEnv {
	t.Helper()
	t.Setenv("GOFER_CONFIG_DIR", t.TempDir())
	hub := buildHubSide(t)
	wsURL := "ws" + strings.TrimPrefix(hub.ts.URL, "http") + "/v1/workers/connect"
	cl := worker.New(worker.Config{
		WorkerID: e2eWorkerID,
		URLs:     []string{wsURL},
		Token:    e2eToken,
		Tunnel:   tun,
		Reload:   reload,
	}, nil)
	worker.StartClient(t, context.Background(), cl)
	waitWorkerOnline(t, hub.hub)
	return &tunEnv{hub: hub, cli: client.New(hub.ts.URL, tunServerToken)}
}

// startTCPEcho serves a byte echo; each accepted conn is reported on conns so a test
// can close it from the "device" side.
func startTCPEcho(t *testing.T) (addr string, conns <-chan net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan net.Conn, 16)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			ch <- c
			go func() { _, _ = io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String(), ch
}

// closedTCPAddr returns a loopback address nobody listens on.
func closedTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := ln.Addr().String()
	_ = ln.Close()
	return a
}

func dialTun(t *testing.T, env *tunEnv, target string, network ...string) (client.TunnelConn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return env.cli.DialTunnel(ctx, e2eWorkerID, target, network...)
}

func mustDialTun(t *testing.T, env *tunEnv, target string, network ...string) client.TunnelConn {
	t.Helper()
	tc, err := dialTun(t, env, target, network...)
	if err != nil {
		t.Fatalf("DialTunnel(%s): %v", target, err)
	}
	t.Cleanup(func() { _ = tc.Conn.CloseNow() })
	return tc
}

func wantTunnelStatus(t *testing.T, err error, status int) {
	t.Helper()
	var te *client.TunnelError
	if !errors.As(err, &te) {
		t.Fatalf("err=%v, want *client.TunnelError with status %d", err, status)
	}
	if te.Status != status {
		t.Fatalf("status=%d (%s), want %d", te.Status, te.Msg, status)
	}
}

// echoOnce writes msg through the tunnel and reads the echo back.
func echoOnce(t *testing.T, tc client.TunnelConn, msg string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := tc.Conn.Write(ctx, websocket.MessageBinary, []byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
	var got []byte
	for len(got) < len(msg) {
		_, b, err := tc.Conn.Read(ctx)
		if err != nil {
			t.Fatalf("read after %q: %v", got, err)
		}
		got = append(got, b...)
	}
	if string(got) != msg {
		t.Fatalf("echo=%q want %q", got, msg)
	}
}

func waitTunnelCount(t *testing.T, env *tunEnv, n int) []client.TunnelInfo {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		list, err := env.cli.ListTunnels()
		if err != nil {
			t.Fatalf("ListTunnels: %v", err)
		}
		if len(list) == n {
			return list
		}
		if time.Now().After(deadline) {
			t.Fatalf("tunnel count=%d want %d: %+v", len(list), n, list)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 1. Round trip + list lifecycle.
func TestTunnelE2ERoundTrip(t *testing.T) {
	echo, _ := startTCPEcho(t)
	env := startTunnelWorker(t, config.WorkerTunnelConfig{Allow: []string{echo}}, nil)

	tc := mustDialTun(t, env, echo)
	if tc.TunnelID == "" {
		t.Error("tunnel id header missing")
	}
	echoOnce(t, tc, "hello tunnel")
	echoOnce(t, tc, strings.Repeat("x", 64*1024))

	list := waitTunnelCount(t, env, 1)
	if list[0].Target != echo || list[0].WorkerID != e2eWorkerID || list[0].ID != tc.TunnelID {
		t.Fatalf("list entry=%+v want target=%s worker=%s id=%s", list[0], echo, e2eWorkerID, tc.TunnelID)
	}

	_ = tc.Conn.Close(websocket.StatusNormalClosure, "bye")
	waitTunnelCount(t, env, 0)
}

// 2. Target outside the worker allowlist -> 403.
func TestTunnelE2ENotAllowed403(t *testing.T) {
	allowed, _ := startTCPEcho(t)
	other, _ := startTCPEcho(t)
	env := startTunnelWorker(t, config.WorkerTunnelConfig{Allow: []string{allowed}}, nil)

	_, err := dialTun(t, env, other)
	wantTunnelStatus(t, err, http.StatusForbidden)
	waitTunnelCount(t, env, 0)
}

// 3. Allowed but nothing listening -> 502 dial failure.
func TestTunnelE2EDialFailed502(t *testing.T) {
	closed := closedTCPAddr(t)
	env := startTunnelWorker(t, config.WorkerTunnelConfig{Allow: []string{closed}}, nil)

	_, err := dialTun(t, env, closed)
	wantTunnelStatus(t, err, http.StatusBadGateway)
	waitTunnelCount(t, env, 0)
}

// 4. max_conns exceeded -> 429; closing one frees a slot.
func TestTunnelE2ELimit429SlotRelease(t *testing.T) {
	echo, _ := startTCPEcho(t)
	env := startTunnelWorker(t, config.WorkerTunnelConfig{Allow: []string{echo}, MaxConns: 2}, nil)

	a := mustDialTun(t, env, echo)
	b := mustDialTun(t, env, echo)
	echoOnce(t, a, "a")
	echoOnce(t, b, "b")

	_, err := dialTun(t, env, echo)
	wantTunnelStatus(t, err, http.StatusTooManyRequests)

	_ = a.Conn.Close(websocket.StatusNormalClosure, "")
	// The worker releases the slot asynchronously once its bridge unwinds.
	deadline := time.Now().Add(5 * time.Second)
	var c client.TunnelConn
	for {
		var derr error
		c, derr = dialTun(t, env, echo)
		if derr == nil {
			break
		}
		wantTunnelStatus(t, derr, http.StatusTooManyRequests)
		if time.Now().After(deadline) {
			t.Fatal("slot was never released after closing a tunnel")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { _ = c.Conn.CloseNow() })
	echoOnce(t, c, "c")
	echoOnce(t, b, "b2") // the surviving tunnel is untouched
}

// 5. Reload that drops the target from the allowlist: new tunnels get 403 (disabled
// when the allowlist becomes empty); an already-open tunnel keeps working because
// the policy is only consulted at open time.
func TestTunnelE2EReloadRemovesTarget(t *testing.T) {
	echo, _ := startTCPEcho(t)
	other, _ := startTCPEcho(t)
	cur := config.WorkerTunnelConfig{Allow: []string{echo}}
	reload := func(*wsproto.Policy) (worker.ReloadOutcome, error) {
		c := cur
		return worker.ReloadOutcome{Tunnel: &c}, nil
	}
	env := startTunnelWorker(t, cur, reload)

	open := mustDialTun(t, env, echo)
	echoOnce(t, open, "before")

	doReload := func() {
		t.Helper()
		// Same RPC POST /v1/workers/{id}/reload drives (hub -> worker reload frame).
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := env.hub.hub.ReloadWorker(ctx, e2eWorkerID, "test"); err != nil {
			t.Fatalf("hub.ReloadWorker: %v", err)
		}
	}

	// Swap the allowlist to a different target: echo is no longer allowed.
	cur = config.WorkerTunnelConfig{Allow: []string{other}}
	doReload()
	_, err := dialTun(t, env, echo)
	wantTunnelStatus(t, err, http.StatusForbidden)
	nt := mustDialTun(t, env, other) // the new allowlist takes effect
	echoOnce(t, nt, "new")
	echoOnce(t, open, "still open") // existing tunnel unaffected

	// Empty allowlist -> tunnels disabled -> 403 too.
	cur = config.WorkerTunnelConfig{}
	doReload()
	_, err = dialTun(t, env, other)
	wantTunnelStatus(t, err, http.StatusForbidden)
}

// 6. The device closes its end: the client sees the close and the tunnel is cleaned up.
func TestTunnelE2EDeviceSideClose(t *testing.T) {
	echo, conns := startTCPEcho(t)
	env := startTunnelWorker(t, config.WorkerTunnelConfig{Allow: []string{echo}}, nil)

	tc := mustDialTun(t, env, echo)
	echoOnce(t, tc, "ping")
	waitTunnelCount(t, env, 1)

	var devConn net.Conn
	select {
	case devConn = <-conns:
	case <-time.After(5 * time.Second):
		t.Fatal("echo server never accepted the worker's connection")
	}
	_ = devConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, err := tc.Conn.Read(ctx)
	if err == nil {
		t.Fatal("read succeeded after device closed; want the tunnel to end")
	}
	if ctx.Err() != nil {
		t.Fatalf("client never saw the close (deadline hit): %v", err)
	}
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.StatusNormalClosure {
		t.Fatalf("read err=%v, want a normal websocket close (EOF)", err)
	}
	waitTunnelCount(t, env, 0)
}

// 7. Worker token / unknown worker token cannot serve or open tunnels.
func TestTunnelE2EWorkerToken403(t *testing.T) {
	echo, _ := startTCPEcho(t)
	env := startTunnelWorker(t, config.WorkerTunnelConfig{Allow: []string{echo}}, nil)

	// (a) the worker's own token cannot open a tunnel as a client -> 403.
	wcli := client.New(env.hub.ts.URL, e2eToken)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := wcli.DialTunnel(ctx, e2eWorkerID, echo)
	wantTunnelStatus(t, err, http.StatusForbidden)

	// (b) an unknown token is rejected at the tunnel endpoint (401) ...
	bad := client.New(env.hub.ts.URL, "no-such-token")
	_, err = bad.DialTunnel(ctx, e2eWorkerID, echo)
	wantTunnelStatus(t, err, http.StatusUnauthorized)

	// (c) ... cannot register as a worker (hub connect refuses the upgrade) ...
	wsBase := "ws" + strings.TrimPrefix(env.hub.ts.URL, "http")
	dial := func(path, token string) int {
		h := http.Header{}
		h.Set("Authorization", "Bearer "+token)
		c, resp, derr := websocket.Dial(ctx, wsBase+path, &websocket.DialOptions{HTTPHeader: h})
		if derr == nil {
			_ = c.CloseNow()
			return 0
		}
		if resp == nil {
			t.Fatalf("dial %s: %v", path, derr)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if got := dial("/v1/workers/connect", "no-such-token"); got != http.StatusUnauthorized && got != http.StatusForbidden {
		t.Fatalf("worker connect with bad token: status=%d want 401/403", got)
	}
	// ... and cannot deliver a tunnel data connection (worker callback endpoint).
	if got := dial(tunnel.WorkerConnectPath, "no-such-token"); got != http.StatusUnauthorized {
		t.Fatalf("worker tunnel-connect with bad token: status=%d want 401", got)
	}
	// A user (non-worker) token is not a worker either.
	if got := dial(tunnel.WorkerConnectPath, tunServerToken); got != http.StatusUnauthorized {
		t.Fatalf("worker tunnel-connect with user token: status=%d want 401", got)
	}

	// (d) a real worker.Client with a wrong token never comes online.
	hub2 := buildHubSide(t)
	cl := worker.New(worker.Config{
		WorkerID: e2eWorkerID,
		URLs:     []string{"ws" + strings.TrimPrefix(hub2.ts.URL, "http") + "/v1/workers/connect"},
		Token:    "wrong-worker-token",
		Tunnel:   config.WorkerTunnelConfig{Allow: []string{echo}},
	}, nil)
	rctx, rcancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = cl.Run(rctx); close(done) }()
	defer func() { rcancel(); <-done }()
	time.Sleep(300 * time.Millisecond)
	if hub2.hub.IsOnline(e2eWorkerID) {
		t.Fatal("worker with a wrong token registered")
	}
	ucli := client.New(hub2.ts.URL, tunServerToken)
	_, err = ucli.DialTunnel(ctx, e2eWorkerID, echo)
	wantTunnelStatus(t, err, http.StatusNotFound) // worker offline
}

// 8. One UDP datagram round trip through a udp/ allowlist entry.
func TestTunnelE2EUDPRoundTrip(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(append([]byte("echo:"), buf[:n]...), from)
		}
	}()
	target := pc.LocalAddr().String()
	env := startTunnelWorker(t, config.WorkerTunnelConfig{Allow: []string{"udp/" + target}}, nil)

	tc := mustDialTun(t, env, target, "udp")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := tc.Conn.Write(ctx, websocket.MessageBinary, []byte("dgram")); err != nil {
		t.Fatal(err)
	}
	_, got, err := tc.Conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "echo:dgram" {
		t.Fatalf("datagram reply=%q want %q", got, "echo:dgram")
	}
	waitTunnelCount(t, env, 1)

	// A tcp-only allowlist must not authorize the same host:port as udp.
	env2 := startTunnelWorker(t, config.WorkerTunnelConfig{Allow: []string{target}}, nil)
	_, err = dialTun(t, env2, target, "udp")
	wantTunnelStatus(t, err, http.StatusForbidden)
}

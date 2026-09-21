package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tunnel"
	"github.com/inhere/gofer/internal/wsproto"
)

// lockedBuffer makes a bytes.Buffer safe for the handler goroutines that log
// concurrently while a test drives the client side.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// captureTunnelEvents runs fn with the default logger swapped for a JSON
// handler and returns every record that carries an event attribute, in order.
func captureTunnelEvents(t *testing.T, fn func()) []map[string]any {
	t.Helper()
	buf := &lockedBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	defer slog.SetDefault(old)
	fn()
	buf.mu.Lock()
	defer buf.mu.Unlock()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(buf.b.Bytes()), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatalf("log line is not JSON: %s", line)
		}
		if _, ok := row["event"]; ok {
			out = append(out, row)
		}
	}
	return out
}

// sensitiveKey matches attr names that must never appear in tunnel logs.
var sensitiveKey = regexp.MustCompile(`(?i)token|authorization|nonce|payload`)

func assertNoSensitiveKeys(t *testing.T, events []map[string]any) {
	t.Helper()
	for _, e := range events {
		for k := range e {
			if sensitiveKey.MatchString(k) {
				t.Fatalf("event %v leaks key %q: %v", e["event"], k, e)
			}
		}
	}
}

// eventNames extracts the event attr of records whose event has the prefix.
func eventNames(events []map[string]any, prefix string) []string {
	var out []string
	for _, e := range events {
		if n, _ := e["event"].(string); strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	return out
}

func findEvent(events []map[string]any, name string) map[string]any {
	for _, e := range events {
		if e["event"] == name {
			return e
		}
	}
	return nil
}

// wsEcho is a websocket server that echoes binary frames, standing in for the
// worker's data websocket in these tests.
func wsEcho(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		for {
			typ, rd, e := c.Reader(r.Context())
			if e != nil {
				return
			}
			b, _ := io.ReadAll(rd)
			if c.Write(r.Context(), typ, b) != nil {
				return
			}
		}
	}))
}

// newTunnelEventServer builds a server whose hub is the given stand-in; the
// returned *Server is exposed so a deliveringHub can reach the registry.
func newTunnelEventServer(t *testing.T, hub workerHub) (*Server, *httptest.Server) {
	t.Helper()
	s := newTestServerCfg(t, config.ServerConfig{Token: "api-token", Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "worker-token"}}})
	s.hub = hub
	s.router = s.buildRouter()
	api := httptest.NewServer(s.Handler())
	t.Cleanup(api.Close)
	return s, api
}

func dialConnect(ctx context.Context, api *httptest.Server) (*websocket.Conn, *http.Response, error) {
	h := http.Header{}
	h.Set("Authorization", "Bearer api-token")
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(api.URL, "http")+"/v1/tunnels/connect?worker=w1&target=127.0.0.1:80", &websocket.DialOptions{HTTPHeader: h})
}

// TestTunnelServerEventSequence drives one tunnel through a full rendezvous,
// one round trip and a client close, and checks the server's event sequence
// and the fields the runbook promises for correlating with the other two ends.
func TestTunnelServerEventSequence(t *testing.T) {
	echo := wsEcho(t)
	defer echo.Close()
	hub := &deliveringHub{echo: echo, opened: make(chan string, 1)}
	srv, api := newTunnelEventServer(t, hub)
	hub.s = srv

	var tunnelID string
	events := captureTunnelEvents(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, resp, err := dialConnect(ctx, api)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		tunnelID = resp.Header.Get(tunnel.HeaderTunnelID)
		if err := c.Write(ctx, websocket.MessageBinary, []byte("ping")); err != nil {
			t.Fatal(err)
		}
		if _, b, err := c.Read(ctx); err != nil || string(b) != "ping" {
			t.Fatalf("echo: %q %v", b, err)
		}
		c.Close(websocket.StatusNormalClosure, "")
		// tunnel.closed is logged after the splice unwinds and the tunnel leaves
		// the active list; wait for that rather than a fixed sleep.
		deadline := time.Now().Add(5 * time.Second)
		for len(srv.tunnels.List()) > 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond) // the closed record is written right after removal
	})
	assertNoSensitiveKeys(t, events)

	want := []string{"tunnel.requested", "tunnel.rendezvous_started", "tunnel.worker_connected", "tunnel.opened", "tunnel.first_up", "tunnel.first_down", "tunnel.closed"}
	got := eventNames(events, "tunnel.")
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("server tunnel events:\n got %v\nwant %v", got, want)
	}
	for _, name := range want {
		e := findEvent(events, name)
		if e["tunnel_id"] != tunnelID || e["component"] != "server" {
			t.Fatalf("%s: tunnel_id=%v component=%v want %s/server", name, e["tunnel_id"], e["component"], tunnelID)
		}
	}
	if e := findEvent(events, "tunnel.requested"); e["network"] != "tcp" || e["target"] != "127.0.0.1:80" || e["worker_id"] != "w1" {
		t.Fatalf("tunnel.requested fields: %v", e)
	}
	if e := findEvent(events, "tunnel.worker_connected"); e["rendezvous_ms"] == nil {
		t.Fatalf("worker_connected lacks rendezvous_ms: %v", e)
	}
	for _, name := range []string{"tunnel.first_up", "tunnel.first_down"} {
		if e := findEvent(events, name); e["first_byte_ms"] == nil {
			t.Fatalf("%s lacks first_byte_ms: %v", name, e)
		}
	}
	closed := findEvent(events, "tunnel.closed")
	if closed["bytes_up"] != float64(4) || closed["bytes_down"] != float64(4) {
		t.Fatalf("tunnel.closed bytes: %v", closed)
	}
	if closed["close_reason"] != tunnel.ReasonClientClosed || closed["duration_ms"] == nil {
		t.Fatalf("tunnel.closed reason/duration: %v", closed)
	}
}

// TestTunnelServerRejectEvents covers the two ways a rendezvous fails after the
// worker answered: the worker refuses the target (its error code must surface
// as tunnel.rejected + the mapped HTTP status) and the worker presents a wrong
// relay nonce (the arrival is dropped, the client times out). Neither leaves a
// tunnel.opened behind.
func TestTunnelServerRejectEvents(t *testing.T) {
	old := tunnelRendezvousTimeout
	tunnelRendezvousTimeout = 200 * time.Millisecond
	defer func() { tunnelRendezvousTimeout = old }()

	t.Run("worker refuses", func(t *testing.T) {
		echo := wsEcho(t)
		defer echo.Close()
		hub := &deliveringHub{echo: echo, opened: make(chan string, 1), reject: "target_not_allowed"}
		srv, api := newTunnelEventServer(t, hub)
		hub.s = srv
		var status int
		events := captureTunnelEvents(t, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, resp, err := dialConnect(ctx, api)
			if err == nil {
				t.Fatal("dial should fail when the worker refuses")
			}
			if resp != nil {
				status = resp.StatusCode
			}
		})
		assertNoSensitiveKeys(t, events)
		if status != tunnel.HTTPStatusForCode("target_not_allowed") {
			t.Fatalf("status=%d want %d", status, tunnel.HTTPStatusForCode("target_not_allowed"))
		}
		rej := findEvent(events, "tunnel.rejected")
		if rej == nil || rej["error_code"] != "target_not_allowed" || rej["tunnel_id"] != <-hub.opened {
			t.Fatalf("tunnel.rejected: %v (all: %v)", rej, eventNames(events, "tunnel."))
		}
		if findEvent(events, "tunnel.opened") != nil {
			t.Fatalf("refused tunnel must not open: %v", eventNames(events, "tunnel."))
		}
	})

	// The nonce is checked by the worker-connect endpoint, so this "worker"
	// calls back over HTTP like a real one instead of delivering into the
	// registry directly.
	t.Run("wrong nonce", func(t *testing.T) {
		hub := &callingBackHub{opened: make(chan string, 1), nonce: "not-the-nonce", closeCode: make(chan websocket.StatusCode, 1)}
		_, api := newTunnelEventServer(t, hub)
		hub.api = api
		var status int
		events := captureTunnelEvents(t, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, resp, err := dialConnect(ctx, api)
			if err == nil {
				t.Fatal("dial should fail on a nonce mismatch")
			}
			if resp != nil {
				status = resp.StatusCode
			}
			select {
			case code := <-hub.closeCode:
				if code != 4401 {
					t.Fatalf("worker close code = %d, want 4401", code)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("worker callback never closed")
			}
		})
		assertNoSensitiveKeys(t, events)
		if status != http.StatusGatewayTimeout {
			t.Fatalf("status=%d want %d (the bad arrival is dropped, the client times out)", status, http.StatusGatewayTimeout)
		}
		id := <-hub.opened
		var workerSide, clientSide map[string]any
		for _, e := range events {
			if e["event"] != "tunnel.rejected" {
				continue
			}
			if e["side"] == "worker" {
				workerSide = e
			} else {
				clientSide = e
			}
		}
		if workerSide == nil || workerSide["error_code"] != "invalid_nonce" || workerSide["tunnel_id"] != id {
			t.Fatalf("worker-side tunnel.rejected: %v (all: %v)", workerSide, eventNames(events, "tunnel."))
		}
		if clientSide == nil || clientSide["tunnel_id"] != id || clientSide["status"] != float64(http.StatusGatewayTimeout) {
			t.Fatalf("client-side tunnel.rejected: %v", clientSide)
		}
		if findEvent(events, "tunnel.opened") != nil {
			t.Fatalf("nonce mismatch must not open: %v", eventNames(events, "tunnel."))
		}
	})
}

// callingBackHub plays a worker that answers OpenTunnel by dialling the
// server's worker-connect endpoint with a Hello, the way internal/worker does,
// so the server's nonce / worker / instance checks are exercised for real.
type callingBackHub struct {
	api       *httptest.Server
	opened    chan string
	nonce     string // presented instead of the real relay nonce
	closeCode chan websocket.StatusCode
}

func (h *callingBackHub) Accept(http.ResponseWriter, *http.Request, string) {}
func (h *callingBackHub) LiveInstance(string) (string, bool)                { return "inst-1", true }
func (h *callingBackHub) WorkerProtocol(string) (int, bool) {
	return wsproto.CurrentProtocolVersion, true
}
func (h *callingBackHub) OpenTunnel(_ string, id, _ string, _ string, nonce string) error {
	h.opened <- id
	if h.nonce != "" {
		nonce = h.nonce
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hdr := http.Header{}
		hdr.Set("Authorization", "Bearer worker-token")
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.api.URL, "http")+tunnel.WorkerConnectPath, &websocket.DialOptions{HTTPHeader: hdr})
		if err != nil {
			h.closeCode <- -1
			return
		}
		if err := wsjson.Write(ctx, c, tunnel.Hello{TunnelID: id, RelayNonce: nonce}); err != nil {
			h.closeCode <- -1
			return
		}
		_, _, err = c.Read(ctx) // the server answers a rejected hello with a close frame
		h.closeCode <- websocket.CloseStatus(err)
	}()
	return nil
}

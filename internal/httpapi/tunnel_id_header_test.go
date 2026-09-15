package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tunnel"
)

// deliveringHub answers OpenTunnel the way a live worker would, minus the
// worker-connect HTTP leg: it dials a websocket to an echo server and hands it
// straight to the registry as the worker's arrival for that tunnel id, so the
// client connect completes the rendezvous and upgrades.
type deliveringHub struct {
	s      *Server
	echo   *httptest.Server
	opened chan string // tunnel ids OpenTunnel was called with
}

func (h *deliveringHub) Accept(http.ResponseWriter, *http.Request, string) {}
func (h *deliveringHub) LiveInstance(string) (string, bool)                { return "inst-1", true }
func (h *deliveringHub) OpenTunnel(_ string, id, _ string, _ string, nonce string) error {
	h.opened <- id
	go func() {
		c, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(h.echo.URL, "http"), nil)
		if err != nil {
			return
		}
		h.s.tunnels.Deliver(id, tunnel.Arrival{Conn: c, Hello: tunnel.Hello{TunnelID: id, RelayNonce: nonce}})
	}()
	return nil
}

// TestTunnelConnectReturnsTunnelIDHeader: the 101 response of the client
// connect endpoint carries X-Gofer-Tunnel-Id, and it is the same id the server
// asked the worker to open, so a forwarder log line can be matched with the
// server's and the worker's for that tunnel.
func TestTunnelConnectReturnsTunnelIDHeader(t *testing.T) {
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	defer echo.Close()

	s := newTestServerCfg(t, config.ServerConfig{Token: "api-token"})
	hub := &deliveringHub{s: s, echo: echo, opened: make(chan string, 1)}
	s.hub = hub
	s.router = s.buildRouter()
	api := httptest.NewServer(s.Handler())
	defer api.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h := http.Header{}
	h.Set("Authorization", "Bearer api-token")
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(api.URL, "http")+"/v1/tunnels/connect?worker=w1&target=127.0.0.1:80", &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	got := resp.Header.Get(tunnel.HeaderTunnelID)
	want := <-hub.opened
	if got == "" || got != want {
		t.Fatalf("%s = %q, want the id handed to the worker %q", tunnel.HeaderTunnelID, got, want)
	}
	// The tunnel really is spliced: bytes go through to the worker-side conn.
	if err := c.Write(ctx, websocket.MessageBinary, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	_, b, err := c.Read(ctx)
	if err != nil || string(b) != "ping" {
		t.Fatalf("echo through tunnel: %q %v", b, err)
	}
}

package worker

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tunnel"
	"github.com/inhere/gofer/internal/wsproto"
)

// tunnelHelloServer accepts the worker's data websocket and hands back the hello it
// sent, which is where the worker reports whether it accepted the tunnel.
func tunnelHelloServer(t *testing.T) (*httptest.Server, <-chan tunnel.Hello) {
	t.Helper()
	hello := make(chan tunnel.Hello, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		var h tunnel.Hello
		if err := wsjson.Read(r.Context(), c, &h); err == nil {
			hello <- h
		}
	}))
	return srv, hello
}

// TestWorkerRejectsUnallowedUDPTarget pins the authorization boundary for UDP: the
// allowlist is per network, so a tcp entry must not let a udp tunnel reach the same
// host:port. The worker answers not_allowed on the data websocket instead of dialing
// the device — "执行授权留在对端" holds for udp exactly as it does for tcp.
func TestWorkerRejectsUnallowedUDPTarget(t *testing.T) {
	// A UDP socket the worker could dial if the allowlist let it, so a pass really
	// means "refused", not "nothing was listening".
	device, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer device.Close()
	target := device.LocalAddr().String()

	t.Run("tcp entry does not authorize udp", func(t *testing.T) {
		srv, hello := tunnelHelloServer(t)
		defer srv.Close()

		cl := &Client{workerID: "w1", token: "worker-token"}
		cl.applyTunnel(outTunnel(config.WorkerTunnelConfig{Allow: []string{target}}))

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cl.handleTunnelOpen(ctx, srv.URL, wsproto.TunnelOpen{
			TunnelID: "t-udp-denied", Target: target, RelayNonce: "n1", Network: "udp",
		})

		select {
		case h := <-hello:
			if h.ErrorCode != tunnel.CodeNotAllowed {
				t.Fatalf("error_code=%q want %q", h.ErrorCode, tunnel.CodeNotAllowed)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("worker never sent a hello")
		}
	})

	t.Run("udp entry authorizes udp", func(t *testing.T) {
		srv, hello := tunnelHelloServer(t)
		defer srv.Close()

		cl := &Client{workerID: "w1", token: "worker-token"}
		cl.applyTunnel(outTunnel(config.WorkerTunnelConfig{Allow: []string{"udp/" + target}}))

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cl.handleTunnelOpen(ctx, srv.URL, wsproto.TunnelOpen{
			TunnelID: "t-udp-allowed", Target: target, RelayNonce: "n1", Network: "udp",
		})

		select {
		case h := <-hello:
			if h.ErrorCode != "" {
				t.Fatalf("error_code=%q want the tunnel to be accepted (%s)", h.ErrorCode, h.Error)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("worker never sent a hello")
		}
	})
}

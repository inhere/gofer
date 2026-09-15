package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/inhere/gofer/internal/tunnel"
)

func TestTunnelListTunnelsJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"tunnels": []any{map[string]any{"id": "t1", "caller_id": "c", "worker_id": "w", "target": "127.0.0.1:1", "client_remote": "x", "started_at": "2026-01-01T00:00:00Z", "bytes_up": 3, "bytes_down": 4}}})
	}))
	defer ts.Close()
	got, e := New(ts.URL, "").ListTunnels()
	if e != nil || len(got) != 1 {
		t.Fatalf("%v %#v", e, got)
	}
	if got[0].CallerID != "c" || got[0].WorkerID != "w" || got[0].BytesDown != 4 || !got[0].StartedAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("bad %#v", got[0])
	}
}

// wsServer accepts one websocket and optionally stamps the response with the
// tunnel id header, standing in for the gofer server's connect endpoint.
func wsServer(t *testing.T, tunnelID string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tunnelID != "" {
			w.Header().Set(tunnel.HeaderTunnelID, tunnelID)
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		// The upgrade is all these tests look at; hanging up right away keeps
		// httptest.Server.Close from waiting out its hijacked-conn grace period.
		c.Close(websocket.StatusNormalClosure, "")
	}))
}

func TestDialTunnelReadsTunnelIDHeader(t *testing.T) {
	ts := wsServer(t, "tun-42")
	defer ts.Close()
	tc, err := New(ts.URL, "tok").DialTunnel(context.Background(), "w1", "127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Conn.Close(websocket.StatusNormalClosure, "")
	if tc.TunnelID != "tun-42" {
		t.Fatalf("TunnelID = %q, want tun-42", tc.TunnelID)
	}
}

// TestDialTunnelToleratesMissingHeader: a server that predates the header
// still yields a usable tunnel, just without an id to correlate on.
func TestDialTunnelToleratesMissingHeader(t *testing.T) {
	ts := wsServer(t, "")
	defer ts.Close()
	tc, err := New(ts.URL, "tok").DialTunnel(context.Background(), "w1", "127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Conn.Close(websocket.StatusNormalClosure, "")
	if tc.TunnelID != "" || tc.Conn == nil {
		t.Fatalf("TunnelID = %q, Conn nil=%v", tc.TunnelID, tc.Conn == nil)
	}
}

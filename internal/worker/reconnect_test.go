package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/wsproto"
)

// acceptHub is a minimal hub: it accepts a WS, reads the register frame and
// replies registered{accepted:true}, then holds the connection (reading frames,
// replying pong to ping) until the client or server closes. It records each
// accepted registration on regCh so the test can observe (re)connects.
func acceptHub(t *testing.T, regCh chan<- struct{}) *httptest.Server {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{
			InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled,
		})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "bye")
		ctx := req.Context()
		var reg wsproto.Envelope
		if err := wsjson.Read(ctx, conn, &reg); err != nil {
			return
		}
		if err := wsjson.Write(ctx, conn, wsproto.Envelope{
			Type: wsproto.TypeRegistered, Payload: mustRaw(wsproto.Registered{Accepted: true}),
		}); err != nil {
			return
		}
		select {
		case regCh <- struct{}{}:
		default:
		}
		for {
			var env wsproto.Envelope
			if err := wsjson.Read(ctx, conn, &env); err != nil {
				return
			}
			if env.Type == wsproto.TypePing {
				pf, _ := wsproto.As[wsproto.Ping](env)
				_ = wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypePong, Payload: mustRaw(wsproto.Pong{TS: pf.TS})})
			}
		}
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// fastClient builds a client with tiny backoff/heartbeat so reconnect behaviour
// runs in milliseconds. No dispatch is exercised here, so a bare stubJobs suffices.
func fastClient(urls []string) *Client {
	return New(Config{
		WorkerID:       "w1",
		URLs:           urls,
		Token:          "t",
		InitialBackoff: 5 * time.Millisecond,
		MaxBackoff:     20 * time.Millisecond,
		PingInterval:   15 * time.Millisecond,
		ReadDeadline:   60 * time.Millisecond,
	}, &stubJobs{})
}

// TestReconnectBadThenGoodAddress (acceptance #2): urls=[bad.invalid, goodHub].
// The client must dial bad (fail), rotate to the good hub and register there.
func TestReconnectBadThenGoodAddress(t *testing.T) {
	regCh := make(chan struct{}, 4)
	good := acceptHub(t, regCh)
	goodWS := "ws" + strings.TrimPrefix(good.URL, "http") + "/v1/workers/connect"
	badWS := "ws://127.0.0.1:1/v1/workers/connect" // connection refused

	cl := fastClient([]string{badWS, goodWS})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cl.Run(ctx) }()

	select {
	case <-regCh:
		// good: rotated past the bad address and registered on the good hub.
	case <-time.After(3 * time.Second):
		t.Fatal("worker never registered on the good hub after a bad address")
	}
	cancel()
	<-done
}

// TestReconnectTransientHubRestart (acceptance #3): a worker connected to a hub
// that briefly goes away and comes back must re-register (not permanently
// disconnect). Simulated by a hub server that closes the first connection, then
// accepts + registers the reconnect.
func TestReconnectTransientHubRestart(t *testing.T) {
	regCh := make(chan struct{}, 8)
	var conns int
	var mu sync.Mutex
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{
			InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled,
		})
		if err != nil {
			return
		}
		ctx := req.Context()
		var reg wsproto.Envelope
		if err := wsjson.Read(ctx, conn, &reg); err != nil {
			_ = conn.Close(websocket.StatusInternalError, "")
			return
		}
		_ = wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeRegistered, Payload: mustRaw(wsproto.Registered{Accepted: true})})
		regCh <- struct{}{}

		mu.Lock()
		conns++
		first := conns == 1
		mu.Unlock()
		if first {
			// Simulate a transient hub drop: close the first connection right away.
			_ = conn.Close(websocket.StatusGoingAway, "restart")
			return
		}
		// Second (reconnect) connection: hold it so the worker stays up.
		defer conn.Close(websocket.StatusNormalClosure, "bye")
		for {
			var env wsproto.Envelope
			if err := wsjson.Read(ctx, conn, &env); err != nil {
				return
			}
		}
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/workers/connect"

	cl := fastClient([]string{wsURL})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cl.Run(ctx) }()

	// First register, then (after the transient drop) a SECOND register proves the
	// worker reconnected rather than permanently disconnecting.
	for i := 0; i < 2; i++ {
		select {
		case <-regCh:
		case <-time.After(3 * time.Second):
			t.Fatalf("expected register #%d (reconnect after transient drop)", i+1)
		}
	}
	cancel()
	<-done
}

// TestGracefulShutdownNoLeak (acceptance #7, worker side): after Run returns on
// ctx cancel, the reconnect/recv/heartbeat goroutines are all gone (no leak).
func TestGracefulShutdownNoLeak(t *testing.T) {
	regCh := make(chan struct{}, 4)
	srv := acceptHub(t, regCh)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/workers/connect"

	before := runtime.NumGoroutine()

	cl := fastClient([]string{wsURL})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- cl.Run(ctx) }()

	select {
	case <-regCh:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not register")
	}
	// Let the heartbeat goroutine spin a few times.
	time.Sleep(60 * time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}

	// Allow goroutines to wind down, then compare counts (with a tolerance for the
	// test http server's own transient goroutines).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutine leak: before=%d after=%d", before, runtime.NumGoroutine())
}

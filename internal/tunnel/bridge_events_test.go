package tunnel

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
)

func TestBridgeFirstByteHooksAndReason(t *testing.T) {
	ready := make(chan *websocket.Conn, 1)
	leftClient, stopLeft := bridgeTestEndpoint(t, ready)
	defer stopLeft()
	wsRight := <-ready
	deviceA, deviceB := net.Pipe()
	defer deviceA.Close()
	defer deviceB.Close()
	var up, down atomic.Int32
	done := make(chan BridgeResult, 1)
	go func() {
		done <- BridgeWithOptions(context.Background(), wsRight, deviceA, BridgeOptions{
			OnFirstUp: func() { up.Add(1) }, OnFirstDown: func() { down.Add(1) },
		})
	}()
	if err := leftClient.Write(context.Background(), websocket.MessageBinary, []byte("up")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(deviceB, buf); err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() {
		_, rd, err := leftClient.Reader(context.Background())
		if err == nil {
			_, err = io.ReadAll(rd)
		}
		readDone <- err
	}()
	if _, err := deviceB.Write([]byte("down")); err != nil {
		t.Fatal(err)
	}
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	_ = leftClient.Close(websocket.StatusNormalClosure, "done")
	r := <-done
	if r.Reason != ReasonClientClosed {
		t.Fatalf("reason=%q, want %q", r.Reason, ReasonClientClosed)
	}
	if up.Load() != 1 || down.Load() != 1 {
		t.Fatalf("hooks up=%d down=%d", up.Load(), down.Load())
	}
}

func bridgeTestEndpoint(t *testing.T, out chan<- *websocket.Conn) (*websocket.Conn, func()) {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err == nil {
			out <- c
		}
	}))
	c, _, err := websocket.Dial(context.Background(), "ws"+s.URL[4:], nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, func() { _ = c.Close(websocket.StatusNormalClosure, ""); s.Close() }
}

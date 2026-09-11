package tunnel

import (
	"context"
	"github.com/coder/websocket"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func bridgePair(t *testing.T, fn func(*websocket.Conn, net.Conn)) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		a, b := net.Pipe()
		go fn(c, a)
		go func() {
			buf := make([]byte, 64*1024)
			for {
				n, e := b.Read(buf)
				if n > 0 {
					b.Write(buf[:n])
				}
				if e != nil {
					return
				}
			}
		}()
	}))
	t.Cleanup(srv.Close)
	u := "ws" + srv.URL[4:]
	c, _, e := websocket.Dial(context.Background(), u, nil)
	if e != nil {
		t.Fatal(e)
	}
	return c
}

func TestBridgeRoundTripAndText(t *testing.T) {
	done := make(chan struct{})
	var gotTo, gotFrom int64
	c := bridgePair(t, func(ws *websocket.Conn, nc net.Conn) {
		to, from, e := Bridge(context.Background(), ws, nc)
		gotTo, gotFrom = to, from
		if e != nil {
			t.Errorf("bridge: %v", e)
		}
		close(done)
	})
	defer c.Close(websocket.StatusNormalClosure, "")
	_ = c.Write(context.Background(), websocket.MessageText, []byte("ignored"))
	data := make([]byte, 100*1024)
	for i := range data {
		data[i] = byte(i)
	}
	for off := 0; off < len(data); off += MaxChunk {
		end := off + MaxChunk
		if end > len(data) {
			end = len(data)
		}
		_ = c.Write(context.Background(), websocket.MessageBinary, data[off:end])
	}
	var out []byte
	for len(out) < len(data) {
		typ, r, e := c.Reader(context.Background())
		if e != nil || typ != websocket.MessageBinary {
			t.Fatal(e)
		}
		part, _ := io.ReadAll(r)
		out = append(out, part...)
	}
	if string(out) != string(data) {
		t.Fatalf("mismatch %d", len(out))
	}
	c.Close(websocket.StatusNormalClosure, "")
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
	if gotFrom != int64(len(data)) || gotTo != int64(len(data)) {
		t.Fatalf("counts %d %d", gotTo, gotFrom)
	}
}

func TestBridgeContextCancel(t *testing.T) {
	done := make(chan error, 1)
	c := bridgePair(t, func(ws *websocket.Conn, nc net.Conn) {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			tm := time.NewTimer(10 * time.Millisecond)
			defer tm.Stop()
			<-tm.C
			cancel()
		}()
		_, _, e := Bridge(ctx, ws, nc)
		done <- e
	})
	defer c.Close(websocket.StatusNormalClosure, "")
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

package tunnel

import (
	"context"
	"github.com/coder/websocket"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTunnelForwarderRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			if typ == websocket.MessageBinary {
				_ = c.Write(r.Context(), typ, b)
			}
		}
	}))
	defer srv.Close()
	sp := ForwardSpec{Bind: "127.0.0.1", LocalPort: 0, Target: "x:1"}
	f := &Forwarder{Spec: sp, Ready: make(chan error, 1)}
	f.Dial = func(ctx context.Context) (*websocket.Conn, error) {
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
		return c, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.Run(ctx)
	if err := <-f.Ready; err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", f.ActualAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	msg := []byte("hello")
	c.Write(msg)
	got := make([]byte, 5)
	io.ReadFull(c, got)
	if string(got) != "hello" {
		t.Fatalf("%q", got)
	}
}

func TestTunnelForwarderDialFail(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	f := &Forwarder{Spec: ForwardSpec{Bind: "127.0.0.1", LocalPort: 0}, Ready: make(chan error, 1), Dial: func(context.Context) (*websocket.Conn, error) { return nil, io.ErrUnexpectedEOF }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.Run(ctx)
	<-f.Ready
	c, _ := net.Dial("tcp", f.ActualAddr)
	c.SetReadDeadline(time.Now().Add(time.Second))
	b := make([]byte, 1)
	_, e := c.Read(b)
	if e == nil {
		t.Fatal("expected close")
	}
	c.Close()
}
func TestTunnelForwarderListenBusy(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	p := l.Addr().(*net.TCPAddr).Port
	f := &Forwarder{Spec: ForwardSpec{Bind: "127.0.0.1", LocalPort: p}, Ready: make(chan error, 1)}
	if err := f.Run(context.Background()); err == nil {
		t.Fatal("want error")
	}
}
func TestTunnelForwarderCtxCancel(t *testing.T) {
	f := &Forwarder{Spec: ForwardSpec{Bind: "127.0.0.1", LocalPort: 0}, Ready: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()
	<-f.Ready
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
	if _, e := net.DialTimeout("tcp", f.ActualAddr, 100*time.Millisecond); e == nil {
		t.Fatal("listener open")
	}
}

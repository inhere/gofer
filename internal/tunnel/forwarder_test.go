package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
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

// TestTunnelForwarderLogsConnections pins the per-connection output `gofer tunnel
// forward` prints: one line when a tunnel comes up and one when it ends, carrying
// the byte counts (without them a forward tells the user nothing while debugging).
func TestTunnelForwarderLogsConnections(t *testing.T) {
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

	var mu sync.Mutex
	var lines []string
	f := &Forwarder{
		Spec:  ForwardSpec{Bind: "127.0.0.1", LocalPort: 0, Target: "device.example:502"},
		Ready: make(chan error, 1),
		Log: func(format string, args ...any) {
			mu.Lock()
			lines = append(lines, fmt.Sprintf(format, args...))
			mu.Unlock()
		},
	}
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
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 5)
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	c.Close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(lines)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) < 2 {
		t.Fatalf("want a connected and a closed line, got %v", lines)
	}
	if !strings.Contains(lines[0], "connected") || !strings.Contains(lines[0], "device.example:502") {
		t.Fatalf("first line should announce the tunnel: %q", lines[0])
	}
	if !strings.Contains(lines[1], "closed") || !strings.Contains(lines[1], "up=5") || !strings.Contains(lines[1], "down=5") {
		t.Fatalf("close line should carry byte counts: %q", lines[1])
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

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestParseForwardSpecUDPPrefix(t *testing.T) {
	s, err := ParseForwardSpec("udp/1502:192.168.0.200:21845")
	if err != nil {
		t.Fatalf("udp spec should parse: %v", err)
	}
	if s.Network != "udp" || s.LocalPort != 1502 || s.Target != "192.168.0.200:21845" || s.Bind != "127.0.0.1" {
		t.Fatalf("unexpected spec: %#v", s)
	}
	// Without a prefix nothing changes: the existing tcp behaviour is the default.
	plain, err := ParseForwardSpec("1502:192.168.0.200:502")
	if err != nil || plain.Network != "tcp" {
		t.Fatalf("plain spec must stay tcp: %#v %v", plain, err)
	}
	bound, err := ParseForwardSpec("udp/0.0.0.0:1502:10.0.0.5:1234")
	if err != nil || bound.Network != "udp" || bound.Bind != "0.0.0.0" || bound.LocalPort != 1502 {
		t.Fatalf("explicit bind with udp prefix: %#v %v", bound, err)
	}
	v6, err := ParseForwardSpec("udp/1502:[fe80::1]:1234")
	if err != nil || v6.Network != "udp" || v6.Target != "[fe80::1]:1234" {
		t.Fatalf("ipv6 target with udp prefix: %#v %v", v6, err)
	}
	for _, in := range []string{
		"udp/",                   // nothing after the prefix
		"udp//1502:1.2.3.4:1",    // stray slash
		"udp/1502:1.2.3.4",       // missing target port
		"udp/1502:1.2.3.4:0",     // port 0 is not a port
		"udp/1502:1.2.3.4:65536", // out of range
	} {
		if _, err := ParseForwardSpec(in); err == nil {
			t.Errorf("%q should be rejected", in)
		}
	}
}

func TestAllowlistNetworkIsolation(t *testing.T) {
	a, err := ParseAllowlist([]string{"udp/192.168.0.200:21845", "192.168.0.100:502", "tcp/10.0.0.1:1217"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		network, target string
		want            bool
	}{
		{"udp", "192.168.0.200:21845", true},
		{"tcp", "192.168.0.200:21845", false}, // a udp entry must not open the tcp port
		{"tcp", "192.168.0.100:502", true},    // no prefix means tcp
		{"udp", "192.168.0.100:502", false},   // ...and must not open udp
		{"tcp", "10.0.0.1:1217", true},        // explicit tcp/ prefix
		{"udp", "10.0.0.1:1217", false},
	} {
		if got := a.AllowsNetwork(c.network, c.target); got != c.want {
			t.Errorf("AllowsNetwork(%q, %q) = %v, want %v", c.network, c.target, got, c.want)
		}
	}
	// Allows stays the tcp-only shorthand its existing callers rely on.
	if !a.Allows("192.168.0.100:502") || a.Allows("192.168.0.200:21845") {
		t.Fatal("Allows must behave as AllowsNetwork(\"tcp\", ...)")
	}
	// An unknown prefix must not silently become a hostname rule.
	if _, err := ParseAllowlist([]string{"sctp/1.2.3.4:1"}); err == nil {
		t.Fatal("unknown network prefix should be rejected")
	}
	// CIDR entries still work, with and without a network prefix.
	c, err := ParseAllowlist([]string{"udp/10.0.0.0/24:21845"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.AllowsNetwork("udp", "10.0.0.9:21845") || c.AllowsNetwork("tcp", "10.0.0.9:21845") {
		t.Fatal("udp CIDR entry did not match on udp only")
	}
}

// udpEcho starts a UDP server that echoes every datagram back to its sender.
func udpEcho(t *testing.T) (addr string, stop func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 2048)
		for {
			n, from, e := pc.ReadFrom(b)
			if e != nil {
				return
			}
			if _, e := pc.WriteTo(b[:n], from); e != nil {
				return
			}
		}
	}()
	return pc.LocalAddr().String(), func() { pc.Close(); <-done }
}

func TestDatagramBridgeRoundTrip(t *testing.T) {
	echo, stopEcho := udpEcho(t)
	defer stopEcho()

	// The worker side of a udp tunnel: a connected UDP socket to the device.
	dev, err := net.Dial("udp", echo)
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()

	accepted := make(chan *websocket.Conn, 1)
	client, closeSrv := spliceEndpoint(t, accepted)
	defer closeSrv()
	server := <-accepted

	type res struct{ toWS, fromWS int64 }
	done := make(chan res, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		up, down, _ := DatagramBridge(ctx, server, dev)
		done <- res{up, down}
	}()

	payload := []byte("modbus-ish datagram")
	if err := client.Write(ctx, websocket.MessageBinary, payload); err != nil {
		t.Fatal(err)
	}
	typ, got, err := client.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("reply type = %v, want binary", typ)
	}
	if string(got) != string(payload) {
		t.Fatalf("round trip = %q, want %q", got, payload)
	}

	client.Close(websocket.StatusNormalClosure, "")
	select {
	case r := <-done:
		// One datagram each way: what the device sent up, and what we sent down.
		if r.toWS != int64(len(payload)) || r.fromWS != int64(len(payload)) {
			t.Fatalf("counts toWS=%d fromWS=%d, want %d each", r.toWS, r.fromWS, len(payload))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("DatagramBridge did not return")
	}
}

// fakeTunnel stands in for server+worker: every accepted websocket echoes each
// binary message back tagged with its own connection number, so a test can tell
// which tunnel a reply came from and notice sessions bleeding into each other.
type fakeTunnel struct {
	srv      *httptest.Server
	opened   atomic.Int64
	closed   atomic.Int64
	holdOpen time.Duration
}

func newFakeTunnel(t *testing.T) *fakeTunnel {
	t.Helper()
	ft := &fakeTunnel{}
	ft.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		id := ft.opened.Add(1)
		defer func() { ft.closed.Add(1); c.Close(websocket.StatusNormalClosure, "") }()
		for {
			typ, rd, e := c.Reader(r.Context())
			if e != nil {
				return
			}
			b, e := io.ReadAll(rd)
			if e != nil {
				return
			}
			if typ != websocket.MessageBinary {
				continue
			}
			if e := c.Write(r.Context(), websocket.MessageBinary, []byte(fmt.Sprintf("t%d:%s", id, b))); e != nil {
				return
			}
		}
	}))
	return ft
}

func (ft *fakeTunnel) dial(ctx context.Context) (*websocket.Conn, error) {
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ft.srv.URL, "http"), nil)
	return c, err
}

func (ft *fakeTunnel) Close() { ft.srv.Close() }

// startUDPForwarder runs f and waits until it is listening.
func startUDPForwarder(t *testing.T, ctx context.Context, f *Forwarder) {
	t.Helper()
	f.Ready = make(chan error, 1)
	go f.Run(ctx)
	if err := <-f.Ready; err != nil {
		t.Fatal(err)
	}
}

func TestForwarderUDPSessionsPerSource(t *testing.T) {
	ft := newFakeTunnel(t)
	defer ft.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &Forwarder{
		Spec: ForwardSpec{Network: "udp", Bind: "127.0.0.1", LocalPort: 0, Target: "192.168.0.200:21845"},
		Dial: ft.dial,
	}
	startUDPForwarder(t, ctx, f)

	// Two independent local clients: each gets its own source port, so each must get
	// its own tunnel and must never see the other's reply.
	reply := func(t *testing.T, send string) string {
		t.Helper()
		c, err := net.Dial("udp", f.ActualAddr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if _, err := c.Write([]byte(send)); err != nil {
			t.Fatal(err)
		}
		c.SetReadDeadline(time.Now().Add(10 * time.Second))
		b := make([]byte, 256)
		n, err := c.Read(b)
		if err != nil {
			t.Fatalf("no reply for %q: %v", send, err)
		}
		return string(b[:n])
	}

	first := reply(t, "alpha")
	second := reply(t, "beta")

	if !strings.HasSuffix(first, ":alpha") || !strings.HasSuffix(second, ":beta") {
		t.Fatalf("replies crossed sessions: first=%q second=%q", first, second)
	}
	firstTag := strings.SplitN(first, ":", 2)[0]
	secondTag := strings.SplitN(second, ":", 2)[0]
	if firstTag == secondTag {
		t.Fatalf("both sources shared tunnel %s; each source must get its own", firstTag)
	}
	if got := ft.opened.Load(); got != 2 {
		t.Fatalf("opened %d tunnels, want 2 (one per source address)", got)
	}
}

func TestForwarderUDPIdleTimeout(t *testing.T) {
	ft := newFakeTunnel(t)
	defer ft.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &Forwarder{
		Spec:           ForwardSpec{Network: "udp", Bind: "127.0.0.1", LocalPort: 0, Target: "192.168.0.200:21845"},
		Dial:           ft.dial,
		UDPIdleTimeout: 150 * time.Millisecond,
	}
	startUDPForwarder(t, ctx, f)

	c, err := net.Dial("udp", f.ActualAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	b := make([]byte, 256)
	if _, err := c.Read(b); err != nil {
		t.Fatalf("no reply: %v", err)
	}

	// Nothing closes a UDP "connection", so the reaper has to: after the idle window
	// the tunnel must be torn down on its own.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ft.closed.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := ft.closed.Load(); got < 1 {
		t.Fatal("idle udp session was never reaped")
	}

	// A later datagram from the same source opens a fresh tunnel rather than writing
	// into the closed one.
	if _, err := c.Write([]byte("again")); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Read(b); err != nil {
		t.Fatalf("no reply after reap: %v", err)
	}
	if got := ft.opened.Load(); got < 2 {
		t.Fatalf("opened %d tunnels, want a new one after the reap", got)
	}
}

func TestForwarderUDPMaxSessions(t *testing.T) {
	ft := newFakeTunnel(t)
	defer ft.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var logs []string
	f := &Forwarder{
		Spec:           ForwardSpec{Network: "udp", Bind: "127.0.0.1", LocalPort: 0, Target: "192.168.0.200:21845"},
		Dial:           ft.dial,
		UDPMaxSessions: 1,
		Log: func(format string, args ...any) {
			mu.Lock()
			logs = append(logs, fmt.Sprintf(format, args...))
			mu.Unlock()
		},
	}
	startUDPForwarder(t, ctx, f)

	first, err := net.Dial("udp", f.ActualAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := first.Write([]byte("one")); err != nil {
		t.Fatal(err)
	}
	first.SetReadDeadline(time.Now().Add(10 * time.Second))
	b := make([]byte, 256)
	if _, err := first.Read(b); err != nil {
		t.Fatalf("first source got no reply: %v", err)
	}

	second, err := net.Dial("udp", f.ActualAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := second.Write([]byte("two")); err != nil {
		t.Fatal(err)
	}
	second.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if _, err := second.Read(b); err == nil {
		t.Fatal("second source should be dropped once the session cap is reached")
	}
	if got := ft.opened.Load(); got != 1 {
		t.Fatalf("opened %d tunnels, want 1 (the cap)", got)
	}
	mu.Lock()
	defer mu.Unlock()
	var sawDrop bool
	for _, l := range logs {
		if strings.Contains(l, "dropped") {
			sawDrop = true
		}
	}
	if !sawDrop {
		t.Fatalf("hitting the cap should be logged, got %v", logs)
	}
}

package tunnel

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// eventLog collects Forwarder.OnEvent calls so tests can assert on the
// sequence and the attrs of each event.
type eventLog struct {
	mu     sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	name  string
	attrs map[string]any
}

func (l *eventLog) record(event string, attrs ...any) {
	m := map[string]any{}
	for i := 0; i+1 < len(attrs); i += 2 {
		if k, ok := attrs[i].(string); ok {
			m[k] = attrs[i+1]
		}
	}
	l.mu.Lock()
	l.events = append(l.events, recordedEvent{name: event, attrs: m})
	l.mu.Unlock()
}

// names returns the event names in order (a snapshot).
func (l *eventLog) names() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.events))
	for i, e := range l.events {
		out[i] = e.name
	}
	return out
}

// find returns the first event with that name.
func (l *eventLog) find(name string) (recordedEvent, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.events {
		if e.name == name {
			return e, true
		}
	}
	return recordedEvent{}, false
}

// waitFor polls until an event with that name has been recorded.
func (l *eventLog) waitFor(t *testing.T, name string) recordedEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := l.find(name); ok {
			return e
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("event %s never recorded; got %v", name, l.names())
	return recordedEvent{}
}

func int64Attr(t *testing.T, e recordedEvent, key string) int64 {
	t.Helper()
	switch v := e.attrs[key].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	}
	t.Fatalf("%s: attr %s missing or not an integer: %#v", e.name, key, e.attrs[key])
	return 0
}

// assertOrder checks that want appears in got as a subsequence, in order.
func assertOrder(t *testing.T, got, want []string) {
	t.Helper()
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("event order: want %v in order, got %v", want, got)
	}
}

// TestForwarderTCPEventSequence walks one TCP connection through a fake tunnel
// and checks the structured events: order, the tunnel id carried from Dial into
// every tunnel-scoped event, monotonic first-byte timings and byte counts.
func TestForwarderTCPEventSequence(t *testing.T) {
	ft := newFakeTunnel(t)
	defer ft.Close()
	log := &eventLog{}
	f := &Forwarder{Spec: ForwardSpec{Bind: "127.0.0.1", LocalPort: 0, Target: "dev:1"}, Ready: make(chan error, 1), Dial: ft.dial, OnEvent: log.record}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.Run(ctx)
	if err := <-f.Ready; err != nil {
		t.Fatal(err)
	}
	if e := log.waitFor(t, "forward.started"); e.attrs["local"] != f.ActualAddr || e.attrs["network"] != "tcp" {
		t.Fatalf("forward.started attrs: %#v", e.attrs)
	}
	c, err := net.Dial("tcp", f.ActualAddr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 64)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := c.Read(reply)
	if err != nil {
		t.Fatal(err)
	}
	if string(reply[:n]) != "t1:ping" {
		t.Fatalf("reply %q", reply[:n])
	}
	c.Close()
	closed := log.waitFor(t, "session.closed")

	assertOrder(t, log.names(), []string{"forward.started", "session.opened", "tunnel.opened", "tunnel.first_up", "tunnel.first_down", "session.closed"})
	opened, _ := log.find("tunnel.opened")
	up, _ := log.find("tunnel.first_up")
	down, _ := log.find("tunnel.first_down")
	for _, e := range []recordedEvent{opened, up, down, closed} {
		if e.attrs["tunnel_id"] != "fake-1" {
			t.Fatalf("%s: tunnel_id = %#v, want fake-1", e.name, e.attrs["tunnel_id"])
		}
		if e.attrs["session_id"] != c.LocalAddr().String() {
			t.Fatalf("%s: session_id = %#v, want %s", e.name, e.attrs["session_id"], c.LocalAddr())
		}
	}
	if int64Attr(t, opened, "dial_ms") < 0 {
		t.Fatal("dial_ms negative")
	}
	fu, fd := int64Attr(t, up, "first_byte_ms"), int64Attr(t, down, "first_byte_ms")
	if fu < 0 || fd < fu {
		t.Fatalf("first_byte_ms up=%d down=%d", fu, fd)
	}
	if int64Attr(t, closed, "bytes_up") != 4 || int64Attr(t, closed, "bytes_down") != 7 {
		t.Fatalf("bytes: %#v", closed.attrs)
	}
	if closed.attrs["close_reason"] != ReasonClientClosed {
		t.Fatalf("close_reason = %#v", closed.attrs["close_reason"])
	}
	if int64Attr(t, closed, "duration_ms") < 0 {
		t.Fatal("duration_ms negative")
	}
	if _, has := closed.attrs["error"]; has {
		t.Fatalf("clean close carries error: %#v", closed.attrs["error"])
	}
}

// TestForwarderUDPEventSequence does the same for a UDP source, then lets the
// session go idle and expects the reap to close it with reason idle.
func TestForwarderUDPEventSequence(t *testing.T) {
	ft := newFakeTunnel(t)
	defer ft.Close()
	log := &eventLog{}
	f := &Forwarder{Spec: ForwardSpec{Network: "udp", Bind: "127.0.0.1", LocalPort: 0, Target: "dev:1"}, UDPIdleTimeout: 100 * time.Millisecond, Dial: ft.dial, OnEvent: log.record}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startUDPForwarder(t, ctx, f)
	if e := log.waitFor(t, "forward.started"); e.attrs["network"] != "udp" || e.attrs["local"] != f.ActualAddr {
		t.Fatalf("forward.started attrs: %#v", e.attrs)
	}
	c, err := net.Dial("udp", f.ActualAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for i := 0; i < 2; i++ { // two datagrams: first_* must still fire once
		if _, err := c.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		reply := make([]byte, 64)
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := c.Read(reply)
		if err != nil {
			t.Fatal(err)
		}
		if string(reply[:n]) != "t1:ping" {
			t.Fatalf("reply %q", reply[:n])
		}
	}
	closed := log.waitFor(t, "session.closed")

	names := log.names()
	assertOrder(t, names, []string{"forward.started", "session.opened", "tunnel.opened", "tunnel.first_up", "tunnel.first_down", "session.closed"})
	count := func(name string) (n int) {
		for _, x := range names {
			if x == name {
				n++
			}
		}
		return n
	}
	if count("tunnel.first_up") != 1 || count("tunnel.first_down") != 1 {
		t.Fatalf("first_* must fire once per session: %v", names)
	}
	opened, _ := log.find("tunnel.opened")
	up, _ := log.find("tunnel.first_up")
	down, _ := log.find("tunnel.first_down")
	for _, e := range []recordedEvent{opened, up, down, closed} {
		if e.attrs["tunnel_id"] != "fake-1" || e.attrs["session_id"] != c.LocalAddr().String() {
			t.Fatalf("%s: attrs %#v", e.name, e.attrs)
		}
	}
	if fu, fd := int64Attr(t, up, "first_byte_ms"), int64Attr(t, down, "first_byte_ms"); fu < 0 || fd < fu {
		t.Fatalf("first_byte_ms up=%d down=%d", fu, fd)
	}
	if closed.attrs["close_reason"] != ReasonIdle {
		t.Fatalf("close_reason = %#v, want idle", closed.attrs["close_reason"])
	}
	if int64Attr(t, closed, "bytes_up") != 8 || int64Attr(t, closed, "bytes_down") != 14 {
		t.Fatalf("bytes: %#v", closed.attrs)
	}
	if int64Attr(t, closed, "packets_up") != 2 || int64Attr(t, closed, "packets_down") != 2 {
		t.Fatalf("packets: %#v", closed.attrs)
	}
	if _, traced := log.find("tunnel.datagram"); traced {
		t.Fatalf("tunnel.datagram must stay silent unless GOFER_TUNNEL_TRACE=1: %v", log.names())
	}
	if int64Attr(t, closed, "duration_ms") < 100 {
		t.Fatalf("duration_ms = %d, should span the idle window", int64Attr(t, closed, "duration_ms"))
	}
}

// TestForwarderUDPDroppedMaxSessions expects a second source to be refused with
// a session.rejected event (no session existed, so no session.closed for it).
func TestForwarderUDPDroppedMaxSessions(t *testing.T) {
	ft := newFakeTunnel(t)
	defer ft.Close()
	log := &eventLog{}
	f := &Forwarder{Spec: ForwardSpec{Network: "udp", Bind: "127.0.0.1", LocalPort: 0, Target: "dev:1"}, UDPMaxSessions: 1, UDPIdleTimeout: time.Hour, Dial: ft.dial, OnEvent: log.record}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startUDPForwarder(t, ctx, f)
	first, err := net.Dial("udp", f.ActualAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	first.Write([]byte("a"))
	buf := make([]byte, 16)
	first.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := first.Read(buf); err != nil {
		t.Fatal(err)
	}
	second, err := net.Dial("udp", f.ActualAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.Write([]byte("b"))
	rej := log.waitFor(t, "session.rejected")
	if rej.attrs["close_reason"] != ReasonDroppedMaxSessions || rej.attrs["session_id"] != second.LocalAddr().String() {
		t.Fatalf("session.rejected attrs: %#v", rej.attrs)
	}
	if int64Attr(t, rej, "max_sessions") != 1 {
		t.Fatalf("max_sessions: %#v", rej.attrs)
	}
	if _, closed := log.find("session.closed"); closed {
		t.Fatalf("no session should have closed: %v", log.names())
	}
}

// TestForwarderDialFailed covers both networks: the failure is reported as a
// session.closed with reason dial_failed and the error, and no tunnel.* event.
func TestForwarderDialFailed(t *testing.T) {
	boom := errors.New("worker offline")
	for _, network := range []string{"tcp", "udp"} {
		t.Run(network, func(t *testing.T) {
			log := &eventLog{}
			f := &Forwarder{Spec: ForwardSpec{Network: network, Bind: "127.0.0.1", LocalPort: 0, Target: "dev:1"}, Ready: make(chan error, 1), OnEvent: log.record,
				Dial: func(context.Context) (DialResult, error) { return DialResult{}, boom }}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go f.Run(ctx)
			if err := <-f.Ready; err != nil {
				t.Fatal(err)
			}
			c, err := net.Dial(network, f.ActualAddr)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			c.Write([]byte("x"))
			if network == "tcp" {
				// The forwarder closes the accepted conn; with our "x" still
				// unread that surfaces as EOF or a reset, both mean "hung up".
				c.SetReadDeadline(time.Now().Add(5 * time.Second))
				if _, err := c.Read(make([]byte, 1)); err == nil {
					t.Fatal("expected the local conn to be closed after dial failure")
				}
			}
			closed := log.waitFor(t, "session.closed")
			if closed.attrs["close_reason"] != ReasonDialFailed || closed.attrs["error"] != boom {
				t.Fatalf("session.closed attrs: %#v", closed.attrs)
			}
			if closed.attrs["session_id"] != c.LocalAddr().String() {
				t.Fatalf("session_id: %#v", closed.attrs)
			}
			for _, n := range log.names() {
				if n == "tunnel.opened" || n == "tunnel.first_up" || n == "tunnel.first_down" {
					t.Fatalf("no tunnel.* event should follow a dial failure: %v", log.names())
				}
			}
			assertOrder(t, log.names(), []string{"forward.started", "session.opened", "session.closed"})
		})
	}
}

// TestForwarderUDPTraceDatagrams flips the trace switch and expects one
// tunnel.datagram per datagram per direction, with the per-direction gap.
func TestForwarderUDPTraceDatagrams(t *testing.T) {
	old := traceDatagrams
	traceDatagrams = true
	defer func() { traceDatagrams = old }()
	ft := newFakeTunnel(t)
	defer ft.Close()
	log := &eventLog{}
	f := &Forwarder{Spec: ForwardSpec{Network: "udp", Bind: "127.0.0.1", LocalPort: 0, Target: "dev:1"}, UDPIdleTimeout: time.Hour, Dial: ft.dial, OnEvent: log.record}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startUDPForwarder(t, ctx, f)
	c, err := net.Dial("udp", f.ActualAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for i := 0; i < 3; i++ {
		c.Write([]byte("ping"))
		reply := make([]byte, 64)
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Read(reply); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	var up, down []recordedEvent
	for time.Now().Before(deadline) && len(up)+len(down) < 6 {
		up, down = up[:0], down[:0]
		log.mu.Lock()
		for _, e := range log.events {
			if e.name != "tunnel.datagram" {
				continue
			}
			if e.attrs["dir"] == "up" {
				up = append(up, e)
			} else {
				down = append(down, e)
			}
		}
		log.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	if len(up) != 3 || len(down) != 3 {
		t.Fatalf("datagram trace: up=%d down=%d, want 3/3", len(up), len(down))
	}
	if int64Attr(t, up[0], "len") != 4 || int64Attr(t, down[0], "len") != 7 || int64Attr(t, up[0], "gap_ms") != 0 {
		t.Fatalf("first trace records: %#v %#v", up[0].attrs, down[0].attrs)
	}
	for _, e := range append(up, down...) {
		if e.attrs["tunnel_id"] != "fake-1" || e.attrs["session_id"] != c.LocalAddr().String() || int64Attr(t, e, "gap_ms") < 0 {
			t.Fatalf("trace attrs: %#v", e.attrs)
		}
	}
}

package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
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
	"github.com/inhere/gofer/internal/testutil/wait"
)

func socks5Connect(atyp byte, addr []byte, port uint16) []byte {
	b := []byte{0x05, 0x01, 0x00, 0x05, 0x01, 0x00, atyp}
	b = append(b, addr...)
	return binary.BigEndian.AppendUint16(b, port)
}

func TestReadProxyRequest(t *testing.T) {
	domain := append([]byte{byte(len("plc.local"))}, "plc.local"...)
	cases := []struct {
		name      string
		in        []byte
		want      ProxyRequest
		wantErr   string
		wantReply []byte
	}{
		{"socks5 ipv4", socks5Connect(0x01, []byte{10, 20, 1, 5}, 502), ProxyRequest{ProxySOCKS5, "10.20.1.5:502"}, "", []byte{0x05, 0x00}},
		{"socks5 ipv6", socks5Connect(0x04, net.ParseIP("fd00::5").To16(), 443), ProxyRequest{ProxySOCKS5, "[fd00::5]:443"}, "", []byte{0x05, 0x00}},
		{"socks5 domain", socks5Connect(0x03, domain, 8080), ProxyRequest{ProxySOCKS5, "plc.local:8080"}, "", []byte{0x05, 0x00}},
		{"socks5 no no-auth method", []byte{0x05, 0x01, 0x02}, ProxyRequest{}, "no supported auth", []byte{0x05, 0xFF}},
		{"socks5 bind command", []byte{0x05, 0x01, 0x00, 0x05, 0x02, 0x00, 0x01, 10, 0, 0, 1, 0, 80}, ProxyRequest{}, "only CONNECT",
			[]byte{0x05, 0x00, 0x05, socksCmdNotSupported, 0, 1, 0, 0, 0, 0, 0, 0}},
		{"socks5 bad atyp", []byte{0x05, 0x01, 0x00, 0x05, 0x01, 0x00, 0x09}, ProxyRequest{}, "address type", []byte{0x05, 0x00, 0x05, socksAddrTypeNotSupport, 0, 1, 0, 0, 0, 0, 0, 0}},
		{"socks5 port zero", socks5Connect(0x01, []byte{10, 0, 0, 1}, 0), ProxyRequest{}, "port", []byte{0x05, 0x00, 0x05, socksGeneralFailure, 0, 1, 0, 0, 0, 0, 0, 0}},
		{"socks5 truncated", []byte{0x05, 0x01, 0x00, 0x05, 0x01, 0x00, 0x01, 10}, ProxyRequest{}, "EOF", []byte{0x05, 0x00}},
		{"http connect", []byte("CONNECT 10.20.1.5:443 HTTP/1.1\r\nHost: 10.20.1.5:443\r\n\r\n"), ProxyRequest{ProxyHTTP, "10.20.1.5:443"}, "", nil},
		{"http get refused", []byte("GET http://10.20.1.5/ HTTP/1.1\r\nHost: 10.20.1.5\r\n\r\n"), ProxyRequest{}, "only CONNECT", []byte("HTTP/1.1 405 ")},
		{"unknown protocol", []byte{0x04, 0x01}, ProxyRequest{}, "unknown proxy protocol", nil},
	}
	for _, c := range cases {
		var out bytes.Buffer
		got, err := ReadProxyRequest(bufio.NewReader(bytes.NewReader(c.in)), &out)
		if c.wantErr == "" {
			if err != nil || got != c.want {
				t.Errorf("%s: got %+v, %v; want %+v", c.name, got, err, c.want)
			}
		} else if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err=%v, want it to mention %q", c.name, err, c.wantErr)
		}
		if c.wantReply != nil && !bytes.HasPrefix(out.Bytes(), c.wantReply) {
			t.Errorf("%s: wrote % x, want prefix % x", c.name, out.Bytes(), c.wantReply)
		}
	}
}

type statusErr struct {
	status int
	msg    string
}

func (e *statusErr) Error() string     { return fmt.Sprintf("tunnel: HTTP %d: %s", e.status, e.msg) }
func (e *statusErr) TunnelStatus() int { return e.status }

func TestProxyFailure(t *testing.T) {
	cases := []struct {
		err  error
		rep  byte
		code int
	}{
		{&statusErr{403, "target not allowed"}, socksNotAllowed, 403},
		{&statusErr{401, "unauthorized"}, socksNotAllowed, 403},
		{&statusErr{404, "worker offline"}, socksGeneralFailure, 502},
		{&statusErr{429, "too many"}, socksGeneralFailure, 503},
		{&statusErr{502, "dial tcp 10.0.0.1:80: connect: connection refused"}, socksConnRefused, 502},
		{&statusErr{502, "dial tcp 10.0.0.1:80: i/o timeout"}, socksHostUnreachable, 502},
		{&statusErr{504, "timeout"}, socksTTLExpired, 504},
		{fmt.Errorf("wrapped: %w", &statusErr{403, "x"}), socksNotAllowed, 403},
		{errors.New("network down"), socksGeneralFailure, 502},
	}
	for _, c := range cases {
		rep, code := ProxyFailure(c.err)
		if rep != c.rep || code != c.code {
			t.Errorf("%v: got (0x%02x, %d) want (0x%02x, %d)", c.err, rep, code, c.rep, c.code)
		}
	}
}

// echoTunnel is a fake worker end: every websocket it accepts echoes binary frames.
func echoTunnel(t *testing.T) string {
	t.Helper()
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
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// startSOCKS runs a socks forwarder whose dialer records each target and answers
// with deny (when it returns non-nil) or a tunnel to the echo end.
func startSOCKS(t *testing.T, deny func(target string) error) (*Forwarder, *[]string, func() string) {
	t.Helper()
	url := echoTunnel(t)
	var mu sync.Mutex
	var targets, events []string
	f := &Forwarder{Spec: ForwardSpec{Network: NetworkSOCKS, Bind: "127.0.0.1", Target: SOCKSTarget}, Ready: make(chan error, 1)}
	f.DialTarget = func(ctx context.Context, network, target string) (DialResult, error) {
		mu.Lock()
		targets = append(targets, network+"|"+target)
		mu.Unlock()
		if deny != nil {
			if err := deny(target); err != nil {
				return DialResult{}, err
			}
		}
		c, _, err := websocket.Dial(ctx, url, nil)
		return DialResult{Conn: c, TunnelID: "t-test"}, err
	}
	f.OnEvent = func(ev string, attrs ...any) {
		mu.Lock()
		events = append(events, fmt.Sprintln(append([]any{ev}, attrs...)...))
		mu.Unlock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = f.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done; f.active.Wait() })
	if err := <-f.Ready; err != nil {
		t.Fatal(err)
	}
	return f, &targets, func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(events, "\n")
	}
}

func TestSOCKSForwarderSOCKS5RoundTrip(t *testing.T) {
	f, targets, _ := startSOCKS(t, nil)
	c, err := net.Dial("tcp", f.ActualAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Send the greeting, the CONNECT and the first payload in one write: the bytes
	// pipelined after the handshake must still reach the tunnel.
	msg := append(socks5Connect(0x01, []byte{10, 20, 1, 5}, 502), "ping"...)
	if _, err := c.Write(msg); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 2+10)
	if _, err := io.ReadFull(c, reply); err != nil {
		t.Fatal(err)
	}
	if reply[0] != 0x05 || reply[1] != 0x00 || reply[3] != socksSucceeded {
		t.Fatalf("handshake reply % x", reply)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "ping" {
		t.Fatalf("echo %q, %v", got, err)
	}
	if len(*targets) != 1 || (*targets)[0] != "tcp|10.20.1.5:502" {
		t.Fatalf("dialed %v", *targets)
	}
}

func TestSOCKSForwarderHTTPConnect(t *testing.T) {
	f, targets, _ := startSOCKS(t, nil)
	c, err := net.Dial("tcp", f.ActualAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := io.WriteString(c, "CONNECT 10.20.1.5:443 HTTP/1.1\r\nHost: 10.20.1.5:443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("CONNECT answer %v, %v", resp, err)
	}
	_, _ = io.WriteString(c, "hello")
	got := make([]byte, 5)
	if _, err := io.ReadFull(br, got); err != nil || string(got) != "hello" {
		t.Fatalf("echo %q, %v", got, err)
	}
	if len(*targets) != 1 || (*targets)[0] != "tcp|10.20.1.5:443" {
		t.Fatalf("dialed %v", *targets)
	}
}

func TestSOCKSForwarderRefusalReplies(t *testing.T) {
	deny := func(target string) error {
		switch target {
		case "10.20.1.9:22":
			return &statusErr{403, "target not allowed"}
		case "10.20.1.5:503":
			return &statusErr{502, "dial tcp 10.20.1.5:503: connect: connection refused"}
		}
		return &statusErr{429, "tunnel connection limit"}
	}
	f, _, events := startSOCKS(t, deny)
	for _, c := range []struct {
		ip   []byte
		port uint16
		rep  byte
	}{
		{[]byte{10, 20, 1, 9}, 22, socksNotAllowed},
		{[]byte{10, 20, 1, 5}, 503, socksConnRefused},
		{[]byte{10, 20, 1, 7}, 80, socksGeneralFailure},
	} {
		conn, err := net.Dial("tcp", f.ActualAddr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write(socks5Connect(0x01, c.ip, c.port)); err != nil {
			t.Fatal(err)
		}
		reply := make([]byte, 12)
		if _, err := io.ReadFull(conn, reply); err != nil {
			t.Fatal(err)
		}
		if reply[3] != c.rep {
			t.Errorf("%v:%d: REP 0x%02x want 0x%02x", net.IP(c.ip), c.port, reply[3], c.rep)
		}
		conn.Close()
	}
	// HTTP CONNECT gets the HTTP status of the same refusal.
	conn, err := net.Dial("tcp", f.ActualAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "CONNECT 10.20.1.9:22 HTTP/1.1\r\nHost: 10.20.1.9:22\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || resp.StatusCode != 403 {
		t.Fatalf("CONNECT refusal %v, %v", resp, err)
	}
	// A refused dial and a broken handshake are both visible in the event log.
	bad, err := net.Dial("tcp", f.ActualAddr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = bad.Write([]byte{0x04, 0x01})
	_, _ = io.ReadAll(bad)
	bad.Close()
	wait.Until(t, 5*time.Second, "dial_failed and handshake_failed events", func() bool {
		ev := events()
		return strings.Contains(ev, "10.20.1.9:22 close_reason dial_failed") && strings.Contains(ev, "close_reason "+ReasonHandshakeFailed)
	})
}

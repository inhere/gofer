package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/ptyrelay"
)

type attachFakeSource struct {
	outCh chan []byte

	mu       sync.Mutex
	writes   [][]byte
	resizes  [][2]int
	closed   bool
	leftover []byte
}

func newAttachFakeSource() *attachFakeSource {
	return &attachFakeSource{outCh: make(chan []byte, 1024)}
}

func (f *attachFakeSource) Emit(b []byte) { f.outCh <- b }

func (f *attachFakeSource) Read(p []byte) (int, error) {
	if len(f.leftover) > 0 {
		n := copy(p, f.leftover)
		f.leftover = f.leftover[n:]
		return n, nil
	}
	chunk, ok := <-f.outCh
	if !ok {
		return 0, io.EOF
	}
	n := copy(p, chunk)
	if n < len(chunk) {
		f.leftover = append([]byte(nil), chunk[n:]...)
	}
	return n, nil
}

func (f *attachFakeSource) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, append([]byte(nil), p...))
	return len(p), nil
}

func (f *attachFakeSource) Resize(cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes = append(f.resizes, [2]int{cols, rows})
	return nil
}

func (f *attachFakeSource) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *attachFakeSource) Writes() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.writes))
	copy(out, f.writes)
	return out
}

func (f *attachFakeSource) Resizes() [][2]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][2]int, len(f.resizes))
	copy(out, f.resizes)
	return out
}

func (f *attachFakeSource) Closed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func newAttachTestServer(t *testing.T, origins []string) (*Server, *ptyrelay.Registry, string) {
	t.Helper()
	cfg := &config.ServerConfig{
		Token: "api-token",
		Governance: config.GovernanceConfig{
			AttachOrigins: origins,
		},
	}
	s := New(cfg, cfg.Token, false, nil, nil, nil, nil, nil, nil, nil, nil)
	relays := ptyrelay.NewRegistry()
	s.SetPtyRelay(ptyrelay.NewNonceStore(), relays)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, relays, "ws" + strings.TrimPrefix(ts.URL, "http")
}

func openAttachRelay(t *testing.T, relays *ptyrelay.Registry, jobID string, src *attachFakeSource) *ptyrelay.Relay {
	t.Helper()
	nonce := "nonce-" + jobID
	relays.Prepare(ptyrelay.RelayBinding{
		WorkerID:     "w1",
		InstanceID:   "inst-1",
		JobID:        jobID,
		PtySessionID: "pty-" + jobID,
		Nonce:        nonce,
		Expiry:       time.Now().Add(time.Minute).Unix(),
	})
	entry, err := relays.Open(nonce, src)
	if err != nil {
		t.Fatalf("open relay: %v", err)
	}
	return entry.Relay
}

func issueAttachTicket(t *testing.T, s *Server, jobID, mode, origin string, ttl time.Duration) string {
	t.Helper()
	return s.attachTickets.Issue(AttachTicketBinding{
		Caller: "alice",
		JobID:  jobID,
		Mode:   mode,
		Origin: origin,
		Expiry: time.Now().Add(ttl).Unix(),
	})
}

func dialAttach(t *testing.T, base, jobID, ticket, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}
	return websocket.Dial(ctx, base+"/v1/jobs/"+jobID+"/attach?ticket="+ticket, &websocket.DialOptions{
		HTTPHeader: header,
	})
}

func mustDialAttach(t *testing.T, base, jobID, ticket, origin string) *websocket.Conn {
	t.Helper()
	conn, _, err := dialAttach(t, base, jobID, ticket, origin)
	if err != nil {
		t.Fatalf("dial attach: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })
	return conn
}

func readAttachBinary(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read attach frame: %v", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("frame type = %v, want binary", typ)
	}
	return data
}

func writeAttachInput(t *testing.T, conn *websocket.Conn, input []byte) {
	t.Helper()
	writeAttachFrame(t, conn, attachClientFrame{
		Type: "i",
		Data: base64.StdEncoding.EncodeToString(input),
	})
}

func writeAttachResize(t *testing.T, conn *websocket.Conn, cols, rows int) {
	t.Helper()
	writeAttachFrame(t, conn, attachClientFrame{Type: "r", Cols: cols, Rows: rows})
}

func writeAttachFrame(t *testing.T, conn *websocket.Conn, frame attachClientFrame) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write attach frame: %v", err)
	}
}

func waitAttachCond(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met")
}

func TestJobAttachValidTicketOriginOutputInputResize(t *testing.T) {
	const origin = "https://example.com"
	s, relays, base := newAttachTestServer(t, []string{"example.com"})
	src := newAttachFakeSource()
	openAttachRelay(t, relays, "job-1", src)
	ticket := issueAttachTicket(t, s, "job-1", "write", origin, time.Minute)

	conn := mustDialAttach(t, base, "job-1", ticket, origin)
	src.Emit([]byte("worker-out"))
	if got := string(readAttachBinary(t, conn)); got != "worker-out" {
		t.Fatalf("output = %q, want worker-out", got)
	}

	writeAttachInput(t, conn, []byte("stdin"))
	waitAttachCond(t, func() bool { return len(src.Writes()) == 1 })
	if got := string(src.Writes()[0]); got != "stdin" {
		t.Fatalf("input = %q, want stdin", got)
	}

	writeAttachResize(t, conn, 120, 40)
	waitAttachCond(t, func() bool { return len(src.Resizes()) == 1 })
	if got := src.Resizes()[0]; got != [2]int{120, 40} {
		t.Fatalf("resize = %v, want [120 40]", got)
	}
}

func TestJobAttachRejectsMissingExpiredConsumedAndMismatchedTickets(t *testing.T) {
	const origin = "https://example.com"
	s, relays, base := newAttachTestServer(t, []string{"example.com"})
	openAttachRelay(t, relays, "job-1", newAttachFakeSource())

	cases := []struct {
		name   string
		jobID  string
		ticket string
	}{
		{name: "missing", jobID: "job-1"},
		{name: "expired", jobID: "job-1", ticket: issueAttachTicket(t, s, "job-1", "write", origin, -time.Minute)},
		{name: "mismatched job", jobID: "other", ticket: issueAttachTicket(t, s, "job-1", "write", origin, time.Minute)},
		{name: "mismatched origin", jobID: "job-1", ticket: issueAttachTicket(t, s, "job-1", "write", "https://other.example.com", time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, resp, err := dialAttach(t, base, tc.jobID, tc.ticket, origin)
			if err == nil {
				t.Fatalf("dial unexpectedly succeeded")
			}
			if resp == nil || resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %v err=%v, want 401", respStatus(resp), err)
			}
		})
	}

	used := issueAttachTicket(t, s, "job-1", "write", origin, time.Minute)
	conn := mustDialAttach(t, base, "job-1", used, origin)
	_ = conn.Close(websocket.StatusNormalClosure, "consume")
	_, resp, err := dialAttach(t, base, "job-1", used, origin)
	if err == nil {
		t.Fatalf("reused ticket dial unexpectedly succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused ticket status = %v err=%v, want 401", respStatus(resp), err)
	}
}

func TestJobAttachRejectsDisallowedOrigin(t *testing.T) {
	s, relays, base := newAttachTestServer(t, []string{"example.com"})
	openAttachRelay(t, relays, "job-1", newAttachFakeSource())
	ticket := issueAttachTicket(t, s, "job-1", "write", "https://evil.example.com", time.Minute)

	_, resp, err := dialAttach(t, base, "job-1", ticket, "https://evil.example.com")
	if err == nil {
		t.Fatalf("dial unexpectedly succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v err=%v, want 403", respStatus(resp), err)
	}
}

func TestJobAttachSecondWriteViewerDowngradesToReadOnly(t *testing.T) {
	const origin = "https://example.com"
	s, relays, base := newAttachTestServer(t, []string{"example.com"})
	src := newAttachFakeSource()
	openAttachRelay(t, relays, "job-1", src)

	first := mustDialAttach(t, base, "job-1", issueAttachTicket(t, s, "job-1", "write", origin, time.Minute), origin)
	second := mustDialAttach(t, base, "job-1", issueAttachTicket(t, s, "job-1", "write", origin, time.Minute), origin)

	writeAttachInput(t, second, []byte("second"))
	time.Sleep(50 * time.Millisecond)
	if len(src.Writes()) != 0 {
		t.Fatalf("second write viewer should be read-only, writes=%q", src.Writes())
	}

	writeAttachInput(t, first, []byte("first"))
	waitAttachCond(t, func() bool { return len(src.Writes()) == 1 })
	if got := string(src.Writes()[0]); got != "first" {
		t.Fatalf("first writer input = %q, want first", got)
	}
}

func TestJobAttachReplaysScrollback(t *testing.T) {
	const origin = "https://example.com"
	s, relays, base := newAttachTestServer(t, []string{"example.com"})
	src := newAttachFakeSource()
	relay := openAttachRelay(t, relays, "job-1", src)
	pre := []byte("pre-attach")
	src.Emit(pre)
	waitAttachCond(t, func() bool { return relay.RecordedLen() >= len(pre) })

	conn := mustDialAttach(t, base, "job-1", issueAttachTicket(t, s, "job-1", "write", origin, time.Minute), origin)
	if got := readAttachBinary(t, conn); !bytes.Contains(got, pre) {
		t.Fatalf("scrollback = %q, want contain %q", got, pre)
	}
}

func TestJobAttachBrowserDisconnectOnlyRemovesViewer(t *testing.T) {
	const origin = "https://example.com"
	s, relays, base := newAttachTestServer(t, []string{"example.com"})
	src := newAttachFakeSource()
	relay := openAttachRelay(t, relays, "job-1", src)

	conn := mustDialAttach(t, base, "job-1", issueAttachTicket(t, s, "job-1", "write", origin, time.Minute), origin)
	_ = conn.Close(websocket.StatusNormalClosure, "browser left")
	time.Sleep(50 * time.Millisecond)

	if src.Closed() {
		t.Fatalf("browser disconnect closed source")
	}
	if _, ok := relays.Lookup("job-1"); !ok {
		t.Fatalf("relay missing after browser disconnect")
	}
	if err := relay.Resize(80, 24); err != nil {
		t.Fatalf("relay should remain usable after browser disconnect: %v", err)
	}
}

func respStatus(resp *http.Response) any {
	if resp == nil {
		return nil
	}
	return resp.StatusCode
}

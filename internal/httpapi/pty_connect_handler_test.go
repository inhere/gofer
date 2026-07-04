package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/ptyrelay"
	"github.com/inhere/gofer/internal/wshub"
	"github.com/inhere/gofer/internal/wsproto"
)

const (
	ptyTestWorkerID = "w1"
	ptyTestToken    = "worker-token"
	ptyTestInst     = "inst-1"
)

func newPtyConnectTestServer(t *testing.T) (*Server, *ptyrelay.NonceStore, *ptyrelay.Registry, string, *websocket.Conn) {
	t.Helper()
	cfg := &config.ServerConfig{
		Token: "api-token",
		Workers: map[string]config.WorkerAuthConfig{
			ptyTestWorkerID: {Token: ptyTestToken},
		},
	}
	hub := wshub.New(map[string]string{ptyTestWorkerID: ptyTestWorkerID})
	s := New(cfg, cfg.Token, false, nil, nil, nil, nil, hub, nil, nil, nil)
	nonces := ptyrelay.NewNonceStore()
	relays := ptyrelay.NewRegistry()
	s.SetPtyRelay(nonces, relays)

	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	base := "ws" + strings.TrimPrefix(ts.URL, "http")
	hubConn := registerPtyTestWorker(t, base)
	return s, nonces, relays, base, hubConn
}

func registerPtyTestWorker(t *testing.T, base string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, base+"/v1/workers/connect", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + ptyTestToken}},
	})
	if err != nil {
		t.Fatalf("dial worker hub: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type: wsproto.TypeRegister,
		Payload: mustRawForPtyTest(t, wsproto.Register{
			WorkerID:   ptyTestWorkerID,
			InstanceID: ptyTestInst,
		}),
	}); err != nil {
		t.Fatalf("write register: %v", err)
	}
	var env wsproto.Envelope
	if err := wsjson.Read(ctx, conn, &env); err != nil {
		t.Fatalf("read registered: %v", err)
	}
	reg, err := wsproto.As[wsproto.Registered](env)
	if err != nil {
		t.Fatalf("decode registered: %v", err)
	}
	if !reg.Accepted {
		t.Fatalf("worker register rejected: %+v", reg)
	}
	return conn
}

func mustRawForPtyTest(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func preparePtyRelay(t *testing.T, nonces *ptyrelay.NonceStore, relays *ptyrelay.Registry, jobID, sessionID, instanceID string) string {
	t.Helper()
	expiry := time.Now().Add(time.Minute).Unix()
	nonce := nonces.Issue(ptyrelay.NonceBinding{
		WorkerID:     ptyTestWorkerID,
		InstanceID:   instanceID,
		JobID:        jobID,
		PtySessionID: sessionID,
		Expiry:       expiry,
	})
	relays.Prepare(ptyrelay.RelayBinding{
		WorkerID:     ptyTestWorkerID,
		InstanceID:   instanceID,
		JobID:        jobID,
		PtySessionID: sessionID,
		Nonce:        nonce,
		Expiry:       expiry,
	})
	return nonce
}

func dialPtyAndHello(t *testing.T, base string, hello ptyConnectHello) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, base+"/v1/workers/pty-connect", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + ptyTestToken}},
	})
	if err != nil {
		t.Fatalf("dial pty: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })
	if err := wsjson.Write(ctx, conn, hello); err != nil {
		t.Fatalf("write pty hello: %v", err)
	}
	return conn
}

func assertPtyClose(t *testing.T, conn *websocket.Conn, want websocket.StatusCode) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := conn.Read(ctx)
	if got := websocket.CloseStatus(err); got != want {
		t.Fatalf("close status = %v (err %v), want %v", got, err, want)
	}
}

func waitForPtyRelay(t *testing.T, relays *ptyrelay.Registry, jobID string, state ptyrelay.RelayState) *ptyrelay.RelayEntry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e, ok := relays.Lookup(jobID)
		if ok && e.State == state && e.Relay != nil {
			return e
		}
		time.Sleep(10 * time.Millisecond)
	}
	e, ok := relays.Lookup(jobID)
	t.Fatalf("relay %s did not reach %s; last=%+v ok=%v", jobID, state, e, ok)
	return nil
}

func waitRecordedLen(t *testing.T, r *ptyrelay.Relay, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r.RecordedLen() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("recorded len = %d, want >= %d", r.RecordedLen(), want)
}

func TestPtyConnectValidOpenAndFraming(t *testing.T) {
	_, nonces, relays, base, _ := newPtyConnectTestServer(t)
	nonce := preparePtyRelay(t, nonces, relays, "job-1", "pty-1", ptyTestInst)
	conn := dialPtyAndHello(t, base, ptyConnectHello{
		JobID:        "job-1",
		PtySessionID: "pty-1",
		RelayNonce:   nonce,
	})
	entry := waitForPtyRelay(t, relays, "job-1", ptyrelay.RelayOpen)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("worker-out")); err != nil {
		t.Fatalf("write worker binary: %v", err)
	}
	waitRecordedLen(t, entry.Relay, len("worker-out"))

	viewer, err := entry.Relay.AddViewer(true)
	if err != nil {
		t.Fatalf("add writer viewer: %v", err)
	}
	if err := viewer.SendInput([]byte("stdin")); err != nil {
		t.Fatalf("send input: %v", err)
	}
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read input frame: %v", err)
	}
	if typ != websocket.MessageBinary || string(data) != "stdin" {
		t.Fatalf("input frame = type %v data %q, want binary stdin", typ, data)
	}

	if err := entry.Relay.Resize(120, 40); err != nil {
		t.Fatalf("resize: %v", err)
	}
	typ, data, err = conn.Read(ctx)
	if err != nil {
		t.Fatalf("read resize frame: %v", err)
	}
	var resize struct {
		Type string `json:"type"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}
	if typ != websocket.MessageText || json.Unmarshal(data, &resize) != nil ||
		resize.Type != "resize" || resize.Cols != 120 || resize.Rows != 40 {
		t.Fatalf("resize frame = type %v data %s", typ, data)
	}
}

func TestPtyConnectRejectsNonceReplay(t *testing.T) {
	_, nonces, relays, base, _ := newPtyConnectTestServer(t)
	nonce := preparePtyRelay(t, nonces, relays, "job-replay", "pty-replay", ptyTestInst)
	_ = dialPtyAndHello(t, base, ptyConnectHello{
		JobID:        "job-replay",
		PtySessionID: "pty-replay",
		RelayNonce:   nonce,
	})
	_ = waitForPtyRelay(t, relays, "job-replay", ptyrelay.RelayOpen)

	replay := dialPtyAndHello(t, base, ptyConnectHello{
		JobID:        "job-replay",
		PtySessionID: "pty-replay",
		RelayNonce:   nonce,
	})
	assertPtyClose(t, replay, ptyCloseInvalidNonce)
}

func TestPtyConnectRejectsInstanceMismatch(t *testing.T) {
	_, nonces, relays, base, _ := newPtyConnectTestServer(t)
	nonce := preparePtyRelay(t, nonces, relays, "job-inst", "pty-inst", "old-inst")
	conn := dialPtyAndHello(t, base, ptyConnectHello{
		JobID:        "job-inst",
		PtySessionID: "pty-inst",
		RelayNonce:   nonce,
	})
	assertPtyClose(t, conn, ptyCloseInstanceGone)
}

func TestPtyConnectRejectsBindingMismatch(t *testing.T) {
	_, nonces, relays, base, _ := newPtyConnectTestServer(t)
	nonce := preparePtyRelay(t, nonces, relays, "job-real", "pty-real", ptyTestInst)
	conn := dialPtyAndHello(t, base, ptyConnectHello{
		JobID:        "job-other",
		PtySessionID: "pty-real",
		RelayNonce:   nonce,
	})
	assertPtyClose(t, conn, ptyCloseNotFound)
}

func TestPtyConnectRejectsMissingPendingRelay(t *testing.T) {
	_, nonces, _, base, _ := newPtyConnectTestServer(t)
	nonce := nonces.Issue(ptyrelay.NonceBinding{
		WorkerID:     ptyTestWorkerID,
		InstanceID:   ptyTestInst,
		JobID:        "job-missing",
		PtySessionID: "pty-missing",
		Expiry:       time.Now().Add(time.Minute).Unix(),
	})
	conn := dialPtyAndHello(t, base, ptyConnectHello{
		JobID:        "job-missing",
		PtySessionID: "pty-missing",
		RelayNonce:   nonce,
	})
	assertPtyClose(t, conn, ptyCloseNotFound)
}

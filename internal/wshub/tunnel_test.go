package wshub

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/tunnel"
	"github.com/inhere/gofer/internal/wsproto"
)

func registerTunnelWorker(t *testing.T, proto, max int) (context.CancelFunc, *websocket.Conn, *Hub) {
	t.Helper()
	h := New(map[string]string{"w1": "w1"})
	_, url := hubServer(t, h, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	reg := wsproto.Register{WorkerID: "w1", ProtocolVersion: proto, MaxConcurrent: max}
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeRegister, Payload: mustRaw(reg)}); err != nil {
		cancel()
		t.Fatal(err)
	}
	if _, err := readEnvelope(ctx, conn); err != nil {
		cancel()
		t.Fatal(err)
	}
	return cancel, conn, h
}

func TestOpenTunnelOfflineErrorIdentity(t *testing.T) {
	h := New(nil)
	err := h.OpenTunnel("missing", "t1", "", "127.0.0.1:1", "n")
	if !errors.Is(err, ErrWorkerOffline) || !errors.Is(err, tunnel.ErrWorkerOffline) {
		t.Fatalf("error identity: %v", err)
	}
}

func TestOpenTunnelUnsupportedProtocol(t *testing.T) {
	cancel, conn, h := registerTunnelWorker(t, 4, 0)
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")
	if err := h.OpenTunnel("w1", "t1", "", "x:1", "n"); !errors.Is(err, tunnel.ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
	ctx, cancelRead := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancelRead()
	if _, err := readEnvelope(ctx, conn); err == nil {
		t.Fatal("worker received tunnel frame")
	}
}

func TestOpenTunnelProtocol5PayloadAndDefaultNetwork(t *testing.T) {
	cancel, conn, h := registerTunnelWorker(t, 5, 0)
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")
	if err := h.OpenTunnel("w1", "tid", "", "host:9", "nonce"); err != nil {
		t.Fatal(err)
	}
	ctx, cancelRead := context.WithTimeout(context.Background(), time.Second)
	defer cancelRead()
	env, err := readEnvelope(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if env.Type != wsproto.TypeTunnelOpen {
		t.Fatalf("type %q", env.Type)
	}
	open, err := wsproto.As[wsproto.TunnelOpen](env)
	if err != nil {
		t.Fatal(err)
	}
	if open.TunnelID != "tid" || open.Target != "host:9" || open.RelayNonce != "nonce" || open.Network != "tcp" {
		t.Fatalf("payload %+v", open)
	}
}

func TestOpenTunnelDoesNotConsumeJobSlot(t *testing.T) {
	cancel, conn, h := registerTunnelWorker(t, 5, 1)
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")
	if err := h.OpenTunnel("w1", "tid", "tcp", "host:9", "nonce"); err != nil {
		t.Fatal(err)
	}
	sink := newFakeSink()
	if err := h.RegisterSink("w1", "job1", sink); err != nil {
		t.Fatal(err)
	}
	if err := h.Dispatch("w1", wsproto.Dispatch{JobID: "job1"}); err != nil {
		t.Fatalf("dispatch refused: %v", err)
	}
	ctx, cancelRead := context.WithTimeout(context.Background(), time.Second)
	defer cancelRead()
	if env, err := readEnvelope(ctx, conn); err != nil || env.Type != wsproto.TypeTunnelOpen {
		t.Fatalf("tunnel frame: %v %+v", err, env)
	}
}

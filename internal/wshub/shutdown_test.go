package wshub

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/wsproto"
)

// Shutdown returns only after every dropped connection's in-flight jobs have been
// disposed of (gofer-5fiw): serve closes the store right after it, and a disposal
// that ran later failed with "database is closed". New connections are refused.
func TestShutdownWaitsForDisconnectDisposal(t *testing.T) {
	hub := recoverHub(time.Minute)
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, _ = dialRegisterRaw(t, ctx, wsURL, wsproto.Register{
		WorkerID: "w1", InstanceID: "inst-1", ProtocolVersion: wsproto.CurrentProtocolVersion,
	})
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })
	sink := newFakeSink()
	sink.suspendDelay = 300 * time.Millisecond
	if err := registerSink(t, hub, "w1", "j1", sink); err != nil {
		t.Fatalf("RegisterSink: %v", err)
	}
	if err := hub.Dispatch("w1", wsproto.Dispatch{JobID: "j1"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if err := hub.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !slices.Contains(sink.snapshot(), "suspend:worker disconnected") {
		t.Fatalf("Shutdown returned before the job was suspended: events=%v", sink.snapshot())
	}

	if conn, _, err := websocket.Dial(ctx, wsURL, nil); err == nil {
		_ = conn.CloseNow()
		t.Fatal("a connection after Shutdown was accepted")
	}
}

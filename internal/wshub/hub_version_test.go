package wshub

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/wsproto"
)

// dialAndRegisterProto dials the hub and sends a register frame carrying an
// explicit protocol_version, returning the registered ack. It is the version-gate
// counterpart to dialAndRegisterInstance (which always reports the current version).
func dialAndRegisterProto(t *testing.T, ctx context.Context, wsURL, workerID string, proto int) wsproto.Registered {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })
	conn.SetReadLimit(1 << 20)
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type: wsproto.TypeRegister,
		Payload: mustRaw(wsproto.Register{
			WorkerID:        workerID,
			ProtocolVersion: proto,
		}),
	}); err != nil {
		t.Fatalf("write register: %v", err)
	}
	env, err := readEnvelope(ctx, conn)
	if err != nil {
		t.Fatalf("read registered: %v", err)
	}
	reg, _ := wsproto.As[wsproto.Registered](env)
	return reg
}

// TestRegisterRejectsOldProtocol (version gate): a pre-federation worker reports no
// protocol_version (decodes to 0) — the hub must reject it with an explicit upgrade
// prompt rather than registering a worker whose capability report is not
// authoritative. The worker must NOT land in the registry.
func TestRegisterRejectsOldProtocol(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reg := dialAndRegisterProto(t, ctx, wsURL, "w1", 0)
	if reg.Accepted {
		t.Fatal("expected rejection for a pre-federation (protocol_version 0) worker")
	}
	if !strings.Contains(reg.Reason, "升级") {
		t.Fatalf("rejection reason must prompt an upgrade, got %q", reg.Reason)
	}
	if _, ok := hub.reg.Get("w1"); ok {
		t.Fatal("a version-rejected worker must not be registered")
	}
}

// TestRegisterRejectsBelowCurrentProtocol: the gate is "< current", not "== 0", so
// any older-but-nonzero version is rejected too (skipped once the current version
// is the lowest possible).
func TestRegisterRejectsBelowCurrentProtocol(t *testing.T) {
	if wsproto.ProtocolVersion <= 1 {
		t.Skip("no below-current, nonzero protocol version to test")
	}
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reg := dialAndRegisterProto(t, ctx, wsURL, "w1", wsproto.ProtocolVersion-1)
	if reg.Accepted {
		t.Fatalf("expected rejection for protocol_version %d (< %d)",
			wsproto.ProtocolVersion-1, wsproto.ProtocolVersion)
	}
	if !strings.Contains(reg.Reason, "升级") {
		t.Fatalf("rejection reason must prompt an upgrade, got %q", reg.Reason)
	}
}

// TestRegisterAcceptsCurrentProtocol: a worker reporting the current protocol
// version passes the gate and registers normally.
func TestRegisterAcceptsCurrentProtocol(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reg := dialAndRegisterProto(t, ctx, wsURL, "w1", wsproto.ProtocolVersion)
	if !reg.Accepted {
		t.Fatalf("current-protocol worker rejected: %+v", reg)
	}
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })
}

// TestRegisterVersionGateRunsAfterBinding: the binding check stays the FIRST gate —
// a worker that fails BOTH (unbound token and an old protocol) is rejected for the
// binding, so the operator sees the security-relevant cause, not a version nag.
func TestRegisterVersionGateRunsAfterBinding(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "other") // token authenticates as a different caller
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reg := dialAndRegisterProto(t, ctx, wsURL, "w1", 0)
	if reg.Accepted {
		t.Fatal("expected rejection")
	}
	if strings.Contains(reg.Reason, "升级") {
		t.Fatalf("binding mismatch must be reported before the version gate, got %q", reg.Reason)
	}
}

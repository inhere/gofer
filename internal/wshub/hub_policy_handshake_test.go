package wshub

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/wsproto"
)

// dialAndWriteRegister dials the hub and sends a register frame WITHOUT reading the
// ack, returning the live conn. Unlike dialAndRegisterProto it leaves the ack on the
// wire so a test can assert what the very FIRST frame the worker reads is.
func dialAndWriteRegister(t *testing.T, ctx context.Context, wsURL, workerID string, proto int) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })
	conn.SetReadLimit(1 << 20)
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type:    wsproto.TypeRegister,
		Payload: mustRaw(wsproto.Register{WorkerID: workerID, ProtocolVersion: proto}),
	}); err != nil {
		t.Fatalf("write register: %v", err)
	}
	return conn
}

// TestRegisteredAckPrecedesRegistryPush is the B3 / §7-N1 regression (验收 6, hub
// side): the registered ack MUST be written before the connection is published to the
// registry, so no broadcast that iterates the registry (a policy push) can slip a
// frame ahead of the ack. We model the broadcast by injecting a policy frame the
// instant the conn appears in the registry (reg.Get ok) — exactly the window a real
// PushPolicyAll runs in. With the fix (Put after ack), the conn only becomes visible
// AFTER the ack write returned, so the worker still reads Registered first.
//
// Falsification: revert to Put-before-ack and the ack via the package-level
// writeEnvelope (bypassing writeMu, §7-N2). Then reg.Get returns ok before the ack is
// on the wire, the injected policy frame races (and, as two concurrent writers, is a
// data race under -race) ahead of it, and the first frame read here is TypePolicy →
// this assertion fails.
func TestRegisteredAckPrecedesRegistryPush(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialAndWriteRegister(t, ctx, wsURL, "w1", wsproto.CurrentProtocolVersion)

	// The conn becomes visible only after Put — which, with the fix, is after the ack
	// write returned. Inject a policy push the moment it is visible.
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })
	wc, _ := hub.reg.Get("w1")
	if err := wc.writeFrame(ctx, wsproto.TypePolicy, "", wsproto.Policy{Rev: 7}); err != nil {
		t.Fatalf("inject policy push: %v", err)
	}

	// First frame the worker reads MUST be the registered ack, never the injected push.
	env, err := readEnvelope(ctx, conn)
	if err != nil {
		t.Fatalf("read first frame: %v", err)
	}
	if env.Type != wsproto.TypeRegistered {
		t.Fatalf("first frame = %q, want %q (a push slipped ahead of the ack)", env.Type, wsproto.TypeRegistered)
	}
	reg, err := wsproto.As[wsproto.Registered](env)
	if err != nil || !reg.Accepted {
		t.Fatalf("first frame not an accepted ack: reg=%+v err=%v", reg, err)
	}

	// The injected push is next, in order — proving the ack merely PRECEDES it, the
	// push is not lost.
	env2, err := readEnvelope(ctx, conn)
	if err != nil {
		t.Fatalf("read second frame: %v", err)
	}
	if env2.Type != wsproto.TypePolicy {
		t.Fatalf("second frame = %q, want %q", env2.Type, wsproto.TypePolicy)
	}
}

// TestAckFailureLeavesRegistryUntouched: if the ack write fails the connection never
// entered the registry, so there is nothing to Remove and no phantom entry is left.
// (Guards the "ack failure → just close, no reg.Remove" simplification.)
func TestAckFailureLeavesRegistryUntouched(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Dial, register, then drop immediately: whether the ack write wins or fails, the
	// registry must never retain w1 after the Accept goroutine returns.
	conn := dialAndWriteRegister(t, ctx, wsURL, "w1", wsproto.CurrentProtocolVersion)
	_ = conn.Close(websocket.StatusNormalClosure, "drop before reading ack")

	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return !ok })
}

// TestV3WorkerStillRegistersAfterV4Bump (验收 2): bumping CurrentProtocolVersion to 4
// must not evict a v3 worker. Min stays 2, so a v3 worker registers and keeps its
// reported version — it just never negotiates policy (SupportsPolicy(3) is false).
func TestV3WorkerStillRegistersAfterV4Bump(t *testing.T) {
	if wsproto.PolicyMinProtocolVersion-1 < wsproto.MinProtocolVersion {
		t.Skip("no below-policy, at-or-above-floor version to test")
	}
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	v3 := wsproto.PolicyMinProtocolVersion - 1 // 3
	_, reg := dialAndRegisterProto(t, ctx, wsURL, "w1", v3)
	if !reg.Accepted {
		t.Fatalf("v%d worker rejected after the v4 bump: %+v", v3, reg)
	}
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })
	wc, _ := hub.reg.Get("w1")
	if got := wc.protocolVersion(); got != v3 {
		t.Fatalf("conn kept protocol %d, want the reported %d", got, v3)
	}
	if wsproto.SupportsPolicy(v3) {
		t.Fatalf("a v%d worker must not be policy-capable", v3)
	}
}

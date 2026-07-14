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
// explicit protocol_version, returning the live conn + the registered ack. It is
// the version-gate counterpart to dialAndRegisterInstance (which always reports the
// version this build implements) — a test uses it to model a worker of ANY vintage,
// which is the whole point of the rolling-upgrade matrix below.
func dialAndRegisterProto(t *testing.T, ctx context.Context, wsURL, workerID string, proto int) (*websocket.Conn, wsproto.Registered) {
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
	return conn, reg
}

// TestRegisterRejectsOldProtocol (version gate): a pre-federation worker reports no
// protocol_version (decodes to 0) — the hub must reject it with an explicit upgrade
// prompt rather than registering a worker whose capability report is not
// authoritative. The worker must NOT land in the registry. Unchanged by the
// floor/implemented split: 0 is below the floor either way.
func TestRegisterRejectsOldProtocol(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, reg := dialAndRegisterProto(t, ctx, wsURL, "w1", 0)
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

// TestRegisterRejectsBelowFloorProtocol: the gate is "< floor", not "== 0", so any
// nonzero version below the floor is rejected too (skipped once the floor is the
// lowest possible).
func TestRegisterRejectsBelowFloorProtocol(t *testing.T) {
	if wsproto.MinProtocolVersion <= 1 {
		t.Skip("no below-floor, nonzero protocol version to test")
	}
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, reg := dialAndRegisterProto(t, ctx, wsURL, "w1", wsproto.MinProtocolVersion-1)
	if reg.Accepted {
		t.Fatalf("expected rejection for protocol_version %d (< floor %d)",
			wsproto.MinProtocolVersion-1, wsproto.MinProtocolVersion)
	}
	if !strings.Contains(reg.Reason, "升级") {
		t.Fatalf("rejection reason must prompt an upgrade, got %q", reg.Reason)
	}
}

// TestRollingUpgradeMatrix is the regression that guards the upgrade path itself:
// this build implements protocol v3, but the fleet it must keep serving still runs
// v2 workers. The gate is the FLOOR, so a v2 worker connects and keeps working with
// the semantics it has always had — it only misses the features its version
// predates, and the hub knows exactly which those are (per-connection negotiation,
// not a hub-wide switch).
//
//	| server | worker | expected                                         |
//	|--------|--------|--------------------------------------------------|
//	| v3     | v2     | registers; dispatch/stream work; no reload        |
//	| v3     | v3     | registers; dispatch/stream work; reload supported |
//
// A single-constant gate (the shape before the split) would fail the first row by
// evicting every v2 worker on its next reconnect.
func TestRollingUpgradeMatrix(t *testing.T) {
	cases := []struct {
		name              string
		workerProto       int
		wantReloadCapable bool
	}{
		{"v2 worker at the compat floor (mid rolling upgrade)", wsproto.MinProtocolVersion, false},
		{"v3 worker on the current protocol", wsproto.CurrentProtocolVersion, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub := New(map[string]string{"w1": "w1"})
			_, wsURL := hubServer(t, hub, "w1")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			conn, reg := dialAndRegisterProto(t, ctx, wsURL, "w1", tc.workerProto)
			if !reg.Accepted {
				t.Fatalf("worker on protocol v%d rejected by a v%d hub: %+v",
					tc.workerProto, wsproto.CurrentProtocolVersion, reg)
			}
			waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })

			wc, _ := hub.reg.Get("w1")
			if got := wc.protocolVersion(); got != tc.workerProto {
				t.Fatalf("conn kept protocol %d, want the version the PEER reported (%d)",
					got, tc.workerProto)
			}
			// Capability negotiation is per connection and independent of the gate.
			if got := wc.supportsReload(); got != tc.wantReloadCapable {
				t.Fatalf("supportsReload() = %v for a v%d worker, want %v",
					got, tc.workerProto, tc.wantReloadCapable)
			}

			// The pre-existing semantics (dispatch → logs → result) must work for BOTH
			// vintages: a v2 worker is not a degraded worker, it is a full worker minus
			// the newest optional frames.
			sink := newFakeSink()
			if err := hub.RegisterSink("w1", "j1", sink); err != nil {
				t.Fatalf("RegisterSink: %v", err)
			}
			if err := hub.Dispatch("w1", wsproto.Dispatch{JobID: "j1", Agent: "shell", Runner: "local"}); err != nil {
				t.Fatalf("Dispatch to a v%d worker: %v", tc.workerProto, err)
			}
			env, err := readEnvelope(ctx, conn)
			if err != nil {
				t.Fatalf("worker did not receive the dispatch frame: %v", err)
			}
			if env.Type != wsproto.TypeDispatch || env.JobID != "j1" {
				t.Fatalf("worker got %q/%q, want a dispatch for j1", env.Type, env.JobID)
			}

			push := func(ty wsproto.FrameType, payload any) {
				if err := wsjson.Write(ctx, conn, wsproto.Envelope{
					Type: ty, JobID: "j1", Payload: mustRaw(payload),
				}); err != nil {
					t.Fatalf("worker push %s: %v", ty, err)
				}
			}
			push(wsproto.TypeLog, wsproto.Log{JobID: "j1", Stream: "stdout", Seq: 1, Text: "one"})
			push(wsproto.TypeResult, wsproto.Result{JobID: "j1", Status: "done"})

			select {
			case res := <-sink.finished:
				if res.Status != "done" {
					t.Fatalf("result status = %q, want done", res.Status)
				}
			case <-ctx.Done():
				t.Fatalf("v%d worker's job never finished on the hub", tc.workerProto)
			}
			if got := sink.snapshot(); len(got) != 2 || got[0] != "log:one" || got[1] != "finish:done" {
				t.Fatalf("stream events = %v, want [log:one finish:done]", got)
			}
		})
	}
}

// TestRegisterVersionGateRunsAfterBinding: the binding check stays the FIRST gate —
// a worker that fails BOTH (unbound token and an old protocol) is rejected for the
// binding, so the operator sees the security-relevant cause, not a version nag.
func TestRegisterVersionGateRunsAfterBinding(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "other") // token authenticates as a different caller
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, reg := dialAndRegisterProto(t, ctx, wsURL, "w1", 0)
	if reg.Accepted {
		t.Fatal("expected rejection")
	}
	if strings.Contains(reg.Reason, "升级") {
		t.Fatalf("binding mismatch must be reported before the version gate, got %q", reg.Reason)
	}
}

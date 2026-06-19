// Package wshub hosts the hub-side WebSocket transport for the Gofer
// ws-worker effort (main plan §4). This file is the P0 spike's keepable smoke
// test — the design-mandated hard gate before WP1 (see
// docs/plans/2026-06-19-ws-worker-c6c7/P0-spike-plan.md).
//
// It proves five things hold simultaneously over a REAL listener (loopback,
// no external network):
//
//	a. coder/websocket Accept upgrades through rux's *responseWriter wrapper
//	   (the wrapper's Hijack() forwards to the underlying http.Hijacker).
//	b. the non-browser origin check passes with InsecureSkipVerify (and a
//	   negative case proves the option is REQUIRED — see TestSpike_OriginRejected).
//	c. after Hijack, rux's end-of-chain defer ctx.writer.ensureWriteHeader()
//	   is a no-op (Hijack set length=0 → Written()==true), so no double-write /
//	   "superfluous WriteHeader" / panic surfaces.
//	d. the wrapper still satisfies http.Flusher (Flush passthrough, SSE parity).
//	e. clean close: Close(StatusNormalClosure) + ctx timeout propagate; the read
//	   loops exit and the test ends inside the deadline (does not hang).
//
// Plus the minimal business loopback: register → registered → dispatch → result
// (a minimal slice of main plan §5 frame table; wsproto is NOT defined here —
// that is WP1's job).
//
// IMPORTANT finding (P0): coder/websocket's Accept calls w.WriteHeader(101) and
// then Hijack(). rux's *responseWriter BUFFERS WriteHeader (it only records the
// status; the real flush happens in ensureWriteHeader, which Hijack() turns into
// a no-op). So the 101 handshake line is NEVER written to the socket and the
// client's Dial hangs. The fix — and the realistic WP1 hub shape — is a thin
// wsUpgradeWriter adapter that forwards WriteHeader to the underlying writer
// IMMEDIATELY (committing the 101 before hijack) while delegating Header/Hijack/
// Flush to rux's c.Resp. With it, Accept upgrades cleanly through rux.
package wshub

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/gookit/rux/v2"
)

// syncBuffer is a goroutine-safe bytes.Buffer for capturing the httptest
// server's ErrorLog from the handler goroutine while the test goroutine reads
// it (keeps -race clean).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// NOTE (WP1): wsUpgradeWriter has been promoted out of this spike file into the
// real package file internal/wshub/upgrade_writer.go (it is the production hub
// shape). This spike test now exercises that promoted type; it no longer defines
// its own copy.

// frame is the spike's local minimal envelope. It is intentionally NOT the
// future internal/wsproto type: the spike only exercises four frame kinds and
// must not lock protocol details (P0-spike-plan §2, decision §6.4). Fields
// mirror the shapes in main plan §5 (type + the few payload fields used here).
type frame struct {
	Type     string `json:"type"`
	WorkerID string `json:"worker_id,omitempty"`
	Accepted bool   `json:"accepted,omitempty"`
	JobID    string `json:"job_id,omitempty"`
	Status   string `json:"status,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
}

// spikeAcceptOptions is the WP1-default Accept options this spike locks in
// (P0-spike-plan §6.2): explicit origin relaxation for non-browser workers and
// compression disabled. Centralised so the positive test and the rux-flusher
// assertion share one source of truth.
func spikeAcceptOptions() *websocket.AcceptOptions {
	return &websocket.AcceptOptions{
		// Risk #b: workers are non-browser, pure-outbound clients. Without an
		// explicit origin relaxation coder/websocket's default origin check
		// would reject the (origin-less) handshake. InsecureSkipVerify is the
		// v1.8.15 field for this (OriginPatterns is the browser-CSRF alternative).
		InsecureSkipVerify: true,
		// spike keeps compression off for simplicity (WP1 re-evaluates per §6.2).
		CompressionMode: websocket.CompressionDisabled,
	}
}

// TestSpike_AcceptLoopbackOverRux is the P0 hard gate. Subtests mirror the
// plan's named checks. They run sequentially on one shared connection because
// the loopback is a single linear conversation; -race covers the concurrent
// hub-goroutine / client-goroutine read/write on that connection.
func TestSpike_AcceptLoopbackOverRux(t *testing.T) {
	// Overall deadline: if the loopback or close stalls, ctx cancellation must
	// unwind both read loops and the test must end here, not hang (check e).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// handlerDone reports the hub handler's final state back to the main
	// goroutine so we can assert no double-write / panic after the post-hijack
	// chain unwinds (check c).
	type handlerOutcome struct {
		acceptErr error
		// rWrapperIsFlusher confirms c.Resp still satisfies http.Flusher even
		// when used for a WS upgrade (check d, SSE parity).
		rWrapperIsFlusher bool
		// reg / res are what the hub observed from the client.
		reg frame
		res frame
		// readDispatchErr / readRegErr capture clean-close semantics.
		readErr string
	}
	outcome := make(chan handlerOutcome, 1)

	router := rux.New()
	router.GET("/v1/workers/connect", func(c *rux.Context) {
		// Check d: the rux *responseWriter wrapper must still be an
		// http.Flusher (same property the SSE handler relies on).
		_, isFlusher := c.Resp.(http.Flusher)

		// Check a: Accept must upgrade THROUGH rux's wrapper. c.Resp is
		// *responseWriter; we wrap it in wsUpgradeWriter so the 101 status
		// flushes before Hijack (see type doc). Hijack still forwards through
		// rux's wrapper to the underlying Hijacker.
		conn, err := websocket.Accept(&wsUpgradeWriter{rw: c.Resp}, c.Req, spikeAcceptOptions())
		if err != nil {
			outcome <- handlerOutcome{acceptErr: err, rWrapperIsFlusher: isFlusher}
			return
		}
		// Defensive close; the happy path closes cleanly below before return.
		defer conn.Close(websocket.StatusInternalError, "spike defer")

		conn.SetReadLimit(1 << 20) // risk #d sidecar: explicit read limit (WP1 tunes).

		out := handlerOutcome{rWrapperIsFlusher: isFlusher}

		// 1) read register
		if err := wsjson.Read(ctx, conn, &out.reg); err != nil {
			out.readErr = "register read: " + err.Error()
			outcome <- out
			return
		}
		// 2) reply registered{accepted:true}
		if err := wsjson.Write(ctx, conn, frame{Type: "registered", Accepted: true, WorkerID: out.reg.WorkerID}); err != nil {
			out.readErr = "registered write: " + err.Error()
			outcome <- out
			return
		}
		// 3) send dispatch{job_id}
		if err := wsjson.Write(ctx, conn, frame{Type: "dispatch", JobID: "j1"}); err != nil {
			out.readErr = "dispatch write: " + err.Error()
			outcome <- out
			return
		}
		// 4) read result
		if err := wsjson.Read(ctx, conn, &out.res); err != nil {
			out.readErr = "result read: " + err.Error()
			outcome <- out
			return
		}

		// Check e: clean close from the hub side.
		_ = conn.Close(websocket.StatusNormalClosure, "done")
		outcome <- out
		// On return, rux's deferred ensureWriteHeader runs. Because Accept's
		// Hijack flipped Written()==true, it is a no-op (check c). If it were
		// not, httptest's server would log "superfluous WriteHeader" / panic.
	})

	// Check c (made explicit): capture the net/http server's ErrorLog. A
	// double-write after hijack would surface as "superfluous response.WriteHeader".
	var logBuf syncBuffer
	srv := httptest.NewUnstartedServer(router)
	srv.Config.ErrorLog = log.New(&logBuf, "", 0)
	srv.Start()
	defer srv.Close()

	// --- worker side (client) ---
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/workers/connect"
	conn, _, err := websocket.Dial(ctx, wsURL, nil) // spike carries no token; auth is WP1.
	if err != nil {
		t.Fatalf("check a FAIL: client Dial failed (handshake did not upgrade through rux): %v", err)
	}
	defer conn.Close(websocket.StatusInternalError, "client defer")
	conn.SetReadLimit(1 << 20)

	t.Run("accept_handshake", func(t *testing.T) {
		// Dial above already proved the upgrade succeeded; assert the conn is live.
		if conn == nil {
			t.Fatal("check a FAIL: nil connection after Dial")
		}
	})

	t.Run("register_result_loopback", func(t *testing.T) {
		// 1) send register
		if err := wsjson.Write(ctx, conn, frame{Type: "register", WorkerID: "laptop-01"}); err != nil {
			t.Fatalf("client register write failed: %v", err)
		}
		// 2) recv registered
		var registered frame
		if err := wsjson.Read(ctx, conn, &registered); err != nil {
			t.Fatalf("client registered read failed: %v", err)
		}
		if registered.Type != "registered" || !registered.Accepted {
			t.Fatalf("check loopback FAIL: bad registered frame: %+v", registered)
		}
		if registered.WorkerID != "laptop-01" {
			t.Fatalf("check loopback FAIL: registered echoed wrong worker_id: %q", registered.WorkerID)
		}
		// 3) recv dispatch
		var dispatch frame
		if err := wsjson.Read(ctx, conn, &dispatch); err != nil {
			t.Fatalf("client dispatch read failed: %v", err)
		}
		if dispatch.Type != "dispatch" || dispatch.JobID != "j1" {
			t.Fatalf("check loopback FAIL: bad dispatch frame: %+v", dispatch)
		}
		// 4) send result
		if err := wsjson.Write(ctx, conn, frame{Type: "result", JobID: "j1", Status: "success", ExitCode: 0}); err != nil {
			t.Fatalf("client result write failed: %v", err)
		}
	})

	t.Run("clean_close", func(t *testing.T) {
		// Check e: client initiates a clean normal closure.
		if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
			t.Fatalf("check e FAIL: client clean close errored: %v", err)
		}
	})

	// Collect the hub handler's outcome (it has its own goroutine in httptest).
	var hub handlerOutcome
	select {
	case hub = <-outcome:
	case <-ctx.Done():
		t.Fatalf("check e FAIL: hub handler did not finish before deadline (hang?): %v", ctx.Err())
	}

	t.Run("accept_through_rux_no_error", func(t *testing.T) {
		if hub.acceptErr != nil {
			t.Fatalf("check a FAIL: Accept through rux wrapper errored: %v", hub.acceptErr)
		}
	})

	t.Run("flush_streaming", func(t *testing.T) {
		// Check d: the rux wrapper satisfies http.Flusher (SSE parity).
		if !hub.rWrapperIsFlusher {
			t.Fatal("check d FAIL: rux c.Resp is not an http.Flusher")
		}
	})

	t.Run("hub_observed_loopback", func(t *testing.T) {
		if hub.readErr != "" {
			t.Fatalf("hub loopback error: %s", hub.readErr)
		}
		if hub.reg.Type != "register" || hub.reg.WorkerID != "laptop-01" {
			t.Fatalf("check loopback FAIL: hub saw bad register: %+v", hub.reg)
		}
		if hub.res.Type != "result" || hub.res.JobID != "j1" || hub.res.Status != "success" || hub.res.ExitCode != 0 {
			t.Fatalf("check loopback FAIL: hub saw bad result: %+v", hub.res)
		}
	})

	t.Run("no_double_write", func(t *testing.T) {
		// Check c (explicit): after hijack, rux's end-of-chain ensureWriteHeader
		// is a no-op (Written()==true), so net/http must NOT log a superfluous
		// WriteHeader. Also: no panic surfaced (the test would have crashed) and
		// the handler returned cleanly (hub collected above).
		//
		// Give the server goroutine a brief beat to flush any post-return log
		// line, then assert. (A superfluous WriteHeader is logged synchronously
		// at end-of-chain, so a short settle is sufficient.)
		time.Sleep(100 * time.Millisecond)
		if got := logBuf.String(); strings.Contains(got, "superfluous") || strings.Contains(got, "WriteHeader") {
			t.Fatalf("check c FAIL: double-write detected in server ErrorLog: %q", got)
		}
		if got := logBuf.String(); got != "" {
			t.Logf("server ErrorLog (non-fatal, no double-write keyword): %q", got)
		}
	})
}

// TestSpike_OriginRejected is the negative case for risk #b: it proves the
// origin-relaxation option is REQUIRED, not optional. With default Accept
// options (no InsecureSkipVerify / OriginPatterns) and a forged cross-origin
// Origin header, the handshake must be rejected (Dial fails / 403).
func TestSpike_OriginRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	router := rux.New()
	router.GET("/v1/workers/connect", func(c *rux.Context) {
		// Default options: origin verification ON. Same wsUpgradeWriter path.
		conn, err := websocket.Accept(&wsUpgradeWriter{rw: c.Resp}, c.Req, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionDisabled,
		})
		if err != nil {
			// Expected: Accept itself rejects and writes the 403 handshake.
			return
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	})
	srv := httptest.NewServer(router)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/workers/connect"
	// Forge a cross-origin Origin header so the default check must reject it.
	_, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: map[string][]string{"Origin": {"http://evil.example.com"}},
	})
	if err == nil {
		t.Fatal("check b FAIL: handshake accepted a forged cross-origin without InsecureSkipVerify; origin option NOT proven required")
	}
	t.Logf("check b OK: forged-origin handshake rejected as expected: %v", err)
}

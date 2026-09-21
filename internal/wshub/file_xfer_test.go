package wshub

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/wsproto"
)

// readFileXferFrame reads one frame off a raw worker connection and asserts it is the
// transfer instruction (job id empty, payload intact). ctx bounds the read so a frame
// that never arrives fails the test instead of hanging it.
func readFileXferFrame(t *testing.T, ctx context.Context, conn *websocket.Conn, want wsproto.FileXfer) {
	t.Helper()
	env, err := readEnvelope(ctx, conn)
	if err != nil {
		t.Fatalf("read file_xfer frame: %v", err)
	}
	if env.Type != wsproto.TypeFileXfer {
		t.Fatalf("frame type = %q, want %q", env.Type, wsproto.TypeFileXfer)
	}
	if env.JobID != "" {
		t.Fatalf("file_xfer job_id = %q, want empty (a transfer is not a job)", env.JobID)
	}
	got, err := wsproto.As[wsproto.FileXfer](env)
	if err != nil {
		t.Fatalf("decode file_xfer payload: %v", err)
	}
	if got != want {
		t.Fatalf("file_xfer payload = %+v, want %+v", got, want)
	}
}

// TestFileXferWorkerBelowProtocolRejected is the XFER-01 capability gate (G032): a
// worker registered below FileXferMinProtocolVersion is REFUSED — with the two versions
// named — and NO frame is written to it (it has no handler for one), and no waiter is
// parked (nothing is queued for a later upgrade).
func TestFileXferWorkerBelowProtocolRejected(t *testing.T) {
	cancel, conn, hub := registerTunnelWorker(t, wsproto.FileXferMinProtocolVersion-1, 0)
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	req := wsproto.FileXfer{XferID: "xf-1", Op: "put", ProjectKey: "alpha", Path: "tmp/in/a.bin", URLPath: "/v1/xfer/xf-1/content"}
	// Grab the connection while it is DEFINITELY registered: the read below is expected
	// to fail (nothing may arrive), and a failed read tears the client side down, after
	// which the hub's teardown may or may not have evicted the entry yet — the waiter
	// bookkeeping on the conn itself is what this assertion is about.
	wc, ok := hub.reg.Get("w1")
	if !ok {
		t.Fatal("worker is not registered")
	}
	_, err := hub.SendFileXfer(context.Background(), "w1", req)
	if !errors.Is(err, ErrFileXferUnsupported) {
		t.Fatalf("SendFileXfer on a v8 worker = %v, want ErrFileXferUnsupported", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "v8") || !strings.Contains(msg, "v9") {
		t.Fatalf("error should name both the peer's version and the required one, got %q", msg)
	}
	rctx, cancelRead := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelRead()
	if _, err := readEnvelope(rctx, conn); err == nil {
		t.Fatal("old worker received a file_xfer frame")
	}
	if n := wc.pendingFileXfers(); n != 0 {
		t.Fatalf("refused transfer left %d waiter(s) parked", n)
	}
}

// TestFileXferResultResolvesWaiterAndRejectsOldPeer covers the hub half of XFER-01:
// the request/result round trip, the orphan-result fallback, and the prompt failure of
// a waiter whose worker drops. (The old-peer REJECTION half of the name lives in
// TestFileXferWorkerBelowProtocolRejected, next to it.)
func TestFileXferResultResolvesWaiterAndRejectsOldPeer(t *testing.T) {
	req := wsproto.FileXfer{
		XferID:     "xf-1",
		Op:         "put",
		ProjectKey: "alpha",
		Path:       "tmp/in/a.bin",
		Size:       7,
		SHA256:     "aa11",
		URLPath:    "/v1/xfer/xf-1/content",
	}

	t.Run("offline worker fails the transfer", func(t *testing.T) {
		hub := New(nil)
		if _, err := hub.SendFileXfer(context.Background(), "nope", req); !errors.Is(err, ErrWorkerOffline) {
			t.Fatalf("got %v, want ErrWorkerOffline", err)
		}
	})

	t.Run("result resolves the waiter", func(t *testing.T) {
		cancel, conn, hub := registerTunnelWorker(t, wsproto.CurrentProtocolVersion, 0)
		defer cancel()
		defer conn.Close(websocket.StatusNormalClosure, "")
		wc, ok := hub.reg.Get("w1")
		if !ok {
			t.Fatal("worker is not registered")
		}

		type outcome struct {
			res wsproto.FileXferResult
			err error
		}
		done := make(chan outcome, 1)
		go func() {
			res, err := hub.SendFileXfer(context.Background(), "w1", req)
			done <- outcome{res, err}
		}()

		rctx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelRead()
		readFileXferFrame(t, rctx, conn, req)

		want := wsproto.FileXferResult{XferID: req.XferID, OK: true, Size: 7, SHA256: "aa11", DurationMS: 3}
		if err := wsjson.Write(rctx, conn, wsproto.Envelope{
			Type:    wsproto.TypeFileXferResult,
			Payload: mustRaw(want),
		}); err != nil {
			t.Fatalf("write file_xfer_result: %v", err)
		}

		select {
		case got := <-done:
			if got.err != nil {
				t.Fatalf("SendFileXfer: %v", got.err)
			}
			if got.res != want {
				t.Fatalf("result = %+v, want %+v", got.res, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("SendFileXfer did not return after the result frame")
		}
		waitFor(t, func() bool { return wc.pendingFileXfers() == 0 })
	})

	t.Run("orphan result goes to the fallback handler", func(t *testing.T) {
		cancel, conn, hub := registerTunnelWorker(t, wsproto.CurrentProtocolVersion, 0)
		defer cancel()
		defer conn.Close(websocket.StatusNormalClosure, "")

		got := make(chan wsproto.FileXferResult, 1)
		hub.SetFileXferResultHandler(func(res wsproto.FileXferResult) { got <- res })
		orphan := wsproto.FileXferResult{XferID: "xf-unknown", OK: false, Error: "exists"}
		rctx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelRead()
		if err := wsjson.Write(rctx, conn, wsproto.Envelope{Type: wsproto.TypeFileXferResult, Payload: mustRaw(orphan)}); err != nil {
			t.Fatalf("write orphan result: %v", err)
		}
		select {
		case res := <-got:
			if res != orphan {
				t.Fatalf("fallback got %+v, want %+v", res, orphan)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("orphan result did not reach the fallback handler")
		}
		// Never fatal: the connection keeps serving (a ping still gets its pong).
		if err := wsjson.Write(rctx, conn, wsproto.Envelope{Type: wsproto.TypePing, Payload: mustRaw(wsproto.Ping{TS: 1})}); err != nil {
			t.Fatalf("write ping after orphan: %v", err)
		}
		env, err := readEnvelope(rctx, conn)
		if err != nil {
			t.Fatalf("read pong: %v", err)
		}
		if env.Type != wsproto.TypePong {
			t.Fatalf("frame after orphan = %q, want pong", env.Type)
		}
	})

	t.Run("disconnect fails the pending waiter promptly", func(t *testing.T) {
		cancel, conn, hub := registerTunnelWorker(t, wsproto.CurrentProtocolVersion, 0)
		defer cancel()

		done := make(chan error, 1)
		go func() {
			_, err := hub.SendFileXfer(context.Background(), "w1", req)
			done <- err
		}()
		rctx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelRead()
		readFileXferFrame(t, rctx, conn, req)

		if err := conn.Close(websocket.StatusNormalClosure, "bye"); err != nil {
			t.Fatalf("close worker conn: %v", err)
		}
		select {
		case err := <-done:
			if !errors.Is(err, ErrWorkerOffline) {
				t.Fatalf("SendFileXfer after the worker dropped = %v, want ErrWorkerOffline", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("SendFileXfer waited for the transfer timeout instead of failing on disconnect")
		}
	})
}

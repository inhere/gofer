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

// A worker below v17 is refused up front with both versions named, no frame is sent
// and no waiter is parked — the summarizer then degrades to the last message.
func TestTranscriptTailWorkerBelowProtocolRejected(t *testing.T) {
	cancel, conn, hub := registerTunnelWorker(t, wsproto.TranscriptTailMinProtocolVersion-1, 0)
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")
	wc, ok := hub.reg.Get("w1")
	if !ok {
		t.Fatal("worker is not registered")
	}
	_, err := hub.SendTranscriptTail(context.Background(), "w1", wsproto.TranscriptTail{ReqID: "r1", Path: "/x/y.jsonl"})
	if !errors.Is(err, ErrTranscriptTailUnsupported) {
		t.Fatalf("v16 worker = %v, want ErrTranscriptTailUnsupported", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "v16") || !strings.Contains(msg, "v17") {
		t.Fatalf("error should name both versions, got %q", msg)
	}
	rctx, cancelRead := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelRead()
	if _, err := readEnvelope(rctx, conn); err == nil {
		t.Fatal("old worker received a transcript_tail frame")
	}
	if n := wc.pendingTranscriptTails(); n != 0 {
		t.Fatalf("refused request left %d waiter(s) parked", n)
	}
	if sup, online := hub.TranscriptTailSupported("w1"); sup && online {
		t.Fatal("v16 worker must not be reported as supporting transcript tail")
	}
}

// The request / result round trip is correlated by req id; a disconnect wakes the
// waiter at once with ErrWorkerOffline.
func TestTranscriptTailRoundTripAndDisconnect(t *testing.T) {
	cancel, conn, hub := registerTunnelWorker(t, wsproto.TranscriptTailMinProtocolVersion, 0)
	defer cancel()
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()

	type out struct {
		res wsproto.TranscriptTailResult
		err error
	}
	got := make(chan out, 1)
	go func() {
		res, err := hub.SendTranscriptTail(ctx, "w1", wsproto.TranscriptTail{ReqID: "r-ok", SessionID: "s1", Path: "/h/s1.jsonl", MaxBytes: 100})
		got <- out{res, err}
	}()
	env, err := readEnvelope(ctx, conn)
	if err != nil || env.Type != wsproto.TypeTranscriptTail || env.JobID != "" {
		t.Fatalf("frame = %+v err=%v", env, err)
	}
	req, _ := wsproto.As[wsproto.TranscriptTail](env)
	if req.ReqID != "r-ok" || req.Path != "/h/s1.jsonl" || req.MaxBytes != 100 {
		t.Fatalf("request payload = %+v", req)
	}
	// A result for an unknown req id is ignored; the matching one completes the wait.
	sendResult(t, ctx, conn, wsproto.TranscriptTailResult{ReqID: "other", OK: true})
	sendResult(t, ctx, conn, wsproto.TranscriptTailResult{ReqID: "r-ok", OK: true, Data: []byte("{}\n"), Size: 3})
	o := <-got
	if o.err != nil || !o.res.OK || string(o.res.Data) != "{}\n" {
		t.Fatalf("result = %+v err=%v", o.res, o.err)
	}

	go func() {
		res, err := hub.SendTranscriptTail(ctx, "w1", wsproto.TranscriptTail{ReqID: "r-drop", Path: "/h/s1.jsonl"})
		got <- out{res, err}
	}()
	if _, err := readEnvelope(ctx, conn); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
	select {
	case o := <-got:
		if !errors.Is(o.err, ErrWorkerOffline) {
			t.Fatalf("after disconnect: %+v err=%v", o.res, o.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter was not woken by the disconnect")
	}
}

func sendResult(t *testing.T, ctx context.Context, conn *websocket.Conn, res wsproto.TranscriptTailResult) {
	t.Helper()
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeTranscriptTailResult, Payload: mustRaw(res)}); err != nil {
		t.Fatal(err)
	}
}

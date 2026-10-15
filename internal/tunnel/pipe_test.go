package tunnel

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestPipePairCarriesFrames: the in-process pair really speaks the websocket framing in
// both directions and closes both ends, which is what a hosted forwarder needs from it.
//
// The pipe is unbuffered (net.Pipe), so — unlike a socket — a write only returns once the
// peer reads it: every direction below writes from a goroutine and reads here, which is the
// same two-goroutine shape Bridge and Splice use at both ends in production.
func TestPipePairCarriesFrames(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, server, err := PipePair(ctx)
	if err != nil {
		t.Fatalf("PipePair: %v", err)
	}
	defer client.CloseNow()
	defer server.CloseNow()

	testPipePairFrame(t, client, server, ctx, "up")
	testPipePairFrame(t, server, client, ctx, "down")

	// A cancelled dial context must not take the pair with it: the caller-side end is used
	// long after the dial returned.
	cancel()
	testPipePairFrame(t, client, server, context.Background(), "still live")

	// CloseNow, not Close: the close handshake needs the peer to read the close frame,
	// and this test's peer is not reading anything at this point.
	_ = client.CloseNow()
	if _, _, err := server.Read(ctx); err == nil {
		t.Fatal("server read after the client closed the pair must fail")
	}
}

// testPipePairFrame writes one binary frame from src and asserts dst receives it verbatim.
func testPipePairFrame(t *testing.T, src, dst *websocket.Conn, ctx context.Context, payload string) {
	t.Helper()
	writeErr := make(chan error, 1)
	go func() { writeErr <- src.Write(ctx, websocket.MessageBinary, []byte(payload)) }()
	typ, got, err := dst.Read(ctx)
	if err != nil {
		t.Fatalf("read %q: %v", payload, err)
	}
	if typ != websocket.MessageBinary || string(got) != payload {
		t.Fatalf("read %v %q, want binary %q", typ, got, payload)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write %q: %v", payload, err)
	}
}

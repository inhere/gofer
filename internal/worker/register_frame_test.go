package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/wsproto"
)

// TestRegisterFirstFrameNotRegistered is the B3 regression (验收 6, worker side): if
// the first frame after register is NOT a registered ack — a policy push that raced
// ahead of the ack, a protocol desync — the worker must fail the handshake with an
// EXPLICIT frame-type error, never mis-decode it As[Registered] into Accepted=false
// with an empty reason (the old bug that surfaced as a bogus "registration rejected"
// and drove a reconnect storm).
//
// Falsification: revert the client to `reg, _ := wsproto.As[wsproto.Registered](env)`
// without the type assertion. Then a policy frame decodes to Accepted=false /
// Reason="" and runSession returns "register rejected: " (empty reason) — this test's
// "expected registered frame" / "policy" assertions then fail, reproducing the exact
// empty-reason symptom.
func TestRegisterFirstFrameNotRegistered(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		ctx := req.Context()
		var reg wsproto.Envelope
		_ = wsjson.Read(ctx, conn, &reg)
		// Send a POLICY frame first, simulating a broadcast that raced ahead of the ack.
		_ = wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypePolicy, Payload: mustRaw(wsproto.Policy{Rev: 9})})
		_ = conn.Close(websocket.StatusPolicyViolation, "test")
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/workers/connect"

	cl := New(Config{WorkerID: "w1", URLs: []string{wsURL}, Token: "t"}, &stubJobs{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	registered, err := cl.runSession(ctx, wsURL)
	if registered {
		t.Fatal("a non-registered first frame must not report registered=true")
	}
	if err == nil {
		t.Fatal("expected a handshake error, got nil")
	}
	if !strings.Contains(err.Error(), "expected registered frame") {
		t.Fatalf("error must name the handshake expectation, got %v", err)
	}
	// The error must name the offending frame type, not collapse to an empty reason.
	if !strings.Contains(err.Error(), string(wsproto.TypePolicy)) {
		t.Fatalf("error must name the got frame type %q, got %v", wsproto.TypePolicy, err)
	}
}

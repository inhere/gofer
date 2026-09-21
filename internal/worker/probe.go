package worker

import (
	"context"
	"fmt"
	"net/http"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/wsproto"
)

// Probe dials one hub address and performs a SINGLE register handshake: it sends
// the register frame the caller built (from the same worker.yaml a real worker
// would start with), reads the ack, closes the connection and returns the ack.
//
// It exists for `gofer worker doctor` (CFG-09): the operator wants to know
// whether THIS machine can reach the hub and whether the hub accepts its
// worker_id + token, before (or instead of) reading the worker log to find out.
//
// It deliberately does NOT reuse Client.runSession: that path publishes the
// connection, seeds the per-session transfer base, opens a policy session and
// then runs the recv loop — none of which a one-shot check may touch. What the
// two DO share is the wire shape (wsproto.Register/Registered), the bearer header
// and the same read envelope helper, so a probe cannot silently drift from what a
// real register sends.
//
// A NOT-accepted ack is not an error: it carries the hub's own reason (worker_id
// not bound to this token, protocol too old) and the caller prints it verbatim.
// Only transport/decode failures come back as an error.
func Probe(ctx context.Context, url, token string, reg wsproto.Register) (wsproto.Registered, error) {
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return wsproto.Registered{}, fmt.Errorf("dial hub %s: %w", url, err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "probe done")
	conn.SetReadLimit(maxWSReadBytes)

	if err := wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeRegister, Payload: mustRaw(reg)}); err != nil {
		return wsproto.Registered{}, fmt.Errorf("send register: %w", err)
	}
	env, err := readEnvelopeOn(ctx, conn)
	if err != nil {
		return wsproto.Registered{}, fmt.Errorf("read registered: %w", err)
	}
	// The first frame MUST be the ack — same assertion (and same reason) as
	// runSession: a stray frame decoded As[Registered] would masquerade as an
	// Accepted=false rejection with an empty reason.
	if env.Type != wsproto.TypeRegistered {
		return wsproto.Registered{}, fmt.Errorf("handshake: expected registered frame, got %q", env.Type)
	}
	ack, err := wsproto.As[wsproto.Registered](env)
	if err != nil {
		return wsproto.Registered{}, fmt.Errorf("decode registered frame: %w", err)
	}
	return ack, nil
}

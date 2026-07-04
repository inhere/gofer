package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	ptyrunner "github.com/inhere/gofer/internal/runner/pty"
)

// ptyConnectPath is the serve-side pty-connect endpoint the worker dials for the
// SECOND (interactive) ws (D-P2-1). It mirrors httpapi's route; the pump derives
// it from the hub session URL (same scheme/host, D-P2-7).
const ptyConnectPath = "/v1/workers/pty-connect"

// ptySession is the narrow view of *ptyrunner.PtySession the pump needs. Keeping
// it an interface lets the pump be unit-tested against a fake session without a
// real pty; *ptyrunner.PtySession satisfies it (Read/WriteInput/Resize/State).
type ptySession interface {
	Read([]byte) (int, error)
	WriteInput([]byte) (int, error)
	Resize(cols, rows int) error
	State() string
}

var _ ptySession = (*ptyrunner.PtySession)(nil)

// ptyConnectHello is the first frame the worker sends on the pty ws. Its json tags
// MUST match the serve endpoint's ptyConnectHello (httpapi/pty_connect_handler.go)
// so the binding check (job_id / pty_session_id / relay_nonce) passes.
type ptyConnectHello struct {
	JobID        string `json:"job_id"`
	PtySessionID string `json:"pty_session_id"`
	RelayNonce   string `json:"relay_nonce"`
}

// ptyResizeMsg is the text control frame the serve side sends to change the window
// size (mirror of httpapi/pty_source.go's Resize; direction: serve→worker).
type ptyResizeMsg struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// pumpPty opens the SECOND, pty-dedicated ws to the serve side (D-P2-1) and pumps
// bytes both ways for one interactive dispatch. It returns a pumpDone chan closed
// when the pump goroutine exits; handleDispatch (T5) joins it before sending the
// terminal Result so the serve-side relay has drained + closed (recordLoop EOF).
//
// Direction (mirror of httpapi/pty_source.remotePtySource):
//   - out (SOLE reader): sess output → conn binary. The PtyRunner handed us sole
//     reader ownership via the observer, so this is the only reader of sess.
//   - in: conn binary → sess.WriteInput; conn text {type:resize} → sess.Resize.
//
// Teardown / disconnect judgement (D-P2-5): sess EOF is a self-teardown
// (selfClosing) and must NOT cancel the local job; an external ws disconnect while
// the session is still starting/running MUST cancel it. A dial/hello/URL failure
// also cancels (the interactive job can never be attached — fail fast).
//
// sess is the narrow ptySession interface; T5 passes the *ptyrunner.PtySession
// returned by waitSession (which satisfies it).
func (cl *Client) pumpPty(ctx context.Context, sessionURL, localID, remoteJobID, ptySessionID, nonce string, sess ptySession) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)

		connectURL, err := derivePtyConnectURL(sessionURL)
		if err != nil {
			_ = cl.jobs.Cancel(localID) // bad hub url → job can never attach
			return
		}

		header := http.Header{}
		if cl.token != "" {
			header.Set("Authorization", "Bearer "+cl.token)
		}
		conn, _, derr := websocket.Dial(ctx, connectURL, &websocket.DialOptions{HTTPHeader: header})
		if derr != nil {
			// Dial failed → the pty ws never came up; cancel so the host is not left
			// waiting on a relay that will never open (host Done(pending)=closed chan).
			_ = cl.jobs.Cancel(localID)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "pty pump end")

		// hello is written serially BEFORE the out goroutine starts so the single-
		// writer constraint (coder/websocket) holds: out is then the only writer.
		if err := wsjson.Write(ctx, conn, ptyConnectHello{
			JobID: remoteJobID, PtySessionID: ptySessionID, RelayNonce: nonce,
		}); err != nil {
			_ = cl.jobs.Cancel(localID)
			return
		}

		// selfClosing distinguishes "sess EOF closed the ws" (natural teardown, do
		// NOT cancel) from "the ws dropped under us" (external, DO cancel). Read/set
		// across the two goroutines → atomic.
		var selfClosing atomic.Bool

		// out: the SOLE reader of sess. On sess error (EOF/teardown) it flags
		// selfClosing and closes the ws so the serve recordLoop sees EOF.
		outDone := make(chan struct{})
		go func() {
			defer close(outDone)
			buf := make([]byte, 32*1024)
			for {
				n, rerr := sess.Read(buf)
				if n > 0 {
					if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
						break // ws gone → let the disconnect judgement (below) decide
					}
				}
				if rerr != nil {
					selfClosing.Store(true) // sess EOF = this end is tearing down
					break
				}
			}
			_ = conn.Close(websocket.StatusNormalClosure, "pty eof")
		}()

		// in: runs in THIS goroutine (reader only). Returns when the ws read errors.
		cl.ptyInputLoop(ctx, conn, sess)

		// The ws read ended. If WE did not tear it down (sess still starting/running,
		// not already cancelling/exiting/closed) it was an external disconnect →
		// cancel the local job (D-P2-5: starting counts too).
		if !selfClosing.Load() {
			switch sess.State() {
			case ptyrunner.StateCancelling, ptyrunner.StateExiting, ptyrunner.StateClosed:
				// already tearing down (our own teardown / natural exit) → do not cancel
			default: // starting / running → external drop killed the attach
				_ = cl.jobs.Cancel(localID)
			}
		}

		<-outDone
	}()
	return done
}

// ptyInputLoop reads inbound ws frames and applies them to the session: binary =
// stdin bytes (WriteInput), text {type:resize} = window resize (clamped). It is a
// reader only (never writes the ws) so the single-writer constraint is preserved.
// It returns when the ws read errors (remote close / ctx cancel).
func (cl *Client) ptyInputLoop(ctx context.Context, conn *websocket.Conn, sess ptySession) {
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		switch typ {
		case websocket.MessageBinary:
			_, _ = sess.WriteInput(data)
		case websocket.MessageText:
			var ctrl ptyResizeMsg
			if json.Unmarshal(data, &ctrl) == nil && ctrl.Type == "resize" {
				c, r := clampSize(ctrl.Cols, ctrl.Rows)
				_ = sess.Resize(c, r)
			}
		}
	}
}

// derivePtyConnectURL keeps the hub session URL's scheme/host and swaps the path
// for the pty-connect endpoint (D-P2-7 per-dispatch). A URL with no host is a
// fail-fast error (the caller cancels rather than dial garbage).
func derivePtyConnectURL(hubURL string) (string, error) {
	u, err := url.Parse(hubURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("pty pump: bad hub url %q", hubURL)
	}
	u.Path = ptyConnectPath
	u.RawQuery = ""
	return u.String(), nil
}

// clampSize bounds a requested window size to sane limits (cols 1..500, rows
// 1..200) before it reaches the pty, so a malformed/hostile resize cannot pass an
// absurd size to the terminal.
func clampSize(cols, rows int) (int, int) {
	if cols < 1 {
		cols = 1
	} else if cols > 500 {
		cols = 500
	}
	if rows < 1 {
		rows = 1
	} else if rows > 200 {
		rows = 200
	}
	return cols, rows
}

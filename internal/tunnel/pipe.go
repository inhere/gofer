package tunnel

import (
	"context"
	"net"
	"net/http"
	"sync"

	"github.com/coder/websocket"
)

// PipePair returns the two ends of an IN-PROCESS websocket pair: a client end and a server
// end, both real *websocket.Conn speaking the RFC 6455 framing over an in-memory pipe
// (net.Pipe) — no socket, no listener to bind and no bearer token involved.
//
// It is what lets a server-side component dial the process it already runs in. The hosted
// forwarders use it to open their tunnels themselves instead of calling the server's own
// HTTP tunnel endpoint with the server's own token (design §6「托管转发改为进程内拨号，不再借
// server 自己的 token 走 tunnelConnect」): the caller-side end goes to the Forwarder, the
// server-side end is spliced with the worker's data connection, exactly like the two ends
// the HTTP tunnel endpoint hands to Splice.
//
// ctx only bounds the in-process handshake; the returned connections live until they are
// closed (they are NOT tied to ctx, so a dialer may cancel its request context as soon as
// the dial returns).
func PipePair(ctx context.Context) (client, server *websocket.Conn, err error) {
	clientConn, serverConn := net.Pipe()
	ln := &singleConnListener{conn: serverConn}
	accepted := make(chan acceptResult, 1)
	// http.Serve hands the pipe's server end to the handler, which performs the very same
	// handshake Accept does for a real listener: it hijacks the connection and returns the
	// server-side websocket. The listener stops accepting after that one connection, so
	// Serve returns and leaves the hijacked connection alone.
	go func() {
		_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ws, e := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
			accepted <- acceptResult{ws: ws, err: e}
		}))
	}()
	// The dial never leaves the process: the transport hands the handshake request to the
	// other end of the in-memory pipe instead of opening a socket.
	clientWS, _, err := websocket.Dial(ctx, "ws://gofer.in-process/tunnel", &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{
			DialContext: func(context.Context, string, string) (net.Conn, error) { return clientConn, nil },
		}},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		_ = clientConn.Close()
		_ = serverConn.Close()
		return nil, nil, err
	}
	res := <-accepted
	if res.err != nil {
		_ = clientWS.CloseNow()
		_ = serverConn.Close()
		return nil, nil, res.err
	}
	return clientWS, res.ws, nil
}

// acceptResult carries the server end of a PipePair handshake back to the dialer.
type acceptResult struct {
	ws  *websocket.Conn
	err error
}

// singleConnListener hands out ONE connection and reports the listener closed on the next
// Accept, so http.Serve returns as soon as it has started that connection's goroutine. The
// connection itself is hijacked by websocket.Accept and outlives the listener, which is why
// Close has nothing to do.
type singleConnListener struct {
	conn net.Conn
	once sync.Once
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() { c = l.conn })
	if c == nil {
		return nil, net.ErrClosed
	}
	return c, nil
}

func (l *singleConnListener) Close() error { return nil }

func (l *singleConnListener) Addr() net.Addr { return inProcessAddr{} }

// inProcessAddr names a listener that binds nothing.
type inProcessAddr struct{}

func (inProcessAddr) Network() string { return "in-process" }
func (inProcessAddr) String() string  { return "gofer.in-process" }

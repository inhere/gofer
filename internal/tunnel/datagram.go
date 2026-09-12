package tunnel

import (
	"context"
	"github.com/coder/websocket"
	"io"
	"net"
	"sync"
)

// DatagramBridge maps one UDP datagram to one binary websocket message.
func DatagramBridge(ctx context.Context, ws *websocket.Conn, conn net.Conn) (toWS, fromWS int64, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var once sync.Once
	stop := func() { once.Do(func() { conn.Close(); ws.Close(websocket.StatusNormalClosure, "closed") }) }
	type result struct {
		n   int64
		err error
		out bool
	}
	ch := make(chan result, 2)
	go func() {
		var n int64
		b := make([]byte, ReadLimit)
		for {
			nr, e := conn.Read(b)
			if nr > 0 {
				if e2 := ws.Write(ctx, websocket.MessageBinary, b[:nr]); e2 != nil {
					e = e2
				} else {
					n += int64(nr)
				}
			}
			if e != nil {
				ch <- result{n, e, true}
				stop()
				return
			}
		}
	}()
	go func() {
		var n int64
		for {
			typ, r, e := ws.Reader(ctx)
			if e != nil {
				ch <- result{n, e, false}
				stop()
				return
			}
			if typ != websocket.MessageBinary {
				io.Copy(io.Discard, r)
				continue
			}
			b, e := io.ReadAll(io.LimitReader(r, ReadLimit+1))
			if e == nil && len(b) <= ReadLimit {
				_, e = conn.Write(b)
				if e == nil {
					n += int64(len(b))
				}
			} else if e == nil {
				e = io.ErrShortBuffer
			}
			if e != nil {
				ch <- result{n, e, false}
				stop()
				return
			}
		}
	}()
	a, b := <-ch, <-ch
	if a.out {
		toWS = a.n
		fromWS = b.n
	} else {
		fromWS = a.n
		toWS = b.n
	}
	if a.err == nil || a.err == io.EOF || a.err == net.ErrClosed || websocket.CloseStatus(a.err) == websocket.StatusNormalClosure {
		return toWS, fromWS, nil
	}
	return toWS, fromWS, a.err
}

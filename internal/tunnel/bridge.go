package tunnel

import (
	"context"
	"io"
	"net"
	"sync"

	"github.com/coder/websocket"
)

// MaxChunk is the maximum TCP read forwarded in one websocket message.
const MaxChunk = 32 * 1024

// ReadLimit is the maximum websocket message payload consumed.
const ReadLimit = 64 * 1024

// Bridge forwards binary websocket messages and TCP bytes in both directions.
// toWS counts TCP bytes sent to websocket; fromWS counts websocket bytes sent to TCP.
func Bridge(ctx context.Context, ws *websocket.Conn, c net.Conn) (toWS, fromWS int64, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type res struct {
		n    int64
		err  error
		toWS bool
	}
	ch := make(chan res, 2)
	var once sync.Once
	stop := func() { once.Do(func() { c.Close(); _ = ws.Close(websocket.StatusNormalClosure, "closed") }) }
	go func() {
		var n int64
		for {
			typ, r, e := ws.Reader(ctx)
			if e != nil {
				ch <- res{n, e, false}
				stop()
				return
			}
			if typ != websocket.MessageBinary {
				io.Copy(io.Discard, r)
				continue
			}
			b, e := io.ReadAll(io.LimitReader(r, ReadLimit))
			if e == nil {
				_, e = c.Write(b)
				n += int64(len(b))
			}
			if e != nil {
				ch <- res{n, e, false}
				stop()
				return
			}
		}
	}()
	go func() {
		b := make([]byte, MaxChunk)
		var n int64
		for {
			nr, e := c.Read(b)
			if nr > 0 {
				e2 := ws.Write(ctx, websocket.MessageBinary, b[:nr])
				n += int64(nr)
				if e2 != nil {
					e = e2
				}
			}
			if e != nil {
				ch <- res{n, e, true}
				stop()
				return
			}
		}
	}()
	x := <-ch
	y := <-ch
	var to, from int64
	if x.toWS {
		to = x.n
	} else {
		from = x.n
	}
	if y.toWS {
		to = y.n
	} else {
		from = y.n
	}
	if x.err == nil || x.err == io.EOF || x.err == net.ErrClosed || websocket.CloseStatus(x.err) == websocket.StatusNormalClosure {
		return to, from, nil
	}
	return to, from, x.err
}

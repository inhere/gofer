package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/coder/websocket"
)

// BridgeOptions observes the first successfully forwarded bytes in each
// direction. Up goes from websocket to device; Down goes from device to websocket.
type BridgeOptions struct{ OnFirstUp, OnFirstDown func() }

// BridgeResult retains the device-relative counters of Bridge and reports the
// first teardown cause using the same reason vocabulary as SpliceResult.
type BridgeResult struct {
	ToWS, FromWS int64
	Reason       string
	Err          error
}
type bridgeRes struct {
	n    int64
	err  error
	toWS bool
}

// MaxChunk is the maximum TCP read forwarded in one websocket message.
const MaxChunk = 32 * 1024

// ReadLimit is the maximum websocket message payload consumed.
const ReadLimit = 64 * 1024

// Bridge forwards binary websocket messages and TCP bytes in both directions.
// toWS counts TCP bytes sent to websocket; fromWS counts websocket bytes sent to TCP.
func Bridge(ctx context.Context, ws *websocket.Conn, c net.Conn) (int64, int64, error) {
	r := BridgeWithOptions(ctx, ws, c, BridgeOptions{})
	return r.ToWS, r.FromWS, r.Err
}

// BridgeWithOptions is Bridge with first-byte callbacks and a teardown result.
func BridgeWithOptions(ctx context.Context, ws *websocket.Conn, c net.Conn, opt BridgeOptions) BridgeResult {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch := make(chan bridgeRes, 2)
	var once sync.Once
	var firstUp, firstDown sync.Once
	stop := func() { once.Do(func() { c.Close(); _ = ws.Close(websocket.StatusNormalClosure, "closed") }) }
	go func() {
		var n int64
		for {
			typ, r, e := ws.Reader(ctx)
			if e != nil {
				ch <- bridgeRes{n, e, false}
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
				if e == nil {
					n += int64(len(b))
					firstUp.Do(func() {
						if opt.OnFirstUp != nil {
							opt.OnFirstUp()
						}
					})
				}
			}
			if e != nil {
				ch <- bridgeRes{n, e, false}
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
				if e2 == nil {
					n += int64(nr)
					firstDown.Do(func() {
						if opt.OnFirstDown != nil {
							opt.OnFirstDown()
						}
					})
				}
				if e2 != nil {
					e = e2
				}
			}
			if e != nil {
				ch <- bridgeRes{n, e, true}
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
	reason, err := closeReason(ctx, x.err, x.toWS)
	return BridgeResult{ToWS: to, FromWS: from, Reason: reason, Err: err}
}

const (
	ReasonClientClosed = "client_closed"
	ReasonWorkerClosed = "worker_closed"
	ReasonIdleTimeout  = "idle_timeout"
	ReasonContextDone  = "ctx_done"
	ReasonError        = "error"
	ReasonPingTimeout  = "ping_timeout"
)

func closeReason(ctx context.Context, err error, workerSide bool) (string, error) {
	if ctx.Err() != nil {
		return ReasonContextDone, ctx.Err()
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ReasonIdleTimeout, err
	}
	status := websocket.CloseStatus(err)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) &&
		status != websocket.StatusNormalClosure && status != websocket.StatusGoingAway {
		return ReasonError, err
	}
	if workerSide {
		return ReasonWorkerClosed, nil
	}
	return ReasonClientClosed, nil
}

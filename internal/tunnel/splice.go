package tunnel

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// SpliceOptions configures websocket keepalive.
type SpliceOptions struct {
	PingInterval, PingTimeout time.Duration
	OnProgress                func(up, down int64)
}

// SpliceResult reports forwarded byte counts and teardown reason.
type SpliceResult struct {
	Up, Down int64
	Reason   string
	Err      error
}

// Splice forwards websocket messages between client and worker.
func Splice(ctx context.Context, client, worker *websocket.Conn, opt SpliceOptions) SpliceResult {
	if opt.PingInterval <= 0 {
		opt.PingInterval = 30 * time.Second
	}
	if opt.PingTimeout <= 0 {
		opt.PingTimeout = 10 * time.Second
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type r struct {
		err error
		up  bool
	}
	ch := make(chan r, 2)
	var up, down int64
	// progMu serialises "add bytes + OnProgress" across the two directions so the
	// callback always observes monotonically non-decreasing totals (the registry's
	// update stores them as-is, so an out-of-order call would briefly regress them).
	var progMu sync.Mutex
	f := func(src, dst *websocket.Conn) {
		for {
			t, rd, e := src.Reader(ctx)
			if e != nil {
				ch <- r{e, src == client}
				return
			}
			if t != websocket.MessageBinary {
				io.Copy(io.Discard, rd)
				continue
			}
			buf, e := io.ReadAll(io.LimitReader(rd, ReadLimit))
			if e == nil {
				e = dst.Write(ctx, websocket.MessageBinary, buf)
				if e == nil { // only bytes actually delivered count
					progMu.Lock()
					if src == client {
						atomic.AddInt64(&up, int64(len(buf)))
					} else {
						atomic.AddInt64(&down, int64(len(buf)))
					}
					if opt.OnProgress != nil {
						opt.OnProgress(atomic.LoadInt64(&up), atomic.LoadInt64(&down))
					}
					progMu.Unlock()
				}
			}
			if e != nil {
				ch <- r{e, src == client}
				return
			}
		}
	}
	go f(client, worker)
	go f(worker, client)
	tick := time.NewTicker(opt.PingInterval)
	defer tick.Stop()
	closeBoth := func() {
		cancel()
		_ = client.Close(websocket.StatusNormalClosure, "closed")
		_ = worker.Close(websocket.StatusNormalClosure, "closed")
	}
	for {
		select {
		case x := <-ch:
			closeBoth()
			y := <-ch
			_ = y
			reason := "client_closed"
			if !x.up {
				reason = "worker_closed"
			}
			var err error
			if x.err != nil && x.err != io.EOF && x.err != net.ErrClosed && websocket.CloseStatus(x.err) != websocket.StatusNormalClosure {
				err = x.err
			}
			return SpliceResult{Up: atomic.LoadInt64(&up), Down: atomic.LoadInt64(&down), Reason: reason, Err: err}
		case <-tick.C:
			pc := context.Background()
			pingctx, cancelPing := context.WithTimeout(pc, opt.PingTimeout)
			e1 := client.Ping(pingctx)
			e2 := worker.Ping(pingctx)
			cancelPing()
			if e1 != nil || e2 != nil {
				closeBoth()
				<-ch
				<-ch
				return SpliceResult{Up: atomic.LoadInt64(&up), Down: atomic.LoadInt64(&down), Reason: "ping_timeout"}
			}
		case <-ctx.Done():
			closeBoth()
			<-ch
			<-ch
			return SpliceResult{Up: atomic.LoadInt64(&up), Down: atomic.LoadInt64(&down), Reason: "ctx_done"}
		}
	}
}

package worker

import (
	"context"
	"time"

	"github.com/inhere/gofer/internal/wsproto"
)

// startHeartbeat launches the worker-side ping sender for the current connection
// (P3 §5.1, symmetric with the hub). It sends ping{ts} every pingInterval so the
// hub's read loop stays fed and so the worker detects a half-open hub via its own
// read deadline (a dead hub stops answering). The goroutine stops when done is
// closed (the recv loop exited / reconnecting) or ctx is cancelled (worker
// shutdown). A write error is benign — the recv loop's read deadline is the
// authoritative disconnect detector.
func (cl *Client) startHeartbeat(ctx context.Context, done <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(cl.pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = cl.writeFrame(ctx, wsproto.TypePing, "", wsproto.Ping{TS: time.Now().Unix()})
			}
		}
	}()
}

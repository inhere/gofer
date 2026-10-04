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
		// The first ping goes out at once so the hub learns the worker's messenger
		// and directory state right after registering, not one interval later.
		cl.sendPing(ctx)
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				cl.sendPing(ctx)
			}
		}
	}()
}

// sendPing writes one heartbeat ping carrying the worker state report (v16). A
// hub that predates the fields ignores them.
func (cl *Client) sendPing(ctx context.Context) {
	m, d := cl.stateReport()
	_ = cl.writeFrame(ctx, wsproto.TypePing, "", wsproto.Ping{TS: time.Now().Unix(), Messenger: m, Dirs: d})
}

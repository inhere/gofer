package worker

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/inhere/gofer/internal/config"
)

// Serve runs a built worker Client until SIGINT/SIGTERM, owning the signal/ctx
// start-stop orchestration (D-B4: moved out of the worker command, which now
// only loads config, builds the local Core + Client and calls Serve). It mirrors
// serve's graceful-shutdown style: a signal cancels the worker ctx, which makes
// Client.Run exit its reconnect/recv/heartbeat loops and close the connection
// (going-away); signal.Stop on return so the signal goroutine never leaks
// (ws-worker §5.6). wc is used only for the structured startup log.
func Serve(cl *Client, wc *config.WorkerConfig) error {
	// Graceful shutdown: SIGINT/SIGTERM cancels the worker ctx, which makes
	// Run exit its reconnect/recv/heartbeat loops and close the connection
	// (going-away). signal.Stop on return so the signal goroutine never leaks
	// (mirrors serve's startReloadLoop, §5.6).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)
	go func() {
		select {
		case <-ctx.Done():
		case <-sig:
			cancel()
		}
	}()

	slog.Info("worker starting", "worker_id", wc.WorkerID, "urls", wc.ServerLink.URLs,
		"labels", wc.Labels, "max_concurrent", wc.MaxConcurrent)
	return cl.Run(ctx)
}

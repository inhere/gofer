package worker

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/daemon"
)

// Serve runs a built worker Client until SIGINT/SIGTERM, owning the signal/ctx
// start-stop orchestration (D-B4: moved out of the worker command, which now
// only loads config, builds the local Core + Client and calls Serve). It mirrors
// serve's graceful-shutdown style: a signal cancels the worker ctx, which makes
// Client.Run exit its reconnect/recv/heartbeat loops and close the connection
// (going-away); signal.Stop on return so the signal goroutine never leaks
// (ws-worker §5.6). wc supplies the process-level settings the client needs from the
// worker's own config (the structured startup log, the XFER-01 transfer deadline).
func Serve(cl *Client, wc *config.WorkerConfig) error {
	// An upgraded worker never ran `gofer init worker`, so the default workspace
	// may not exist yet (the Runners page flagged it). Failure only warns.
	if dir, created, err := config.EnsureWorkspaceDir(); err != nil {
		slog.Warn("workspace.ensure_failed", "event", "workspace.ensure_failed", "component", "worker", "dir", dir, "err", err)
	} else if created {
		slog.Info("workspace.created", "event", "workspace.created", "component", "worker", "dir", dir)
	}

	// XFER-01: the single-transfer deadline comes from the worker's OWN config
	// (worker.xfer_timeout_sec, default 10m) — the hub cannot know this machine's
	// budget. Resolved here, before Run accepts any frame; like the rest of the
	// process-level wiring it is not re-read on reload (a changed timeout therefore
	// needs a worker restart, which the docs state alongside the key).
	cl.SetXferTimeout(wc.EffectiveXferTimeout())

	// Graceful shutdown: SIGINT/SIGTERM cancels the worker ctx, which makes
	// Run exit its reconnect/recv/heartbeat loops and close the connection
	// (going-away). signal.Stop on return so the signal goroutine never leaks
	// (mirrors serve's startReloadLoop, §5.6).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// An upgrade handover ends this process through the same graceful path a signal does.
	cl.SetExit(cancel)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)
	// Windows: a detached worker has no console and therefore no Ctrl+C, so this
	// makes it reachable by `worker stop` (the named stop event). No-op on unix.
	daemon.NotifyStop(sig)
	go func() {
		select {
		case <-ctx.Done():
		case <-sig:
			cancel()
		}
	}()

	// Local config reload (SIGHUP on unix; no-op on Windows, which has no SIGHUP and
	// reloads through the hub instead). It only ENQUEUES onto the same serial reload
	// executor a hub-issued reload uses — one reload path, one ordering. A SIGHUP
	// arriving while the queue is full is dropped (the reload already queued ahead of
	// it will pick the same file up); there is no requester to answer.
	hup := make(chan os.Signal, 1)
	notifyReloadSignal(hup)
	defer signal.Stop(hup)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				if !cl.enqueueReload(reloadReq{reason: "sighup"}) {
					slog.Warn("worker reload queue full, dropping SIGHUP", "worker_id", wc.WorkerID)
				}
			}
		}
	}()

	slog.Info("worker.starting", "event", "worker.starting", "component", "worker", "worker_id", wc.WorkerID, "urls", wc.ServerLink.URLs,
		"labels", wc.Labels, "max_concurrent", wc.MaxConcurrent)
	err := cl.Run(ctx)
	// Like serve: stop the agent process trees of resident ACP sessions this worker
	// runs. A worker restart ends them anyway (the hub fails the job with a resume
	// hint), and without this every restart left the agent orphaned.
	if sd, ok := cl.jobs.(interface {
		ShutdownResidentSessions(context.Context) error
	}); ok {
		sctx, scancel := context.WithTimeout(context.Background(), 20*time.Second)
		if serr := sd.ShutdownResidentSessions(sctx); serr != nil {
			slog.Error("worker.session_shutdown_failed", "worker_id", wc.WorkerID, "error", serr)
		}
		scancel()
	}
	slog.Info("worker.shutdown", "event", "worker.shutdown", "component", "worker", "worker_id", wc.WorkerID)
	return err
}

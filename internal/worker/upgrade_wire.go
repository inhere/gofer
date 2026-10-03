package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/inhere/gofer/internal/wsproto"
)

// RolledBackError reports an upgrade that installed the new binary, started the new
// process and then restored the old binary because the new process never came up.
type RolledBackError struct{ Err error }

func (e *RolledBackError) Error() string { return e.Err.Error() }
func (e *RolledBackError) Unwrap() error { return e.Err }

// upgradeReportAttempts / upgradeReportEvery bound how long a rollback or give-up
// report keeps trying to reach the hub: right after a failed handover the connection
// may be mid-reconnect.
const (
	upgradeReportAttempts = 30
	upgradeReportEvery    = time.Second
)

// handleUpgrade runs one upgrade request (in its own goroutine, never on the read
// loop). The first frame it writes is the answer the hub's caller waits on: accepted
// once the candidate binary is verified, or the failure. A later failure (drain
// timeout, rollback) is reported as a second frame; a successful handover reports
// nothing — the new process's registration is the hub's success signal.
func (cl *Client) handleUpgrade(ctx context.Context, req wsproto.Upgrade) {
	reply := func(r wsproto.UpgradeResult) error {
		r.RequestID = req.RequestID
		return cl.writeFrame(ctx, wsproto.TypeUpgradeResult, "", r)
	}
	if cl.upgradeFn == nil {
		_ = reply(wsproto.UpgradeResult{Phase: wsproto.UpgradePhaseFailed, Error: "worker: binary upgrade is not wired"})
		return
	}
	if !cl.upgrading.CompareAndSwap(false, true) {
		_ = reply(wsproto.UpgradeResult{Phase: wsproto.UpgradePhaseFailed, Error: "worker: another upgrade is already in progress"})
		return
	}
	defer cl.upgrading.Store(false)

	acceptedSent := false
	err := cl.upgradeFn(ctx, req, func(version string) {
		acceptedSent = true
		if werr := reply(wsproto.UpgradeResult{OK: true, Phase: wsproto.UpgradePhaseAccepted, Version: version}); werr != nil {
			slog.Warn("worker.upgrade_accept_unsent", "event", "worker.upgrade_accept_unsent", "component", "worker",
				"worker_id", cl.workerID, "upgrade_id", req.RequestID, "error", werr)
		}
	})
	if err == nil {
		return
	}
	res := wsproto.UpgradeResult{Phase: wsproto.UpgradePhaseFailed, Error: err.Error()}
	var rb *RolledBackError
	if errors.As(err, &rb) {
		res.Phase = wsproto.UpgradePhaseRolledBack
	}
	slog.Warn("worker.upgrade_failed", "event", "worker.upgrade_failed", "component", "worker",
		"worker_id", cl.workerID, "upgrade_id", req.RequestID, "phase", res.Phase, "error", err)
	if !acceptedSent {
		_ = reply(res)
		return
	}
	for i := 0; i < upgradeReportAttempts; i++ {
		if reply(res) == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(upgradeReportEvery):
		}
	}
}

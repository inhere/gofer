package core

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/wshub"
	"github.com/inhere/gofer/internal/wsproto"
	"github.com/inhere/gofer/internal/xfer"
)

// Xfer returns the XFER-01 transfer manager. It is always non-nil after Build: the
// transfer surface (HTTP routes, the CLI) is mounted unconditionally, and a transfer
// that cannot reach a worker fails with a reason instead of the feature being absent.
func (c *Core) Xfer() *xfer.Manager { return c.xferMgr }

// buildXferManager assembles the transfer manager over the metadata store: the staging
// area, the journal and the two executors (`local` = this process, anything else = a
// worker over the hub).
//
// It is the ONLY place that may wire this: internal/wshub and internal/xfer are on
// opposite sides of the layering rule (G022: the hub depends on wsproto alone), so the
// adapter below — the one type that knows both vocabularies — has to live here.
func buildXferManager(c *Core, cfg *config.Config, hub *wshub.Hub, st *jobstore.Store) (*xfer.Manager, error) {
	mgr, err := xfer.NewManager(xfer.Options{
		Root:   xferRoot(cfg),
		Limits: xferLimits(cfg.Server.Xfer),
		Repo:   st,
		// The job service is the event sink (XFER-01 X2): a transfer's xfer.put|xfer.get
		// lands in the shared event log AND runs through the notification pipeline the
		// job events do (webhook enqueue by the transfer's project) — a transfer is not
		// a job, but it is the same audit/notify surface. The store alone cannot do the
		// second half, which is why the assembly hands over the service.
		Events: c.Jobs,
	})
	if err != nil {
		return nil, err
	}
	mgr.SetRunner(&xfer.Router{
		Local: &xfer.LocalRunner{
			// A method value, not a snapshot: the server hot-reloads its config, so the
			// runner must resolve a project root against the generation in force when the
			// transfer runs. c.Config() is safe here for the same reason corePolicySource
			// is — the runner is only invoked long after Build seeded the snapshot.
			Config: c.Config,
			Store:  mgr.Store(),
			// CommitGet is what makes a local get usable: it finalizes the staged payload
			// and records its digest, and its failure must fail the transfer (Run returns
			// the error) rather than report a get as done with nothing to download.
			OnGetContent: mgr.CommitGet,
		},
		Sender: hubXferSender{hub: hub},
		// Timeout stays 0: the WORKER enforces its own budget (worker.xfer_timeout_sec),
		// and the server-side bound defaults to the same ten minutes — a bound shorter
		// than the far side's would only convert a slow-but-working transfer into a
		// spurious failure.
	})
	// A result whose waiter already gave up (the dispatch timed out, or the worker
	// reconnected mid-transfer) still settles the journal: OnWorkerResult settles a
	// record that is still dispatched and ignores a terminal one, so a late or
	// duplicated report can never reopen a finished transfer.
	hub.SetFileXferResultHandler(func(res wsproto.FileXferResult) {
		if err := mgr.OnWorkerResult(xfer.FileXferResult{
			XferID:     res.XferID,
			OK:         res.OK,
			Size:       res.Size,
			SHA256:     res.SHA256,
			Error:      res.Error,
			DurationMS: res.DurationMS,
		}); err != nil {
			slog.Warn("xfer.result_unmatched_failed", "event", "xfer.result_unmatched_failed", "component", "server",
				"xfer_id", res.XferID, "err", err)
		}
	})
	return mgr, nil
}

// xferRoot resolves where the staging area lives: storage.root when the operator
// configured a global store (the same directory the results/jobs use), else the config
// dir — the transfers are small, transient data and must survive a serve restart, so
// neither the cwd nor the OS temp dir is a good home for them. The temp dir is the last
// resort (ConfigDir failed), which only happens when the home directory cannot be
// resolved at all.
func xferRoot(cfg *config.Config) string {
	if root := cfg.Storage.Root; root != "" {
		return root
	}
	dir, err := config.ConfigDir()
	if err != nil {
		slog.Warn("xfer.root_config_dir_failed", "event", "xfer.root_config_dir_failed", "component", "server",
			"err", err, "fallback", os.TempDir())
		return os.TempDir()
	}
	return dir
}

// xferLimits resolves server.xfer over the package defaults (256MB / 24h / 1GB
// collect): a zero or unset field keeps the documented default rather than meaning
// "no transfers".
func xferLimits(xc config.XferConfig) xfer.Limits {
	lim := xfer.DefaultLimits()
	if xc.MaxBytes > 0 {
		lim.MaxBytes = xc.MaxBytes
	}
	if xc.TTLSec > 0 {
		lim.TTL = time.Duration(xc.TTLSec) * time.Second
	}
	if xc.CollectMaxBytes > 0 {
		lim.CollectMaxBytes = xc.CollectMaxBytes
	}
	return lim
}

// hubXferSender adapts the hub's wire-level transfer call onto xfer.WorkerSender. It
// exists so that neither side has to import the other: the hub speaks wsproto, the
// transfer manager speaks its own request/result types, and this is the single place
// that knows both (G022).
type hubXferSender struct {
	hub *wshub.Hub
}

func (s hubXferSender) SendFileXfer(ctx context.Context, workerID string, req xfer.FileXferRequest) (xfer.FileXferResult, error) {
	res, err := s.hub.SendFileXfer(ctx, workerID, wsproto.FileXfer{
		XferID:     req.XferID,
		Op:         req.Op,
		ProjectKey: req.ProjectKey,
		Path:       req.Path,
		Size:       req.Size,
		SHA256:     req.SHA256,
		Force:      req.Force,
		URLPath:    req.URLPath,
	})
	if err != nil {
		return xfer.FileXferResult{}, err
	}
	return xfer.FileXferResult{
		XferID:     res.XferID,
		OK:         res.OK,
		Size:       res.Size,
		SHA256:     res.SHA256,
		Error:      res.Error,
		DurationMS: res.DurationMS,
	}, nil
}

package serve

import (
	"path/filepath"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/notify"
	"github.com/inhere/gofer/internal/serve/healthcheck"
)

// sweepProbePeriods is how many schedule-sweep periods may pass without a COMPLETED
// pass before the `sweep` self-check item fails (design 2026-11-14 §5: three periods).
const sweepProbePeriods = 3

// startHealthCheck wires and starts the server self-check loop (design 2026-11-14 §5,
// plan R10/R11) and hands it to httpapi for /v1/health/checks, /health and /v1/stats.
//
// The four probes come from the package that owns the capability — the store, the
// platform free-space read, the schedule sweeper's stamp and the notification
// dispatcher's per-channel streaks — so this file only assembles them. The returned
// channel closes when the loop has stopped, which is what serve waits for before the
// store closes (G051).
func startHealthCheck(c *gcli.Command, cr *core.Core, sweeps *sweepClock, srv *httpapi.Server, stop <-chan struct{}) <-chan struct{} {
	cfg := cr.Config()
	threshold := cfg.EffectiveHealthFailThreshold()
	if sweeps == nil {
		sweeps = &sweepClock{}
	}
	// The delivery executor counts its per-channel streaks here; the notification probe
	// below only reads them.
	streaks := notify.NewChannelStreaks()
	cr.Jobs.SetDeliveryStreaks(streaks)
	checks := []healthcheck.Check{
		{Name: healthcheck.CheckStorage, Probe: healthcheck.StorageProbe(cr.Store, time.Now)},
		{Name: healthcheck.CheckDisk, Probe: healthcheck.DiskProbe(dataDir(cfg))},
		{Name: healthcheck.CheckSweep, Probe: healthcheck.SweepProbe(sweeps.LastSweepAt, sweepProbePeriods*scheduleSweepCadence(cfg), time.Now)},
		{Name: healthcheck.CheckNotification, Probe: healthcheck.NotifyProbe(streaks.Fails, threshold)},
	}
	runner := healthcheck.New(threshold, checks...)
	srv.SetHealthChecks(runner)
	c.Printf("gofer: health self-check enabled (interval=%s, fail_threshold=%d)\n", healthcheck.DefaultInterval, threshold)

	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.Loop(healthcheck.DefaultInterval, stop)
	}()
	return done
}

// dataDir is the volume the free-space self-check watches: the metadata database's
// directory, i.e. the disk gofer keeps its database (and, unless configured otherwise,
// the job logs) on. The config directory is the fallback when the db path cannot be
// resolved — the same place a default install puts the database.
func dataDir(cfg *config.Config) string {
	if cfg != nil {
		if dbPath, err := cfg.ResolveDBPathChecked(); err == nil && dbPath != "" {
			return filepath.Dir(dbPath)
		}
	}
	dir, _ := config.ConfigDir()
	return dir
}

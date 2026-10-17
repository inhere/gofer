package serve

import (
	"errors"
	"log/slog"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/workerenroll"
)

// enrollConfigWriter is the slice of core.Core the registrar needs.
type enrollConfigWriter interface {
	Update(mut func(*config.Config) error) error
}

// enrollHub is the slice of the worker hub the registrar needs.
type enrollHub interface {
	DisconnectWorker(id string)
}

// enrollRegistrar implements workerenroll.Registrar over the core's serial config
// transaction and the hub.
type enrollRegistrar struct {
	core enrollConfigWriter
	hub  enrollHub
	jobs *job.Service // optional: records the system-side config.updated event
}

func (r enrollRegistrar) Register(id string, labels, projects []string, token string) error {
	return r.core.Update(func(cfg *config.Config) error {
		return config.RegisterWorkerInto(cfg, id, labels, projects, token)
	})
}

// Remove revokes id only while its configured token still has fingerprint fp; a
// missing worker or a different token is "nothing removed", not an error.
func (r enrollRegistrar) Remove(id, fp string) (bool, error) {
	removed := false
	err := r.core.Update(func(cfg *config.Config) error {
		ok, err := config.RemoveWorkerFrom(cfg, id, fp)
		removed = ok
		return err
	})
	if errors.Is(err, config.ErrWorkerNotFound) || errors.Is(err, config.ErrWorkerFingerprintMismatch) {
		return false, nil
	}
	if err == nil && removed && r.jobs != nil {
		r.jobs.RecordScopedEvent(job.ConfigEventScope, job.EventConfigUpdated, "", map[string]any{
			"section": "workers", "key": id, "by": "system", "fields": []string{"worker_id", "projects"},
		})
	}
	return removed, err
}

func (r enrollRegistrar) Disconnect(id string) {
	if r.hub != nil {
		r.hub.DisconnectWorker(id)
	}
}

// enrollAudit writes enrollment events into audit_events (target = enrollment id).
type enrollAudit struct{ store *jobstore.Store }

func (a enrollAudit) Record(kind, enrollID, actor string) {
	if _, err := a.store.AppendAuditEvent(kind, enrollID, actor, "{}"); err != nil {
		slog.Warn("workerenroll.audit", "event", "workerenroll.audit", "kind", kind, "err", err)
	}
}

// enrollConfig implements workerenroll.ConfigSource over the live config snapshot,
// read on every call so a hot reload (e.g. `enabled`) applies at once.
type enrollConfig struct{ current func() *config.Config }

func (c enrollConfig) Enrollment() workerenroll.Settings {
	w := c.current().Server.WorkerEnrollment
	return workerenroll.Settings{Enabled: w.Enabled, DecideSec: w.EffectiveDecideSec(),
		CollectSec: w.EffectiveCollectSec(), MaxPending: w.EffectiveMaxPending()}
}

func (c enrollConfig) WorkerToken(id string) (string, bool) {
	w, ok := c.current().Server.Workers[id]
	if !ok {
		return "", false
	}
	// Enrollment only ever writes literal tokens; a token_env entry has none here.
	return w.Token, w.Token != ""
}

// enrollment bundles what serve starts and stops for the feature.
type enrollment struct {
	svc       *workerenroll.Service
	sweeper   *workerenroll.Sweeper
	collector *workerenroll.Collector
}

// newEnrollment builds the Service, reconciles leftovers once, and starts the sweeper
// and the hub first-connect collector. Close it before the store closes (G051).
func newEnrollment(store *jobstore.Store, reg enrollRegistrar, current func() *config.Config) (*enrollment, error) {
	lim := workerenroll.NewSourceLimiter(func() int {
		return current().Server.WorkerEnrollment.EffectivePerSourcePerMin()
	}, nil)
	svc, err := workerenroll.New(workerenroll.Options{
		Store: store, Registrar: reg, Audit: enrollAudit{store: store},
		Config: enrollConfig{current: current}, Limiter: lim, Blocker: lim,
	})
	if err != nil {
		return nil, err
	}
	if res, err := svc.Reconcile(); err != nil {
		slog.Warn("workerenroll.reconcile", "event", "workerenroll.reconcile", "err", err)
	} else if res.Expired+res.Reconciled+res.Pruned > 0 {
		slog.Info("workerenroll.reconcile", "event", "workerenroll.reconcile",
			"expired", res.Expired, "reconciled", res.Reconciled, "pruned", res.Pruned)
	}
	e := &enrollment{svc: svc, collector: workerenroll.NewCollector(svc, 0)}
	e.sweeper = workerenroll.NewSweeper(svc, 0)
	e.sweeper.Start()
	return e, nil
}

// Close stops the sweeper and the collector and waits for them.
func (e *enrollment) Close() {
	if e == nil {
		return
	}
	e.sweeper.Close()
	e.collector.Close()
}

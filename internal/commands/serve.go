package commands

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/job"
)

// serveExitErr is the process exit code used when serve fails to start or run.
// gcli derives the process exit code from errorx.ErrorCoder errors, so serve
// returns coded errors to guarantee a non-zero exit (plan §11: refuse to start
// without a token).
const serveExitErr = 2

// serveOpts holds the serve command flags.
var serveOpts = struct {
	config        string
	addr          string
	token         string
	allowEmptyTok bool
	noWeb         bool
}{}

// NewServeCmd builds the `serve` command: load config, wire the job service and
// the httpapi server, then start the HTTP control plane (plan §9-P5).
func NewServeCmd() *gcli.Command {
	return &gcli.Command{
		Name: "serve",
		Desc: "Start the gofer HTTP server",
		Config: func(c *gcli.Command) {
			c.StrOpt(&serveOpts.config, "config", "c", "", "path to the bridge config file")
			c.StrOpt(&serveOpts.addr, "addr", "", "", "HTTP listen address (default from config / 0.0.0.0:8765)")
			c.StrOpt(&serveOpts.token, "token", "", "", "bearer token override (prefer config/env)")
			c.BoolOpt(&serveOpts.allowEmptyTok, "allow-empty-token", "", false, "allow starting without a token")
			c.BoolOpt(&serveOpts.noWeb, "no-web", "", false, "disable the web console (static UI)")
		},
		Func: runServe,
	}
}

func runServe(c *gcli.Command, _ []string) error {
	cfg, _, err := config.Load(serveOpts.config)
	if err != nil {
		return errorx.Failf(serveExitErr, "%v", err)
	}

	// --addr overrides server.addr; config.Load already defaulted addr to
	// 0.0.0.0:8765 when unset (plan §11).
	addr := cfg.Server.Addr
	if serveOpts.addr != "" {
		addr = serveOpts.addr
	}

	// --allow-empty-token (flag) OR allow_empty_token (config) opts out of auth.
	allowEmpty := cfg.Server.AllowEmptyToken || serveOpts.allowEmptyTok

	// Web console is on by default (config web_enabled, default true); --no-web
	// force-disables it. Fold the final decision back onto cfg so httpapi.New
	// reads it via serverCfg.IsWebEnabled().
	webEnabled := cfg.Server.IsWebEnabled() && !serveOpts.noWeb
	cfg.Server.WebEnabled = &webEnabled

	token := resolveToken(&cfg.Server, serveOpts.token)
	if token == "" && !allowEmpty {
		// Refuse to start an unauthenticated server unless explicitly allowed
		// (plan §11). A coded error makes gcli exit non-zero.
		return errorx.Failf(serveExitErr, "refusing to start without a token: set server.token / server.token_env / --token, or pass --allow-empty-token")
	}

	core, err := buildCore(cfg)
	if err != nil {
		return errorx.Failf(serveExitErr, "%v", err)
	}
	// Graceful shutdown: close the metadata store (WAL checkpoint) when serve
	// returns (design §14).
	defer func() { _ = core.Close() }()

	// Periodic retention prune (design §13 SP5). Only runs when storage.retention
	// is configured; stop is closed when serve returns so the goroutine exits
	// cleanly with the rest of the process.
	stopPrune := make(chan struct{})
	defer close(stopPrune)
	startPruneLoop(c, core.Jobs, cfg.Storage.Retention, stopPrune)

	// E14 webhook delivery sweeper (design §5.6). Only started when notification is
	// configured (at least one webhook); with no config it does nothing (regression
	// same as before). stop is closed when serve returns so the goroutine exits.
	stopDelivery := make(chan struct{})
	defer close(stopDelivery)
	startDeliveryLoop(c, core.Jobs, cfg.Server.Notification, stopDelivery)

	// Config hot-reload (C3): SIGHUP re-loads the config from the serve path and
	// atomically swaps it into the registries + job service (no restart, no
	// effect on in-flight jobs). The goroutine stops cleanly when serve returns.
	stopReload := make(chan struct{})
	defer close(stopReload)
	startReloadLoop(c, core, serveOpts.config, stopReload)

	// ws-worker hub graceful shutdown (WP3 §5.6): when serve returns, stopHub
	// closes so the hub gracefully closes every live worker connection (going-away),
	// unblocking the per-connection read loops and stopping the heartbeat goroutines
	// — no goroutine/fd leak. Mirrors the prune/reload stop-channel pattern.
	stopHub := make(chan struct{})
	defer close(stopHub)
	core.Hub.SetStop(stopHub)

	// C6/P4 peer-http active health probe: build a prober over the configured
	// peer-http runners (nil when none) and start its periodic loop. The loop
	// stops cleanly when serve returns (stopProbe). The prober's cache feeds the
	// /v1/runners endpoint; a nil prober renders peer-http rows `unknown`.
	prober := newPeerProber(cfg, cfg.Server.RunnerProbe.ProbeTimeout())
	stopProbe := make(chan struct{})
	defer close(stopProbe)
	startProbeLoop(c, prober, cfg.Server.RunnerProbe.ProbeInterval(), stopProbe)

	// C6/P4 worker observability: adapt the hub to httpapi's workerRegistry so the
	// /v1/runners endpoint reports each worker runner's connection / heartbeat /
	// in-flight / labels (D2 narrow interface). httpapi needs a typed-nil-safe
	// value: only wire the adapter when the hub is present.
	var workers = hubWorkerRegistry{hub: core.Hub}

	srv := httpapi.New(&cfg.Server, token, allowEmpty, core.Jobs, core.Projects, core.Agents, core.Hub, cfg.Runners, proberOrNil(prober), workers)

	if token == "" {
		c.Printf("gofer: starting WITHOUT auth (allow_empty_token) on %s\n", addr)
	} else {
		c.Printf("gofer: starting on %s (token auth enabled)\n", addr)
	}
	// Run blocks until the server stops (or fails to bind). The token is never
	// printed (plan §11).
	if err := srv.Run(addr); err != nil {
		return errorx.Failf(serveExitErr, "%v", err)
	}
	return nil
}

// startPruneLoop launches the periodic retention prune goroutine when retention
// is configured (storage.retention has MaxAgeDays>0 or MaxCount>0). It prunes
// once immediately, then on every tick of the configured interval (default 60m).
// The goroutine exits when stop is closed (serve shutdown). With no retention
// configured it does nothing (zero behaviour change).
//
// Reload scope (C3): the prune-loop ENABLE gate and the tick INTERVAL are frozen
// here at serve startup and are NOT re-read on SIGHUP. The retention THRESHOLDS
// (MaxAgeDays/MaxCount) DO take effect on reload because jobs.Prune reads the
// atomic cfg snapshot fresh each tick. Enabling retention from a disabled state,
// or changing the interval, needs a process restart.
func startPruneLoop(c *gcli.Command, jobs *job.Service, ret config.RetentionConfig, stop <-chan struct{}) {
	if !ret.Enabled() {
		return
	}
	interval := ret.PruneInterval()
	c.Printf("gofer: retention prune enabled (interval=%s, max_age_days=%d, max_count=%d)\n",
		interval, ret.MaxAgeDays, ret.MaxCount)

	go func() {
		prune := func() {
			if n, err := jobs.Prune(); err != nil {
				c.Errorf("gofer: prune failed: %v\n", err)
			} else if n > 0 {
				c.Printf("gofer: pruned %d terminal job(s)\n", n)
			}
		}
		prune() // run once at startup so a backlog is trimmed without waiting a full interval
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				prune()
			}
		}
	}()
}

// deliveryInterval is the E14 webhook delivery sweep cadence. A short tick keeps
// due deliveries (initially next_retry_at=now) prompt without busy-waiting; the
// backoff table spaces retries far wider than this.
const deliveryInterval = 15 * time.Second

// startDeliveryLoop launches the E14 webhook delivery sweeper goroutine when
// notification is configured (at least one webhook). It mirrors startPruneLoop /
// startProbeLoop: sweep once immediately (so a freshly-enqueued delivery is not
// delayed a whole interval), then on every tick. The goroutine exits when stop is
// closed (serve shutdown); an in-flight sweep is cancelled via a ctx tied to stop
// so a POST does not outlive shutdown. With no notification config (nil/empty
// webhooks) it does nothing — zero behaviour change.
//
// Reload scope (C3): like prune/probe, the ENABLE gate and the tick interval are
// frozen at serve startup. The webhook targets / retry cap DO take effect on
// reload because DeliverDue reads the atomic cfg snapshot fresh each sweep.
// Enabling notification from a disabled state needs a process restart.
func startDeliveryLoop(c *gcli.Command, jobs *job.Service, nconf *config.NotificationConfig, stop <-chan struct{}) {
	if nconf == nil || len(nconf.Webhooks) == 0 {
		return
	}
	c.Printf("gofer: webhook delivery enabled (interval=%s, webhooks=%d)\n", deliveryInterval, len(nconf.Webhooks))

	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-stop
			cancel()
		}()
		defer cancel()

		jobs.DeliverDue(ctx) // sweep once at startup so an enqueued delivery is prompt
		ticker := time.NewTicker(deliveryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				jobs.DeliverDue(ctx)
			}
		}
	}()
}

// proberOrNil returns the prober as an httpapi.runnerProber, or an UNTYPED nil
// when p is nil. Passing a typed nil (*peerProber)(nil) into the interface
// parameter would make httpapi's `s.prober == nil` check false (a non-nil
// interface wrapping a nil pointer), so the handler would call Snapshot on a nil
// receiver. Returning the plain interface value keeps the nil-safe contract.
func proberOrNil(p *peerProber) interface{ Snapshot() []httpapi.ProbeResult } {
	if p == nil {
		return nil
	}
	return p
}

// startProbeLoop launches the peer-http health-probe goroutine (C6/P4). It
// mirrors startPruneLoop: when there are peer-http targets it probes once
// immediately (so the first interval is not all-unknown), then on every tick of
// interval. The goroutine exits when stop is closed (serve shutdown) — no leak.
// With no peer-http runners (prober==nil) it does nothing (zero behaviour change).
//
// Probe targets are frozen at serve startup and NOT re-read on SIGHUP (runner
// instances are not rebuilt on reload either — overview §9.1 C3 / P4 §3.2):
// adding or removing a peer needs a restart.
func startProbeLoop(c *gcli.Command, prober *peerProber, interval time.Duration, stop <-chan struct{}) {
	if prober == nil {
		return
	}
	c.Printf("gofer: peer-http health probe enabled (interval=%s, targets=%d)\n", interval, len(prober.targets))

	go func() {
		// Each probe round runs under a context cancelled when stop closes so an
		// in-flight probe does not outlive serve shutdown.
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-stop
			cancel()
		}()
		defer cancel()

		prober.probeOnce(ctx) // probe once at startup so the cache is not all-unknown
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				prober.probeOnce(ctx)
			}
		}
	}()
}

// startReloadLoop launches the SIGHUP config hot-reload goroutine (C3). On each
// SIGHUP it calls core.Reload(path) to re-load the config and atomically swap it
// into the registries + job service. A reload that fails to load/validate keeps
// the previous config (Core.Reload swaps nothing on error) and only logs.
//
// The goroutine shuts down cleanly when stop is closed (serve shutdown): it
// stops signal delivery (signal.Stop) so no further SIGHUP is queued, then
// returns. No goroutine leak. Mirrors startPruneLoop's shutdown style.
func startReloadLoop(c *gcli.Command, core *Core, path string, stop <-chan struct{}) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGHUP)
	go func() {
		defer signal.Stop(sig)
		for {
			select {
			case <-stop:
				return
			case <-sig:
				if err := core.Reload(path); err != nil {
					c.Errorf("gofer: reload failed, keep old config: %v\n", err)
				} else {
					c.Printf("gofer: config reloaded\n")
				}
			}
		}
	}()
}

// resolveToken computes the effective bearer token (plan §7):
//   - start from the static server.token;
//   - server.token_env (when set and the env var is non-empty) takes priority;
//   - the --token flag is a temporary override and wins when provided.
func resolveToken(sc *config.ServerConfig, flagToken string) string {
	token := sc.Token
	if sc.TokenEnv != "" {
		if v := os.Getenv(sc.TokenEnv); v != "" {
			token = v
		}
	}
	if flagToken != "" {
		token = flagToken
	}
	return token
}

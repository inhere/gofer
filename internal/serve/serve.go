// Package serve holds the serve process orchestration (BP2): it builds the
// runtime Core, starts the periodic sweepers / health probe / SIGHUP reload
// loops, wires the httpapi server and blocks until the server stops. The
// commands layer keeps only flag binding + a thin call into serve.Start, so the
// process编排 lives below the entry layer (D-B2/D-B3: serve depends on
// core/job/runner/httpapi/metrics/config, and is never imported back by them).
package serve

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/metrics"
	"github.com/inhere/gofer/internal/presence"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/supervisor"
)

// ExitErr is the process exit code used when serve fails to start or run. gcli
// derives the process exit code from errorx.ErrorCoder errors, so serve returns
// coded errors to guarantee a non-zero exit (plan §11: refuse to start without a
// token).
const ExitErr = 2

// Opts collects the serve flags + resolved config paths the commands layer
// passes into Start. The config itself is loaded by the caller (so it can print
// the discovered path and apply overlays before assembling Core); CfgPath is the
// loaded config file (empty = defaults + discovery), ReloadPath is the path the
// SIGHUP reload loop re-loads from (config.InputCfgFile).
type Opts struct {
	Addr          string
	Token         string
	AllowEmptyTok bool
	NoWeb         bool
	CfgPath       string
	ReloadPath    string
}

// Start runs the serve process: assemble Core, start the sweeper / probe /
// reload loops, wire the httpapi server and block until it stops. It keeps the
// *gcli.Command so the c.Printf operator logging is byte-for-byte unchanged
// (zero behaviour change). cfg is the already-loaded config (the caller passes
// the discovered path via opts.CfgPath).
func Start(c *gcli.Command, cfg *config.Config, opts Opts) error {
	// Surface which config file serve actually loaded (or that it is running on
	// defaults + discovery), so an operator can tell at a glance whether the
	// expected config was picked up (P2/T2.1).
	if opts.CfgPath == "" {
		c.Printf("gofer: config: (none — defaults + discovery)\n")
	} else {
		c.Printf("gofer: config: %s\n", opts.CfgPath)
	}

	// --addr overrides server.addr; config.Load already defaulted addr to
	// 0.0.0.0:8765 when unset (plan §11).
	addr := cfg.Server.Addr
	if opts.Addr != "" {
		addr = opts.Addr
	}

	// --allow-empty-token (flag) OR allow_empty_token (config) opts out of auth.
	allowEmpty := cfg.Server.AllowEmptyToken || opts.AllowEmptyTok

	// Web console is on by default (config web_enabled, default true); --no-web
	// force-disables it. Fold the final decision back onto cfg so httpapi.New
	// reads it via serverCfg.IsWebEnabled().
	webEnabled := cfg.Server.IsWebEnabled() && !opts.NoWeb
	cfg.Server.WebEnabled = &webEnabled

	// Merge per-project thin overlays (.gofer.project.yaml) into the loaded
	// config before assembling Core, so admission/result-dir decisions observe
	// the overlay (D6). Overlay parse failures only warn — never block start
	// (T1.1 fail-safe semantics).
	for _, w := range config.ApplyProjectOverlays(cfg) {
		c.Printf("gofer: overlay warn: %s\n", w)
	}

	token := resolveToken(&cfg.Server, opts.Token)
	if token == "" && !allowEmpty {
		// Refuse to start an unauthenticated server unless explicitly allowed
		// (plan §11). A coded error makes gcli exit non-zero.
		return errorx.Failf(ExitErr, "refusing to start without a token: set server.token / server.token_env / --token, or pass --allow-empty-token")
	}

	cr, err := core.Build(cfg)
	if err != nil {
		return errorx.Failf(ExitErr, "%v", err)
	}
	// Graceful shutdown: close the metadata store (WAL checkpoint) when serve
	// returns (design §14).
	defer func() { _ = cr.Close() }()

	// Periodic retention prune (design §13 SP5). Only runs when storage.retention
	// is configured; stop is closed when serve returns so the goroutine exits
	// cleanly with the rest of the process.
	stopPrune := make(chan struct{})
	defer close(stopPrune)
	startPruneLoop(c, cr.Jobs, cfg.Storage.Retention, stopPrune)

	// E14 webhook delivery sweeper (design §5.6). Only started when notification is
	// configured (at least one webhook); with no config it does nothing (regression
	// same as before). stop is closed when serve returns so the goroutine exits.
	stopDelivery := make(chan struct{})
	defer close(stopDelivery)
	startDeliveryLoop(c, cr.Jobs, cfg.Server.Notification, stopDelivery)

	// 工作流推进 sweeper（job 链，crash 兜底）：扫 running 工作流，对当前 step 已终态但
	// 未被 finish 钩子推进的补推（幂等，与钩子叠加安全）。始终启动（工作流是核心能力，
	// 开销低：无 running 工作流时空转）；stop 在 serve 返回时关闭，goroutine 干净退出。
	stopWorkflow := make(chan struct{})
	defer close(stopWorkflow)
	startWorkflowLoop(c, cr.Workflow(), stopWorkflow)

	// E36 presence prune sweeper: GC offline driver-agent rows (last_seen past the
	// TTL window) + read/expired inbox messages. Always started (low cost: empty
	// registry = a cheap delete touching nothing). stop closes when serve returns.
	stopPresence := make(chan struct{})
	defer close(stopPresence)
	startPresencePruneLoop(c, cr.Presence, time.Duration(cfg.Presence.PruneIntervalSec)*time.Second, stopPresence)

	// E25 crash-recovery backstop (复审 #4): a process that died mid-job never ran
	// finish()'s reconciliation, so its pending interactions are stuck. Sweep them
	// to cancelled once at startup before the answerer begins.
	if n, rerr := cr.Jobs.ReconcileOrphanInteractions(); rerr != nil {
		c.Errorf("gofer: reconcile orphan interactions failed: %v\n", rerr)
	} else if n > 0 {
		c.Printf("gofer: reconciled %d orphan pending interaction(s) from terminal jobs\n", n)
	}

	// Crash-recovery backstop for jobs: a serve that died / restarted mid-flight (or a
	// worker that restarted and was superseded on the hub, §5.5) leaves jobs stuck
	// "running"/"queued" in the store with no live orchestration to ever finish them.
	// Fail them once at startup, before new work is accepted, so they don't hang forever.
	if n, rerr := cr.Jobs.ReconcileOrphanJobs(); rerr != nil {
		c.Errorf("gofer: reconcile orphan jobs failed: %v\n", rerr)
	} else if n > 0 {
		c.Printf("gofer: reconciled %d orphan non-terminal job(s) left by a prior serve/worker\n", n)
	}

	// E25 supervisor (layered answerer): only started when cfg.supervisor.enabled.
	// stop closes when serve returns so the poller exits with the process.
	stopSupervisor := make(chan struct{})
	defer close(stopSupervisor)
	startSupervisorLoop(c, cr, stopSupervisor)

	// Config hot-reload (C3): SIGHUP re-loads the config from the serve path and
	// atomically swaps it into the registries + job service (no restart, no
	// effect on in-flight jobs). The goroutine stops cleanly when serve returns.
	stopReload := make(chan struct{})
	defer close(stopReload)
	startReloadLoop(c, cr, opts.ReloadPath, stopReload)

	// ws-worker hub graceful shutdown (WP3 §5.6): when serve returns, stopHub
	// closes so the hub gracefully closes every live worker connection (going-away),
	// unblocking the per-connection read loops and stopping the heartbeat goroutines
	// — no goroutine/fd leak. Mirrors the prune/reload stop-channel pattern.
	stopHub := make(chan struct{})
	defer close(stopHub)
	cr.Hub.SetStop(stopHub)

	// C6/P4 peer-http active health probe: build a prober over the configured
	// peer-http runners (nil when none) and start its periodic loop. The loop
	// stops cleanly when serve returns (stopProbe). The prober's cache feeds the
	// /v1/runners endpoint; a nil prober renders peer-http rows `unknown`.
	prober := runner.NewPeerProber(cfg, cfg.Server.RunnerProbe.ProbeTimeout())
	stopProbe := make(chan struct{})
	defer close(stopProbe)
	startProbeLoop(c, prober, cfg.Server.RunnerProbe.ProbeInterval(), stopProbe)

	// C6/P4 worker observability: adapt the hub to httpapi's workerRegistry so the
	// /v1/runners endpoint reports each worker runner's connection / heartbeat /
	// in-flight / labels (D2 narrow interface). httpapi needs a typed-nil-safe
	// value: only wire the adapter when the hub is present.
	var workers = hubWorkerRegistry{hub: cr.Hub}

	srv := httpapi.New(&cfg.Server, token, allowEmpty, cr.Jobs, cr.Workflow(), cr.Projects, cr.Agents, cr.Hub, cfg.Runners, proberOrNil(prober), workers)

	// E16 Prometheus metrics: build the registry, inject the lifecycle-counter sink
	// into the job service, register the scrape-time GaugeFuncs (in-flight/queued/
	// running + workers connected/in-flight), then mount the /metrics endpoint +
	// the /v1 HTTP middleware on the server. The endpoint is gated by
	// metrics.enabled (default true) and an optional metrics.token (design §6.2).
	m := metrics.New()
	cr.Jobs.SetMetrics(m)
	m.RegisterRuntimeGauges(
		func() (int, int, int) {
			st := cr.Jobs.Stats()
			return st.InFlight, st.Queued, st.Running
		},
		func() (int, int) { return workerCounts(cr.Hub, cfg.Server.Workers) },
	)
	srv.SetMetrics(m, cfg.Server.Metrics.IsEnabled(), cfg.Server.Metrics.Token)

	// E36: mount the presence/mailbox endpoints (rebuilds router; after SetMetrics
	// so the metrics middleware is preserved). The prune sweeper is started below.
	srv.SetPresence(cr.Presence)

	if token == "" {
		c.Printf("gofer: starting WITHOUT auth (allow_empty_token) on %s\n", addr)
	} else {
		c.Printf("gofer: starting on %s (token auth enabled)\n", addr)
	}
	// SIGINT/SIGTERM trigger a graceful shutdown: the http.Server stops accepting
	// new connections and drains in-flight ones, RunCtx returns nil, then every
	// deferred cleanup above (store close, stop-channel closes for the sweeper /
	// reload / hub loops) runs — unlike a default signal kill, which skips defers.
	// `serve stop` / daemon teardown send SIGTERM here (c44). SIGHUP reload is a
	// separate loop and unaffected.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// RunCtx blocks until the server stops (signal-driven shutdown or bind
	// failure). The token is never printed (plan §11).
	if err := srv.RunCtx(ctx, addr); err != nil {
		return errorx.Failf(ExitErr, "%v", err)
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

// presencePruneInterval is the DEFAULT E36 presence/inbox prune cadence (used when
// config leaves presence.prune_interval_sec unset). The presence TTL is 90s
// (presence.DefaultTTL); pruning a bit slower than that keeps just-offline rows
// around briefly (so a flapping agent's id survives a missed heartbeat) while still
// bounding registry/inbox growth.
const presencePruneInterval = 60 * time.Second

// startPresencePruneLoop launches the E36 presence/mailbox prune goroutine: it
// prunes once at startup (clear a backlog from a previous run), then on every tick.
// It mirrors startPruneLoop's stop-channel lifecycle and exits when stop closes
// (serve shutdown). pres is always non-nil (core.Build wires it). interval <=0 falls
// back to presencePruneInterval (config presence.prune_interval_sec override).
func startPresencePruneLoop(c *gcli.Command, pres *presence.Service, interval time.Duration, stop <-chan struct{}) {
	if pres == nil {
		return
	}
	if interval <= 0 {
		interval = presencePruneInterval
	}
	go func() {
		prune := func() {
			if pN, mN, err := pres.Prune(); err != nil {
				c.Errorf("gofer: presence prune failed: %v\n", err)
			} else if pN > 0 || mN > 0 {
				c.Printf("gofer: pruned %d offline agent(s), %d message(s)\n", pN, mN)
			}
		}
		prune()
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

// startSupervisorLoop constructs the E25 layered answerer from cfg.supervisor and
// runs its poller goroutine when enabled (design §8.3-8.4). With no supervisor
// config (nil) or enabled=false it does nothing — pending interactions then wait
// for a human (conservative default). The poller exits when stop is closed (serve
// shutdown) via a ctx tied to stop, mirroring startDeliveryLoop.
//
// Reload scope (C3): like the other loops, the enable gate + policy are frozen at
// startup; toggling supervisor.enabled or editing the policy needs a restart.
func startSupervisorLoop(c *gcli.Command, cr *core.Core, stop <-chan struct{}) {
	sc := cr.Cfg.Supervisor
	if sc == nil || !sc.Enabled {
		return
	}
	pol := supervisor.Policy{
		Enabled:          true,
		Interval:         time.Duration(sc.IntervalSec) * time.Second,
		AutoAnswer:       sc.AutoAnswer,
		EscalateTo:       sc.EscalateTo,
		MaxRoundsPerJob:  sc.MaxRoundsPerJob,
		AllowPromptRegex: sc.AllowPromptRegex,
	}
	sup := supervisor.NewService(cr.Jobs, cr.Presence, pol)
	c.Printf("gofer: supervisor answerer enabled (auto_answer=%t, escalate_to=%q)\n", sc.AutoAnswer, sc.EscalateTo)
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-stop
			cancel()
		}()
		defer cancel()
		sup.Run(ctx)
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

// workflowInterval is the工作流推进 sweeper cadence (crash recovery). The finish
// hook推进 the common case promptly; this only catches workflows whose hook was
// lost (process crash mid-step), so a relaxed tick is fine.
const workflowInterval = 30 * time.Second

// startWorkflowLoop launches the工作流推进 sweeper goroutine (job 链, crash 兜底).
// It mirrors startDeliveryLoop: sweep once at startup (re-drive any workflow whose
// step finished while the server was down), then on every tick. The goroutine
// exits when stop is closed (serve shutdown); an in-flight sweep is cancelled via
// a ctx tied to stop. Unlike prune/delivery it is ALWAYS started — workflows are a
// core capability, not opt-in config — and is a cheap no-op when none are running.
func startWorkflowLoop(c *gcli.Command, eng *workflow.Engine, stop <-chan struct{}) {
	c.Printf("gofer: workflow advance sweeper enabled (interval=%s)\n", workflowInterval)

	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-stop
			cancel()
		}()
		defer cancel()

		eng.AdvanceRunning(ctx) // sweep once at startup (crash recovery)
		ticker := time.NewTicker(workflowInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				eng.AdvanceRunning(ctx)
			}
		}
	}()
}

// proberOrNil returns the prober as an httpapi.runnerProber, or an UNTYPED nil
// when p is nil. Passing a typed nil (*peerProber)(nil) into the interface
// parameter would make httpapi's `s.prober == nil` check false (a non-nil
// interface wrapping a nil pointer), so the handler would call Snapshot on a nil
// receiver. Returning the plain interface value keeps the nil-safe contract.
func proberOrNil(p *runner.PeerProber) interface{ Snapshot() []runner.ProbeResult } {
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
func startProbeLoop(c *gcli.Command, prober *runner.PeerProber, interval time.Duration, stop <-chan struct{}) {
	if prober == nil {
		return
	}
	c.Printf("gofer: peer-http health probe enabled (interval=%s, targets=%d)\n", interval, prober.TargetCount())

	go func() {
		// Each probe round runs under a context cancelled when stop closes so an
		// in-flight probe does not outlive serve shutdown.
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-stop
			cancel()
		}()
		defer cancel()

		prober.ProbeOnce(ctx) // probe once at startup so the cache is not all-unknown
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				prober.ProbeOnce(ctx)
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
func startReloadLoop(c *gcli.Command, cr *core.Core, path string, stop <-chan struct{}) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGHUP)
	go func() {
		defer signal.Stop(sig)
		for {
			select {
			case <-stop:
				return
			case <-sig:
				if err := cr.Reload(path); err != nil {
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

// Package serve holds the serve process orchestration (BP2): it builds the
// runtime Core, starts the periodic sweepers / health probe / SIGHUP reload
// loops, wires the httpapi server and blocks until the server stops. The
// commands layer keeps only flag binding + a thin call into serve.Start, so the
// process编排 lives below the entry layer (D-B2/D-B3: serve depends on
// core/job/runner/httpapi/metrics/config, and is never imported back by them).
package serve

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/castrec"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/metrics"
	"github.com/inhere/gofer/internal/presence"
	"github.com/inhere/gofer/internal/runner"
	ptyrunner "github.com/inhere/gofer/internal/runner/pty"
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
	WebDir        string
	CfgPath       string
	ReloadPath    string
	Build         buildinfo.Info
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
	cfg.Server.WebDir = opts.WebDir

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

	// WEB-03 P3 cast recorder: resolve the recording factory from storage.cast at
	// serve start so a missing/short encryption key fails fast BEFORE the server
	// binds (never a half-recorded session). Recording is opt-in — disabled ⇒ nil
	// recorder ⇒ recording off (G023 zero-regression). Injected into the server via
	// SetCastRecorder below (after New).
	castRecorder, err := buildCastRecorder(cfg)
	if err != nil {
		return errorx.Failf(ExitErr, "%v", err)
	}
	// castEnabled also gates the prune loop (cast TTL sweep rides the same loop even
	// with no job/workflow retention configured, D-P3-6); castTTLSec is for logging.
	castEnabled := cfg.Storage.Cast.Enabled
	var castTTLSec int64
	if castEnabled {
		castTTLSec = int64(cfg.Storage.Cast.RetentionTTLHours) * 3600
	}

	// Periodic retention prune (design §13 SP5). Runs when storage.retention is
	// configured OR cast recording is enabled (the cast TTL sweep rides this loop);
	// stop is closed when serve returns so the goroutine exits cleanly with the rest
	// of the process.
	stopPrune := make(chan struct{})
	defer close(stopPrune)
	startPruneLoop(c, cr.Jobs, cfg.Storage.Retention, castEnabled, castTTLSec, stopPrune)

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

	// AUTO-02 cron schedule sweeper: advance due schedules first, then submit the
	// embedded job request. The stop channel follows the other serve-owned loops.
	stopSchedule := make(chan struct{})
	defer close(stopSchedule)
	startScheduleLoop(c, cr, stopSchedule)

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

	// supWake connects the answerer (producer) to the reconciler (consumer) for
	// event-driven sup dispatch (y5wt): the answerer's escalate() signals it when a
	// pending interaction has no reachable sup; the reconciler spawns one on demand.
	// Buffered=1: a signal already queued means "a sup is wanted" — extra sends drop
	// (the reconciler's active-sup gate makes the consume idempotent anyway).
	supWake := make(chan struct{}, 1)

	// E25 supervisor (layered answerer): only started when cfg.supervisor.enabled.
	// stop closes when serve returns so the poller exits with the process.
	stopSupervisor := make(chan struct{})
	defer close(stopSupervisor)
	startSupervisorLoop(c, cr, supWake, stopSupervisor)

	// Event-driven supervisor reconciler (y5wt): spawn a sup ON DEMAND (wake signal or
	// a cheap periodic demand poll) instead of keeping one resident, gated so at most
	// desired_supervisors run at once. No-op unless supervisor.desired_supervisors>0.
	stopSupReconcile := make(chan struct{})
	defer close(stopSupReconcile)
	startSupReconcileLoop(c, cr, supWake, stopSupReconcile)

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
	srv.SetBuildInfo(opts.Build)

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
	srv.SetPtyRelay(cr.RelayNonces, cr.PtyRelays)
	// WEB-03 P3: inject the cast recorder (nil when recording is off) and the pty
	// session persistence seam (the metadata store satisfies PtySessionStore). Both
	// mount no routes, so they do not rebuild the router.
	srv.SetCastRecorder(castRecorder)
	srv.SetPtySessionStore(cr.Store)
	if pr, ok := cr.Runners[ptyrunner.Name].(*ptyrunner.PtyRunner); ok {
		pr.SetObserver(srv)
	}

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
func startPruneLoop(c *gcli.Command, jobs *job.Service, ret config.RetentionConfig, castEnabled bool, castTTLSec int64, stop <-chan struct{}) {
	// The loop runs when job/workflow retention is configured OR cast recording is
	// enabled — the cast TTL sweep (jobs.Prune) rides this same loop (D-P3-6). With
	// neither configured it does nothing (zero behaviour change, G023).
	if !ret.Enabled() && !castEnabled {
		return
	}
	interval := ret.PruneInterval()
	c.Printf("gofer: retention prune enabled (interval=%s, max_age_days=%d, max_count=%d, cast=%t, cast_ttl_hours=%d)\n",
		interval, ret.MaxAgeDays, ret.MaxCount, castEnabled, castTTLSec/3600)

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

// buildCastRecorder resolves the WEB-03 P3 cast recording factory from cfg at serve
// start (D-P3-5 / H1). Recording is opt-in (storage.cast.enabled); disabled ⇒ a nil
// recorder ⇒ recording off with zero behaviour change (G023). When encryption is on
// the key is read from its env var and validated (decodes to >= 32 bytes) so a
// missing/short key returns an error and serve fails fast before binding (rather
// than half-recording). The key is never logged (SR403). Plaintext recording needs
// no key (nil secret), so the recorder is built without touching the env.
func buildCastRecorder(cfg *config.Config) (*castrec.Recorder, error) {
	if !cfg.Storage.Cast.Enabled {
		return nil, nil
	}
	var key []byte
	if cfg.Storage.Cast.Encryption.Enabled {
		k, err := cfg.Storage.Cast.ResolveCastSecret()
		if err != nil {
			return nil, err
		}
		key = k
	}
	return castrec.New(cfg.Storage.Cast, key)
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
func startSupervisorLoop(c *gcli.Command, cr *core.Core, wake chan<- struct{}, stop <-chan struct{}) {
	sc := cr.Cfg.Supervisor
	if sc == nil || !sc.Enabled {
		return
	}
	pol := supervisor.Policy{
		Enabled:            true,
		Interval:           time.Duration(sc.IntervalSec) * time.Second,
		AutoAnswer:         sc.AutoAnswer,
		EscalateTo:         sc.EscalateTo,
		MaxRoundsPerJob:    sc.MaxRoundsPerJob,
		AllowPromptRegex:   sc.AllowPromptRegex,
		OwnerAnswerTimeout: time.Duration(sc.OwnerAnswerTimeoutSec) * time.Second,
	}
	sup := supervisor.NewService(cr.Jobs, cr.Presence, pol)
	// Event-driven dispatch (y5wt): escalate-found-no-sup wakes the reconciler. Send is
	// non-blocking — a full buffer already means "a sup is wanted", so dropping is correct.
	sup.SetWake(func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	})
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

// supReconcileInterval is the DEFAULT event-driven reconciler BACKSTOP poll cadence
// (used when supervisor.reconcile_interval_sec is unset/<=0). Dispatch is normally
// wake-driven (low latency); this periodic CountSupPendingDemand poll only covers a lost
// wake / a serve restart with pending work, so it is a cheap DB count, not a hot path.
// Kept at 60s (not lower) to bound over-spawn in the rare msg-pruned-while-pending edge —
// the wake already gives low latency, so the backstop trades a little staleness for safety.
const supReconcileInterval = 60 * time.Second

// defaultSupReconcilePrompt is the kickoff turn given to each reconciler-spawned sup
// job when supervisor.reconcile_prompt is empty. A cli-agent (codex) rejects an empty
// prompt (agent/adapter.go), so the reconciler must always supply one; the resident
// guardrails live in roles.supervisor.system_prompt. Mirrors the P4a runbook mission.
const defaultSupReconcilePrompt = "你是 supervisor。循环调用 gofer_poll_inbox(传 ack=false 只查看不消费) 获取 kind=escalation 的消息，" +
	"用 gofer_get_interactions 找到对应 interaction_id：通用、低危的问题用 gofer_answer_interaction 作答；" +
	"拿不准或高危的不要猜，用 gofer_punt_interaction 标记留给人处理(不要只是跳过)。连续2轮空箱即结束。non-interactive。"

// supReconcileJobTimeoutDefault is the per-sup-job timeout when
// supervisor.reconcile_job_timeout_sec is unset/<=0. Under event-driven dispatch a healthy
// sup drains the pending demand and EXITS early (seconds), so this is really a HUNG-sup cap:
// a sup that wedged (job running but agent not polling) is force-terminated within the cap,
// freeing the active-sup gate so the next demand re-spawns a fresh one. 1h (MaxTimeoutSec;
// submit clamps to the same cap) is a safe upper bound on a wedged sup's blast radius.
const supReconcileJobTimeoutDefault = 3600

// reconcileSupervisors is the pure event-driven dispatch decision (y5wt): spawn a sup
// ON DEMAND, gated so at most `desired` run at once. Two guards, cheapest first:
//   - active-sup gate: countActive (ACTIVE role=supervisor jobs — job state, not presence,
//     to avoid double-count). active>=desired ⇒ a sup is already draining work, do nothing.
//   - demand gate: countDemand (CountSupPendingDemand — pending sup-bound interactions not
//     yet punted to a human). demand<=0 ⇒ idle ⇒ spawn NOTHING ⇒ zero claude cost.
//
// Only when a sup is wanted (demand>0) AND none is running does it submit one per missing
// replica. A submit error aborts THIS call (the loop/wake retries) rather than spinning.
// Pure (no *Core / *gcli.Command) so it is unit-testable across the active×demand matrix.
func reconcileSupervisors(desired int, countActive, countDemand func() (int, error),
	submit func() error, logf, errf func(string, ...any)) {
	active, err := countActive()
	if err != nil {
		errf("gofer: sup reconcile active-count failed: %v\n", err)
		return
	}
	if active >= desired {
		return // a sup is already running (≤desired); it drains the current demand
	}
	demand, err := countDemand()
	if err != nil {
		errf("gofer: sup reconcile demand-count failed: %v\n", err)
		return
	}
	if demand <= 0 {
		return // idle: no pending sup demand → spawn nothing → zero claude cost
	}
	for i := active; i < desired; i++ {
		if err := submit(); err != nil {
			errf("gofer: sup reconcile submit failed: %v\n", err)
			return // best-effort; next wake/tick retries the remaining deficit
		}
		logf("gofer: sup reconciler dispatched a supervisor job on demand (active=%d, demand=%d, desired=%d)\n", active, demand, desired)
	}
}

// startSupReconcileLoop launches the event-driven supervisor reconciler (y5wt) when
// supervisor.desired_supervisors > 0 (opt-in; <=0 disables — same conservative default as
// the answerer). It spawns a sup ON DEMAND rather than keeping one resident: it reconciles
//   - on a wake signal (the answerer's escalate() found no reachable sup — low latency), and
//   - on a periodic backstop tick (covers a lost wake / a serve restart with pending work),
//
// each time spawning at most desired sups and only when CountSupPendingDemand>0. Idle ⇒ no
// sup ⇒ zero claude cost. `desired` is the concurrency CAP (default 1). The per-sup-job
// timeout still bounds a hung sup; a sup that finishes draining demand simply exits and is
// not re-spawned until new demand appears. Exits when stop closes.
func startSupReconcileLoop(c *gcli.Command, cr *core.Core, wake <-chan struct{}, stop <-chan struct{}) {
	sc := cr.Cfg.Supervisor
	if sc == nil || sc.DesiredSupervisors <= 0 {
		return
	}
	interval := supReconcileInterval
	if sc.ReconcileIntervalSec > 0 {
		interval = time.Duration(sc.ReconcileIntervalSec) * time.Second
	}
	runnerName := sc.ReconcileRunner
	if runnerName == "" {
		runnerName = "local"
	}
	desired := sc.DesiredSupervisors
	prompt := sc.ReconcilePrompt
	if prompt == "" {
		prompt = defaultSupReconcilePrompt
	}
	jobTimeout := sc.ReconcileJobTimeoutSec
	if jobTimeout <= 0 {
		jobTimeout = supReconcileJobTimeoutDefault
	}
	logf := func(f string, a ...any) { c.Printf(f, a...) }
	errf := func(f string, a ...any) { c.Errorf(f, a...) }
	countActive := func() (int, error) { return cr.Store.CountActiveJobsByRole("supervisor") }
	// ownerTimeout mirrors the answerer's owner-answer window so demand excludes interactions
	// still legitimately with their owner (L1) — only owner-less or owner-timed-out ones are
	// sup demand (CountSupPendingDemand precision). Mirrors NewService's <=0 default.
	ownerTimeout := int64(sc.OwnerAnswerTimeoutSec)
	if ownerTimeout <= 0 {
		ownerTimeout = int64(supervisor.DefaultOwnerAnswerTimeout.Seconds())
	}
	countDemand := func() (int, error) { return cr.Store.CountSupPendingDemand(ownerTimeout, time.Now().Unix()) }
	submit := func() error {
		// Role=supervisor pulls agent/system_prompt/env (incl. GOFER_AGENT_ROLE) from the
		// roles.supervisor preset (validated present at load when desired>0). Prompt is the
		// kickoff turn — required because a cli-agent (codex) rejects an empty prompt.
		// TimeoutSec bounds a hung sup; a healthy on-demand sup drains demand and exits early.
		_, err := cr.Jobs.Submit(job.JobRequest{
			Role: "supervisor", Runner: runnerName, Prompt: prompt, TimeoutSec: jobTimeout,
		})
		return err
	}
	reconcile := func() { reconcileSupervisors(desired, countActive, countDemand, submit, logf, errf) }
	c.Printf("gofer: supervisor reconciler enabled (event-driven; desired=%d, runner=%q, backstop=%s)\n", desired, runnerName, interval)
	go func() {
		reconcile() // startup: catch pending demand from before serve came up
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-wake: // answerer signalled "a sup is wanted" — dispatch promptly
				reconcile()
			case <-ticker.C: // backstop poll: lost wake / restart safety
				reconcile()
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

// workflowInterval is the工作流推进 sweeper cadence (crash recovery). The finish
// hook推进 the common case promptly; this only catches workflows whose hook was
// lost (process crash mid-step), so a relaxed tick is fine.
const workflowInterval = 30 * time.Second

// scheduleSweepInterval is below cron's 1min granularity, so minute cron entries
// are picked up promptly without a hot loop.
const scheduleSweepInterval = 30 * time.Second

// sweepSchedules handles one AUTO-02 cron pass: every due schedule is first
// advanced with a compare-and-swap update, then its embedded job request is
// submitted. Submit failures are logged after advance so a bad request does not
// pin next_run_at forever.
func sweepSchedules(now int64, due []jobstore.ScheduleRecord, missGrace int64,
	nextOf func(expr string, after int64) (int64, error),
	advance func(id string, oldNext, newNext int64) (bool, error),
	submit func(r jobstore.ScheduleRecord) (string, error),
	setLast func(id, jobID string),
	setEnabled func(id string, enabled int),
	logf, errf func(string, ...any)) {
	for _, r := range due {
		oldNext := r.NextRunAt
		if r.ScheduleType == "once" {
			ok, err := advance(r.ID, oldNext, 0)
			if err != nil {
				errf("gofer: schedule %s advance failed: %v\n", r.ID, err)
				continue
			}
			if !ok {
				continue
			}
			jobID, err := submit(r)
			if err != nil {
				errf("gofer: schedule %s submit failed: %v\n", r.ID, err)
				setEnabled(r.ID, 0)
				continue
			}
			setLast(r.ID, jobID)
			setEnabled(r.ID, 0)
			continue
		}
		newNext, err := nextOf(r.CronExpr, now)
		if err != nil {
			errf("gofer: schedule %s next cron failed: %v\n", r.ID, err)
			continue
		}
		ok, err := advance(r.ID, oldNext, newNext)
		if err != nil {
			errf("gofer: schedule %s advance failed: %v\n", r.ID, err)
			continue
		}
		if !ok {
			continue
		}
		if r.CatchUp == 0 && now-oldNext > missGrace {
			logf("gofer: schedule %s skipped missed run (miss=%ds, grace=%ds)\n", r.ID, now-oldNext, missGrace)
			continue
		}
		jobID, err := submit(r)
		if err != nil {
			errf("gofer: schedule %s submit failed: %v\n", r.ID, err)
			continue
		}
		setLast(r.ID, jobID)
	}
}

// startScheduleLoop launches the AUTO-02 cron schedule sweeper. It mirrors the
// other serve loops: sweep once at startup for crash recovery, then on every
// configured tick, and exit when stop closes.
func startScheduleLoop(c *gcli.Command, cr *core.Core, stop <-chan struct{}) {
	interval := scheduleSweepInterval
	if cr.Cfg.Schedule.SweepIntervalSec > 0 {
		interval = time.Duration(cr.Cfg.Schedule.SweepIntervalSec) * time.Second
	}
	missGrace := int64(cr.Cfg.Schedule.MissGraceSec)
	if missGrace <= 0 {
		missGrace = 3600
	}
	c.Printf("gofer: schedule sweeper enabled (interval=%s, miss_grace=%ds)\n", interval, missGrace)

	go func() {
		run := func() {
			now := time.Now().Unix()
			due, err := cr.Store.DueSchedules(now)
			if err != nil {
				c.Errorf("gofer: schedule due query failed: %v\n", err)
				return
			}
			sweepSchedules(now, due, missGrace,
				func(expr string, after int64) (int64, error) {
					return jobstore.NextCronRun(expr, time.Unix(after, 0))
				},
				func(id string, oldNext, newNext int64) (bool, error) {
					return cr.Store.AdvanceSchedule(id, oldNext, newNext, now)
				},
				func(r jobstore.ScheduleRecord) (string, error) {
					var req job.JobRequest
					if err := json.Unmarshal([]byte(r.RequestJSON), &req); err != nil {
						return "", err
					}
					req.Channel = "cron"
					res, err := cr.Jobs.Submit(req)
					if err != nil {
						return "", err
					}
					return res.ID, nil
				},
				func(id, jobID string) {
					if err := cr.Store.SetScheduleLastJob(id, jobID); err != nil {
						c.Errorf("gofer: schedule %s set last job failed: %v\n", id, err)
					}
				},
				func(id string, enabled int) {
					if err := cr.Store.SetScheduleEnabled(id, enabled); err != nil {
						c.Errorf("gofer: schedule %s set enabled failed: %v\n", id, err)
					}
				},
				func(f string, a ...any) { c.Printf(f, a...) },
				func(f string, a ...any) { c.Errorf(f, a...) },
			)
		}

		run()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}

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

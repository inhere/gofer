package commands

import (
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

	srv := httpapi.New(&cfg.Server, token, allowEmpty, core.Jobs, core.Projects, core.Agents, core.Hub)

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

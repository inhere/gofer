package commands

import (
	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/serve"
)

// serveOpts holds the serve command flags. The config path is the app-level
// global -c (config.InputCfgFile), not a per-command flag (P1).
var serveOpts = struct {
	addr          string
	token         string
	allowEmptyTok bool
	noWeb         bool
	daemon        bool
}{}

// servePIDFile / serveLogFile are the daemon-mode runtime files (c44):
// <config-dir>/run/serve.{pid,log}.
func servePIDFile() string { return config.RuntimeFilePath("run", "serve.pid") }
func serveLogFile() string { return config.RuntimeFilePath("run", "serve.log") }

// NewServeCmd builds the `serve` command: load config, wire the job service and
// the httpapi server, then start the HTTP control plane (plan §9-P5).
func NewServeCmd() *gcli.Command {
	return &gcli.Command{
		Name:    "serve",
		Desc:    "Start the gofer HTTP server",
		Aliases: []string{"s"},
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			c.StrOpt(&serveOpts.addr, "addr", "", "", "HTTP listen address (default from config / 0.0.0.0:8765)")
			c.StrOpt(&serveOpts.token, "token", "", "", "bearer token override (prefer config/env)")
			c.BoolOpt(&serveOpts.allowEmptyTok, "allow-empty-token", "", false, "allow starting without a token")
			c.BoolOpt(&serveOpts.noWeb, "no-web", "", false, "disable the web console (static UI)")
			c.BoolOpt(&serveOpts.daemon, "daemon", "d", false, "run in background (detached); logs to <config-dir>/run/serve.log")
		},
		Subs: []*gcli.Command{NewServeStopCmd()},
		Func: runServe,
	}
}

// NewServeStopCmd builds `gofer serve stop`: stop the backgrounded (-d) serve via
// its pidfile (counterpart to `serve -d`). See stopDaemon for the SIGTERM + wait
// semantics.
func NewServeStopCmd() *gcli.Command {
	return &gcli.Command{
		Name: "stop",
		Desc: "Stop the backgrounded (-d) serve via its pidfile",
		Func: runServeStop,
	}
}

func runServeStop(c *gcli.Command, _ []string) error {
	return stopDaemon(c, servePIDFile(), "serve")
}

// runServe loads the config and hands the resolved flags to serve.Start, which
// owns the process orchestration (BP2: config-path print, overlay merge, Core
// assembly, sweeper/probe/reload loops, httpapi). The command layer keeps only
// flag binding + config loading + the thin call.
func runServe(c *gcli.Command, _ []string) error {
	// -d/--daemon: the parent re-execs itself detached, prints the child pid and
	// returns; the detached child re-enters runServe with daemon.Daemonized()==true
	// and runs the real server below (c44). A second start is refused when a live
	// pidfile exists.
	if serveOpts.daemon && !daemon.Daemonized() {
		pid, err := daemon.Spawn(daemon.Options{Name: "serve", PIDPath: servePIDFile(), LogPath: serveLogFile()})
		if err != nil {
			return errorx.Failf(serve.ExitErr, "%v", err)
		}
		c.Printf("gofer serve 已后台启动 pid=%d log=%s\n", pid, serveLogFile())
		return nil
	}
	// Detached child: remove the pidfile when serve.Start returns (graceful
	// shutdown), so a stale pidfile never lingers after a clean stop.
	if daemon.Daemonized() {
		defer daemon.RemovePIDFile(servePIDFile())
	}

	cfg, cfgPath, err := config.Load(config.InputCfgFile)
	if err != nil {
		return errorx.Failf(serve.ExitErr, "%v", err)
	}

	return serve.Start(c, cfg, serve.Opts{
		Addr:          serveOpts.addr,
		Token:         serveOpts.token,
		AllowEmptyTok: serveOpts.allowEmptyTok,
		NoWeb:         serveOpts.noWeb,
		CfgPath:       cfgPath,
		ReloadPath:    config.InputCfgFile,
	})
}

package commands

import (
	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/serve"
)

// serveOpts holds the serve command flags. The config path is the app-level
// global -c (config.InputCfgFile), not a per-command flag (P1).
var serveOpts = struct {
	addr          string
	token         string
	allowEmptyTok bool
	noWeb         bool
}{}

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
		},
		Func: runServe,
	}
}

// runServe loads the config and hands the resolved flags to serve.Start, which
// owns the process orchestration (BP2: config-path print, overlay merge, Core
// assembly, sweeper/probe/reload loops, httpapi). The command layer keeps only
// flag binding + config loading + the thin call.
func runServe(c *gcli.Command, _ []string) error {
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

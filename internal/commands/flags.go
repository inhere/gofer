package commands

import (
	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
)

// bindConfigFlag binds the shared -c/--config flag to the global
// config.InputCfgFile so every subcommand resolves the same config source.
//
// P1.5: gcli v3.8 only consumes an app-level flag BEFORE the command name
// (`gofer -c X serve`), it does not下放 to subcommands — so `gofer serve -c X`
// (flag after the command, the git/docker/kubectl convention) reported
// "option not defined: -c". Binding -c again on each subcommand fixes that.
//
// The default is the empty string (NOT ${GOFER_CONFIG}) on purpose: the
// app-level -c (app.go) is parsed first and, when present, has already written
// config.InputCfgFile before this binding runs. gcli's strOpt keeps a non-empty
// *p as the new default (`if *p != "" { opt.DefVal = *p }`), so an empty default
// here never clobbers a value already set on the command line front
// (`gofer -c X serve`). An unset -c leaves config.InputCfgFile empty, so
// config.Load("") falls through env GOFER_CONFIG → the discovery chain (D-A2).
func bindConfigFlag(c *gcli.Command) {
	c.StrOpt(&config.InputCfgFile, "config", "c", "", "path to the gofer config file")
}

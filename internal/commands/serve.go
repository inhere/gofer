package commands

import "github.com/gookit/gcli/v3"

// serveOpts holds the serve command flags. Logic is implemented in P5.
var serveOpts = struct {
	config        string
	addr          string
	token         string
	allowEmptyTok bool
}{}

// NewServeCmd builds the `serve` command. P1: placeholder body, flags declared
// for --help visibility; real HTTP server wiring lands in P5.
func NewServeCmd() *gcli.Command {
	return &gcli.Command{
		Name: "serve",
		Desc: "Start the agent-bridge HTTP server",
		Config: func(c *gcli.Command) {
			c.StrOpt(&serveOpts.config, "config", "c", "", "path to the bridge config file")
			c.StrOpt(&serveOpts.addr, "addr", "", "0.0.0.0:8765", "HTTP listen address")
			c.StrOpt(&serveOpts.token, "token", "", "", "bearer token override (prefer config/env)")
			c.BoolOpt(&serveOpts.allowEmptyTok, "allow-empty-token", "", false, "allow starting without a token")
		},
		Func: func(c *gcli.Command, args []string) error {
			c.Println("serve: not implemented yet (P5)")
			return nil
		},
	}
}

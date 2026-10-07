//go:build windows

package commands

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/servicemgr"
)

var superviseSpecPath string

// newServePlatformCommands registers the private entry used by the native
// scheduled task. Public management commands are integrated in T6.
func newServePlatformCommands() []*gcli.Command {
	return []*gcli.Command{{
		Name: "supervise", Hidden: true,
		Desc: "Run the registered Windows server supervisor",
		Config: func(c *gcli.Command) {
			c.StrOpt(&superviseSpecPath, "spec", "", "", "absolute managed service spec path")
		},
		Func: func(_ *gcli.Command, _ []string) error {
			if superviseSpecPath == "" {
				return errors.New("--spec is required")
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return servicemgr.Supervise(ctx, superviseSpecPath)
		},
	}}
}

package commands

import (
	"context"
	"fmt"
	"github.com/coder/websocket"
	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tunnel"
	"os"
	"os/signal"
	"time"
)

var tunnelOpts struct {
	worker string
	quiet  bool
}

// NewTunnelCmd builds tunnel subcommands.
func NewTunnelCmd() *gcli.Command {
	f := &gcli.Command{Name: "forward", Aliases: []string{"fwd"}, Desc: "Forward local TCP ports through a worker tunnel.", Config: func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.StrOpt(&tunnelOpts.worker, "worker", "w", "", "worker id")
		c.BoolOpt(&tunnelOpts.quiet, "quiet", "", false, "quiet")
		c.AddArg("spec", "forward spec [bind:]lport:host:port (one or more)", true, true)
	}, Func: runTunnelForward}
	ch := &gcli.Command{Name: "check", Desc: "Check connectivity to a worker tunnel target.", Config: func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.StrOpt(&tunnelOpts.worker, "worker", "w", "", "worker id")
		c.AddArg("target", "host:port", true)
	}, Func: runTunnelCheck}
	ls := &gcli.Command{Name: "ls", Aliases: []string{"list"}, Desc: "List active worker tunnels.", Config: func(c *gcli.Command) { bindConfigFlag(c); bindServerFlags(c) }, Func: runTunnelList}
	return &gcli.Command{Name: "tunnel", Aliases: []string{"tun"}, Desc: "Manage TCP tunnels.", Subs: []*gcli.Command{f, ch, ls}}
}
func tunnelClient() (*client.Client, error) {
	return newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
}
func runTunnelForward(c *gcli.Command, args []string) error {
	if tunnelOpts.worker == "" {
		return fmt.Errorf("worker required")
	}
	cli, e := tunnelClient()
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	// Positional args declared via AddArg are bound to the named arg, not passed in args.
	specs := c.Arg("spec").Array()
	errCh := make(chan error, len(specs))
	for _, s := range specs {
		sp, e := tunnel.ParseForwardSpec(s)
		if e != nil {
			return e
		}
		f := &tunnel.Forwarder{Spec: sp, Ready: make(chan error, 1), Dial: func(x context.Context) (*websocket.Conn, error) {
			return cli.DialTunnel(x, tunnelOpts.worker, sp.Target)
		}}
		if !tunnelOpts.quiet {
			// One line per local connection (up, and closed with byte counts).
			// --quiet leaves Log nil, which still logs dial failures via slog.
			f.Log = func(format string, args ...any) { fmt.Printf(format+"\n", args...) }
		}
		go func() { errCh <- f.Run(ctx) }()
		if e := <-f.Ready; e != nil {
			return e
		}
		if !tunnelOpts.quiet {
			fmt.Printf("forwarding %s -> %s:%s\n", f.ActualAddr, tunnelOpts.worker, sp.Target)
		}
	}
	for {
		select {
		case err := <-errCh:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}
func runTunnelCheck(c *gcli.Command, _ []string) error {
	if tunnelOpts.worker == "" {
		return fmt.Errorf("worker required")
	}
	target := c.Arg("target").String()
	if target == "" {
		return fmt.Errorf("target required")
	}
	cli, e := tunnelClient()
	if e != nil {
		return e
	}
	st := time.Now()
	ws, e := cli.DialTunnel(context.Background(), tunnelOpts.worker, target)
	if e != nil {
		return e
	}
	ws.Close(websocket.StatusNormalClosure, "")
	fmt.Printf("OK %s -> %s (%d ms)\n", tunnelOpts.worker, target, time.Since(st).Milliseconds())
	return nil
}
func runTunnelList(c *gcli.Command, _ []string) error {
	cli, e := tunnelClient()
	if e != nil {
		return e
	}
	ts, e := cli.ListTunnels()
	if e != nil {
		return e
	}
	if len(ts) == 0 {
		fmt.Println("no active tunnels")
		return nil
	}
	fmt.Println("ID CALLER WORKER TARGET CLIENT AGE UP DOWN")
	for _, t := range ts {
		fmt.Printf("%s %s %s %s %s %s %d %d\n", t.ID, t.CallerID, t.WorkerID, t.Target, t.ClientRemote, time.Since(t.StartedAt).Round(time.Second), t.BytesUp, t.BytesDown)
	}
	return nil
}

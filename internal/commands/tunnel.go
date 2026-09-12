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
	"sort"
	"strings"
	"time"
)

var tunnelOpts struct {
	worker string
	quiet  bool
	name   string
	note   string
	force  bool
}

// NewTunnelCmd builds tunnel subcommands.
func NewTunnelCmd() *gcli.Command {
	f := &gcli.Command{Name: "forward", Aliases: []string{"fwd"}, Desc: "Forward local ports through a worker tunnel.", Config: func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.StrOpt(&tunnelOpts.worker, "worker", "w", "", "worker id")
		c.BoolOpt(&tunnelOpts.quiet, "quiet", "", false, "quiet")
		c.StrOpt(&tunnelOpts.name, "name", "n", "", "saved preset name (gofer tunnel saved)")
		c.AddArg("spec", "forward spec [udp/][bind:]lport:host:port (one or more; optional with --name)", false, true)
	}, Func: runTunnelForward}
	save := &gcli.Command{Name: "save", Aliases: []string{"record"}, Desc: "Save a forward preset for reuse with --name.", Config: func(c *gcli.Command) {
		bindConfigFlag(c)
		c.StrOpt(&tunnelOpts.worker, "worker", "w", "", "worker id")
		c.StrOpt(&tunnelOpts.note, "note", "", "", "free-form remark shown by `tunnel saved`")
		c.BoolOpt(&tunnelOpts.force, "force", "", false, "overwrite an existing preset of the same name")
		c.AddArg("name", "preset name", true, false)
		c.AddArg("spec", "forward spec [udp/][bind:]lport:host:port (one or more)", true, true)
	}, Func: runTunnelSave}
	saved := &gcli.Command{Name: "saved", Aliases: []string{"profiles"}, Desc: "List saved forward presets.", Config: func(c *gcli.Command) { bindConfigFlag(c) }, Func: runTunnelSaved}
	forget := &gcli.Command{Name: "forget", Desc: "Delete a saved forward preset.", Config: func(c *gcli.Command) {
		bindConfigFlag(c)
		c.AddArg("name", "preset name", true, false)
	}, Func: runTunnelForget}
	ch := &gcli.Command{Name: "check", Desc: "Check connectivity to a worker tunnel target.", Config: func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.StrOpt(&tunnelOpts.worker, "worker", "w", "", "worker id")
		c.StrOpt(&tunnelOpts.name, "name", "n", "", "check every target of a saved preset")
		c.AddArg("target", "[udp/]host:port", false)
	}, Func: runTunnelCheck}
	ls := &gcli.Command{Name: "ls", Aliases: []string{"list"}, Desc: "List active worker tunnels.", Config: func(c *gcli.Command) { bindConfigFlag(c); bindServerFlags(c) }, Func: runTunnelList}
	return &gcli.Command{Name: "tunnel", Aliases: []string{"tun"}, Desc: "Manage TCP/UDP tunnels.", Subs: []*gcli.Command{f, ch, ls, save, saved, forget}}
}
func tunnelClient() (*client.Client, error) {
	return newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
}

// resolveForward decides which worker and forward specs a command runs with.
// Explicit flags always win and a preset only fills what the caller left empty,
// so `--name hw -w other` is a one-off override that does not edit the preset.
func resolveForward(name, worker string, specs []string) (string, []string, error) {
	if name != "" {
		t, err := config.LoadTunnels()
		if err != nil {
			return "", nil, err
		}
		p, ok := t.Forwards[name]
		if !ok {
			return "", nil, fmt.Errorf("tunnel preset %q not found (gofer tunnel saved)", name)
		}
		if worker == "" {
			worker = p.Worker
		}
		if len(specs) == 0 {
			specs = p.Specs
		}
	}
	if worker == "" {
		return "", nil, fmt.Errorf("worker required")
	}
	if len(specs) == 0 {
		return "", nil, fmt.Errorf("spec required")
	}
	return worker, specs, nil
}

// presetWorker returns just the worker a preset names, for commands that take
// their target from the command line but still want `--name` to say where.
func presetWorker(name string) (string, error) {
	t, err := config.LoadTunnels()
	if err != nil {
		return "", err
	}
	p, ok := t.Forwards[name]
	if !ok {
		return "", fmt.Errorf("tunnel preset %q not found (gofer tunnel saved)", name)
	}
	return p.Worker, nil
}

// checkTarget is one device address to probe, with the network to reach it on.
type checkTarget struct {
	network string
	addr    string
}

// presetCheckTargets turns a preset's forward specs into the addresses to probe.
// A preset stores SPECS ("[udp/][bind:]lport:host:port"), whose target half is the
// device address — probing the spec itself would dial a local port number.
func presetCheckTargets(specs []string) ([]checkTarget, error) {
	out := make([]checkTarget, 0, len(specs))
	for _, s := range specs {
		sp, err := tunnel.ParseForwardSpec(s)
		if err != nil {
			return nil, err
		}
		out = append(out, checkTarget{network: sp.Network, addr: sp.Target})
	}
	return out, nil
}

// parseCheckTarget reads an explicit `[udp/]host:port` argument.
func parseCheckTarget(s string) checkTarget {
	if rest, ok := strings.CutPrefix(s, "udp/"); ok {
		return checkTarget{network: "udp", addr: rest}
	}
	return checkTarget{network: "tcp", addr: s}
}

func runTunnelForward(c *gcli.Command, args []string) error {
	if tunnelOpts.worker == "" && tunnelOpts.name == "" {
		return fmt.Errorf("worker required")
	}
	// Positional args declared via AddArg are bound to the named arg, not passed in args.
	worker, specs, err := resolveForward(tunnelOpts.name, tunnelOpts.worker, c.Arg("spec").Array())
	if err != nil {
		return err
	}
	cli, e := tunnelClient()
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	errCh := make(chan error, len(specs))
	for _, s := range specs {
		sp, e := tunnel.ParseForwardSpec(s)
		if e != nil {
			return e
		}
		f := &tunnel.Forwarder{Spec: sp, Ready: make(chan error, 1), Dial: func(x context.Context) (*websocket.Conn, error) {
			return cli.DialTunnel(x, worker, sp.Target, sp.Network)
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
			// Mark udp: the two listeners behave differently enough (per-source
			// sessions, idle reaping) that the startup line should say which one ran.
			label := ""
			if sp.Network == "udp" {
				label = "UDP "
			}
			fmt.Printf("forwarding %s%s -> %s:%s\n", label, f.ActualAddr, worker, sp.Target)
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
	if tunnelOpts.worker == "" && tunnelOpts.name == "" {
		return fmt.Errorf("worker required")
	}
	worker, targets := tunnelOpts.worker, []checkTarget(nil)
	if explicit := c.Arg("target").String(); explicit != "" {
		// An explicit target wins over the preset's, same as an explicit -w; the
		// preset is then only consulted for the worker.
		if worker == "" {
			w, err := presetWorker(tunnelOpts.name)
			if err != nil {
				return err
			}
			worker = w
		}
		targets = []checkTarget{parseCheckTarget(explicit)}
	} else {
		w, specs, err := resolveForward(tunnelOpts.name, worker, nil)
		if err != nil {
			return err
		}
		worker = w
		if targets, err = presetCheckTargets(specs); err != nil {
			return err
		}
	}
	cli, e := tunnelClient()
	if e != nil {
		return e
	}
	for _, tg := range targets {
		st := time.Now()
		ws, e := cli.DialTunnel(context.Background(), worker, tg.addr, tg.network)
		if e != nil {
			return e
		}
		ws.Close(websocket.StatusNormalClosure, "")
		label := ""
		if tg.network == "udp" {
			// A udp "dial" completes without a handshake, so OK proves authorization
			// and the worker-side socket, not that the device answered.
			label = "UDP "
		}
		fmt.Printf("OK %s%s -> %s (%d ms)\n", label, worker, tg.addr, time.Since(st).Milliseconds())
	}
	return nil
}

func runTunnelSave(c *gcli.Command, _ []string) error {
	if tunnelOpts.worker == "" {
		return fmt.Errorf("worker required")
	}
	name, specs := c.Arg("name").String(), c.Arg("spec").Array()
	if err := config.UpsertTunnel(name, config.TunnelProfile{Worker: tunnelOpts.worker, Specs: specs, Note: tunnelOpts.note}, tunnelOpts.force); err != nil {
		return err
	}
	fmt.Printf("saved preset %s (%s: %s)\n", name, tunnelOpts.worker, strings.Join(specs, " "))
	return nil
}
func runTunnelSaved(c *gcli.Command, _ []string) error {
	t, e := config.LoadTunnels()
	if e != nil {
		return e
	}
	if len(t.Forwards) == 0 {
		fmt.Println("no saved presets")
		return nil
	}
	names := make([]string, 0, len(t.Forwards))
	for n := range t.Forwards {
		names = append(names, n)
	}
	sort.Strings(names) // map order is random; a listing must be stable to be readable
	fmt.Println("NAME WORKER SPECS NOTE")
	for _, n := range names {
		p := t.Forwards[n]
		fmt.Printf("%s %s %s %s\n", n, p.Worker, strings.Join(p.Specs, ","), p.Note)
	}
	return nil
}
func runTunnelForget(c *gcli.Command, _ []string) error {
	name := c.Arg("name").String()
	if err := config.DeleteTunnel(name); err != nil {
		return err
	}
	fmt.Printf("deleted preset %s\n", name)
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

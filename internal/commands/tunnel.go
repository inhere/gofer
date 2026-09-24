package commands

import (
	"context"
	"fmt"
	"github.com/coder/websocket"
	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/logx"
	"github.com/inhere/gofer/internal/tunnel"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var tunnelOpts struct {
	worker  string
	quiet   bool
	logFile string
	logDir  string
	name    string
	note    string
	force   bool
}

// NewTunnelCmd builds tunnel subcommands.
func NewTunnelCmd() *gcli.Command {
	f := &gcli.Command{Name: "forward", Aliases: []string{"fwd"}, Desc: "Forward local ports through a worker tunnel.", Config: func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.StrOpt(&tunnelOpts.worker, "worker", "w", "", "worker id")
		c.BoolOpt(&tunnelOpts.quiet, "quiet", "", false, "quiet")
		c.StrOpt(&tunnelOpts.logFile, "log-file", "", "", "forwarder log file")
		c.StrOpt(&tunnelOpts.logDir, "log-dir", "", "", "forwarder log directory")
		c.StrOpt(&tunnelOpts.name, "name", "n", "", "saved preset name (gofer tunnel saved)")
		c.AddArg("spec", "forward spec [udp/][bind:]lport:host:port (one or more; optional with --name)", false, true)
	}, Func: runTunnelForward}
	save := &gcli.Command{Name: "save", Aliases: []string{"record"}, Desc: "Save a forward preset for reuse with --name (stored on the server).", Config: func(c *gcli.Command) {
		bindConfigFlag(c)
		// TUN-03: `save` writes the server's preset store, so it needs the connection
		// flags — without them a client node could not choose which hub to save to (the
		// env default was the only way, and `--server` was a usage error).
		bindServerFlags(c)
		c.StrOpt(&tunnelOpts.worker, "worker", "w", "", "worker id")
		c.StrOpt(&tunnelOpts.note, "note", "", "", "free-form remark shown by `tunnel saved`")
		c.BoolOpt(&tunnelOpts.force, "force", "", false, "overwrite an existing preset of the same name")
		c.AddArg("name", "preset name", true, false)
		c.AddArg("spec", "forward spec [udp/][bind:]lport:host:port (one or more)", true, true)
	}, Func: runTunnelSave}
	saved := &gcli.Command{Name: "saved", Aliases: []string{"profiles"}, Desc: "List saved forward presets (the server's, plus local ones not uploaded).", Config: func(c *gcli.Command) { bindConfigFlag(c); bindServerFlags(c) }, Func: runTunnelSaved}
	forget := &gcli.Command{Name: "forget", Desc: "Delete a saved forward preset.", Config: func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.AddArg("name", "preset name", true, false)
	}, Func: runTunnelForget}
	presets := &gcli.Command{Name: "presets", Desc: "Manage the presets stored on the server.", Subs: []*gcli.Command{
		{Name: "push", Desc: "Upload local presets to the server (names it already has are skipped).", Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.BoolOpt(&tunnelOpts.force, "force", "", false, "overwrite a preset the server already has")
		}, Func: runTunnelPresetsPush},
	}}
	ch := &gcli.Command{Name: "check", Desc: "Check connectivity to a worker tunnel target.", Config: func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.StrOpt(&tunnelOpts.worker, "worker", "w", "", "worker id")
		c.StrOpt(&tunnelOpts.name, "name", "n", "", "check every target of a saved preset")
		c.AddArg("target", "[udp/]host:port", false)
	}, Func: runTunnelCheck}
	ls := &gcli.Command{Name: "ls", Aliases: []string{"list"}, Desc: "List active worker tunnels.", Config: func(c *gcli.Command) { bindConfigFlag(c); bindServerFlags(c) }, Func: runTunnelList}
	return &gcli.Command{Name: "tunnel", Aliases: []string{"tun"}, Desc: "Manage TCP/UDP tunnels.", Subs: []*gcli.Command{f, ch, ls, save, saved, forget, presets}}
}
func tunnelClient() (*client.Client, error) {
	return newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
}

// resolvedForward is what a forward/check run resolved: the worker and rules to run,
// plus the note to print when the answer came from the LOCAL file (TUN-03 — the server
// is the source of truth, the file is the fallback).
type resolvedForward struct {
	worker string
	specs  []string
	note   string
}

// lookupPreset resolves a preset NAME: the server first, then the local
// tunnels.yaml. A local answer carries the note telling the operator how to upload it;
// a name neither end knows is an error.
//
// The local read exists only as a fallback for a preset the server does not have (or
// cannot answer about at all). DEPRECATED(v0.60.2): remove in v0.63 — with the local
// file gone there is one answer, and this fallback goes with it.
func lookupPreset(cli *client.Client, name string) (config.TunnelProfile, string, error) {
	if cli == nil {
		return localPreset(name, "no server configured; ")
	}
	p, err := cli.GetTunnelPreset(name)
	if err == nil {
		return config.TunnelProfile{Worker: p.Worker, Specs: p.Specs, Note: p.Note}, "", nil
	}
	if client.StatusOf(err) != http.StatusNotFound {
		// The hub could not answer (down, unreachable, or an error on its side). The
		// local file still works, and the note says which of the two happened.
		return localPreset(name, fmt.Sprintf("server unavailable (%v); ", err))
	}
	return localPreset(name, "the server has no such preset; ")
}

// localPreset reads the fallback preset file and returns the profile plus the nudge to
// upload it.
func localPreset(name, why string) (config.TunnelProfile, string, error) {
	t, err := config.LoadTunnels()
	if err != nil {
		return config.TunnelProfile{}, "", err
	}
	p, ok := t.Forwards[name]
	if !ok {
		return config.TunnelProfile{}, "", fmt.Errorf("tunnel preset %q not found (gofer tun saved)", name)
	}
	return p, fmt.Sprintf("using local preset %q (%s`gofer tun presets push` uploads it)", name, why), nil
}

// resolveForward decides which worker and forward specs a command runs with.
// Explicit flags always win and a preset only fills what the caller left empty,
// so `--name hw -w other` is a one-off override that does not edit the preset.
func resolveForward(cli *client.Client, name, worker string, specs []string) (resolvedForward, error) {
	out := resolvedForward{worker: worker, specs: specs}
	if name != "" {
		p, note, err := lookupPreset(cli, name)
		if err != nil {
			return resolvedForward{}, err
		}
		if out.worker == "" {
			out.worker = p.Worker
		}
		if len(out.specs) == 0 {
			out.specs = p.Specs
		}
		out.note = note
	}
	if out.worker == "" {
		return resolvedForward{}, fmt.Errorf("worker required")
	}
	if len(out.specs) == 0 {
		return resolvedForward{}, fmt.Errorf("spec required")
	}
	return out, nil
}

// presetWorker returns just the worker a preset names, for commands that take
// their target from the command line but still want `--name` to say where.
func presetWorker(cli *client.Client, name string) (string, string, error) {
	p, note, err := lookupPreset(cli, name)
	if err != nil {
		return "", "", err
	}
	return p.Worker, note, nil
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
	// A preset written before (or outside) `tun save` may still carry a comma-joined
	// list: flatten it the same way every other spec entry point does (TUN-04).
	flat, err := tunnel.SplitSpecs(specs)
	if err != nil {
		return nil, err
	}
	parsed, err := tunnel.ParseSpecs(flat)
	if err != nil {
		return nil, err
	}
	out := make([]checkTarget, 0, len(parsed))
	for _, sp := range parsed {
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
	if tunnelOpts.logFile != "" && tunnelOpts.logDir != "" {
		return fmt.Errorf("--log-file and --log-dir are mutually exclusive")
	}
	if tunnelOpts.worker == "" && tunnelOpts.name == "" {
		return fmt.Errorf("worker required")
	}
	cli, e := tunnelClient()
	if e != nil {
		return e
	}
	// Positional args declared via AddArg are bound to the named arg, not passed in args.
	rf, err := resolveForward(cli, tunnelOpts.name, tunnelOpts.worker, c.Arg("spec").Array())
	if err != nil {
		return err
	}
	if rf.note != "" {
		c.Printf("%s\n", rf.note)
	}
	// TUN-04: one argument may carry several comma-separated rules; the parse error
	// names the offending entry instead of reporting a bare "invalid spec".
	raw, err := tunnel.SplitSpecs(rf.specs)
	if err != nil {
		return err
	}
	parsed, err := tunnel.ParseSpecs(raw)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if e := configureForwardLogging(tunnelOpts.quiet, tunnelOpts.logFile, tunnelOpts.logDir, time.Now(), os.Getpid()); e != nil {
		return e
	}
	errCh := make(chan error, len(parsed))
	registered := make([]client.TunnelSpecView, 0, len(parsed))
	for _, sp := range parsed {
		f := &tunnel.Forwarder{Spec: sp, Ready: make(chan error, 1), Dial: func(x context.Context) (tunnel.DialResult, error) {
			tc, err := cli.DialTunnel(x, rf.worker, sp.Target, sp.Network)
			return tunnel.DialResult{Conn: tc.Conn, TunnelID: tc.TunnelID}, err
		}}
		f.OnEvent = forwardEventSink(rf.worker)
		go func() { errCh <- f.Run(ctx) }()
		if e := <-f.Ready; e != nil {
			return e
		}
		registered = append(registered, client.TunnelSpecView{
			Network: sp.Network, Bind: sp.Bind, LocalPort: sp.LocalPort, Target: sp.Target,
		})
	}
	// TUN-03: every listener is up, so tell the hub — the console can then show this
	// forwarder instead of an empty list until the first connection arrives. Best
	// effort: a hub that cannot be reached never blocks the forwarding itself.
	unregister := registerForwarder(ctx, c, cli, rf.worker, registered)
	defer unregister()
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

// forwarderHeartbeatInterval is the TUN-03 renewal period. The hub expires a silent
// registration after three of them (90s by default), so one lost heartbeat is harmless.
const forwarderHeartbeatInterval = 30 * time.Second

// registerForwarder announces this forwarder to the hub and keeps the registration
// alive until the returned func is called — which also REMOVES it, so a normal exit and
// a Ctrl+C both leave the online list clean.
//
// Registration is display state, never a forwarding dependency: a hub that refuses it
// (old server, wrong token) or cannot be reached only warns. A heartbeat that comes back
// 404 means the hub forgot the registration (it restarted, or the TTL lapsed) — the
// loop re-registers so the console keeps seeing a live forwarder.
func registerForwarder(ctx context.Context, c *gcli.Command, cli *client.Client, worker string, specs []client.TunnelSpecView) func() {
	host, _ := os.Hostname()
	reg, err := cli.RegisterTunnelForwarder(client.TunnelForwarderRegistration{
		Worker: worker, Specs: specs, Host: host, PID: os.Getpid(), StartedAt: time.Now(),
	})
	if err != nil {
		c.Printf("warning: forwarder registration failed (%v); the forward runs, but `gofer tun ls` will not list it\n", err)
		return func() {}
	}
	c.Printf("registered as %s\n", reg.ID)
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		id := reg.ID
		ticker := time.NewTicker(forwarderHeartbeatInterval)
		defer ticker.Stop()
	heartbeat:
		for {
			select {
			case <-done:
			case <-ctx.Done():
			case <-ticker.C:
				if _, err := cli.HeartbeatTunnelForwarder(id, nil); err != nil {
					if client.StatusOf(err) == http.StatusNotFound {
						n, rerr := cli.RegisterTunnelForwarder(client.TunnelForwarderRegistration{
							Worker: worker, Specs: specs, Host: host, PID: os.Getpid(), StartedAt: time.Now(),
						})
						if rerr != nil {
							c.Printf("warning: forwarder re-registration failed: %v\n", rerr)
							continue
						}
						id = n.ID
						c.Printf("re-registered as %s (the hub had dropped the previous registration)\n", id)
						continue
					}
					c.Printf("warning: forwarder heartbeat failed: %v\n", err)
				}
				continue heartbeat
			}
			break
		}
		if err := cli.UnregisterTunnelForwarder(id); err != nil {
			c.Printf("warning: removing forwarder registration %s failed: %v\n", id, err)
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

// forwardLogPath picks the forwarder's JSONL log file. An explicit --log-file
// wins; otherwise the file lives in --log-dir (or <config-dir>/run/tunnels) and
// is named after the start time and pid so concurrent forwarders never share a
// file. explicit reports whether the user chose the location, which decides
// whether an unwritable path is fatal (see logx.FileOptions.Explicit).
func forwardLogPath(logFile, logDir string, now time.Time, pid int) (path string, explicit bool) {
	if logFile != "" {
		return logFile, true
	}
	dir, explicit := logDir, logDir != ""
	if dir == "" {
		cd, err := config.ConfigDir()
		if err != nil {
			return "", false
		}
		dir = filepath.Join(cd, "run", "tunnels")
	}
	return filepath.Join(dir, fmt.Sprintf("forward-%s-%d.log", now.Format("20060102-150405"), pid)), explicit
}

// configureForwardLogging wires the forward command's two sinks: --quiet drops
// the terminal (stderr) sink only, the file sink is always attempted. An
// unwritable default location degrades to a warning; an explicit one fails.
func configureForwardLogging(quiet bool, logFile, logDir string, now time.Time, pid int) error {
	if quiet {
		logx.SilenceStderr()
	}
	path, explicit := forwardLogPath(logFile, logDir, now, pid)
	if path == "" {
		return nil
	}
	return logx.ConfigureFile(logx.FileOptions{Path: path, Explicit: explicit, Component: "forward"})
}

// forwardEventSink turns Forwarder events into slog records tagged with the
// worker; the Forwarder already supplies event-specific attrs such as
// session_id, tunnel_id, first_byte_ms and close_reason.
func forwardEventSink(worker string) func(string, ...any) {
	return func(event string, attrs ...any) {
		slog.Info(event, append([]any{"event", event, "worker", worker}, attrs...)...)
	}
}

func runTunnelCheck(c *gcli.Command, _ []string) error {
	if tunnelOpts.worker == "" && tunnelOpts.name == "" {
		return fmt.Errorf("worker required")
	}
	cli, e := tunnelClient()
	if e != nil {
		return e
	}
	worker, targets := tunnelOpts.worker, []checkTarget(nil)
	if explicit := c.Arg("target").String(); explicit != "" {
		// An explicit target wins over the preset's, same as an explicit -w; the
		// preset is then only consulted for the worker.
		if worker == "" {
			w, note, err := presetWorker(cli, tunnelOpts.name)
			if err != nil {
				return err
			}
			worker = w
			if note != "" {
				c.Printf("%s\n", note)
			}
		}
		targets = []checkTarget{parseCheckTarget(explicit)}
	} else {
		rf, err := resolveForward(cli, tunnelOpts.name, worker, nil)
		if err != nil {
			return err
		}
		worker = rf.worker
		if rf.note != "" {
			c.Printf("%s\n", rf.note)
		}
		if targets, err = presetCheckTargets(rf.specs); err != nil {
			return err
		}
	}
	for _, tg := range targets {
		st := time.Now()
		tc, e := cli.DialTunnel(context.Background(), worker, tg.addr, tg.network)
		ws := tc.Conn
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
		c.Printf("OK %s%s -> %s (%d ms)\n", label, worker, tg.addr, time.Since(st).Milliseconds())
	}
	return nil
}

func runTunnelSave(c *gcli.Command, _ []string) error {
	if tunnelOpts.worker == "" {
		return fmt.Errorf("worker required")
	}
	name := c.Arg("name").String()
	// TUN-04: a comma-joined argument carries several rules; they are stored SPLIT.
	specs, err := tunnel.SplitSpecs(c.Arg("spec").Array())
	if err != nil {
		return err
	}
	profile := config.TunnelProfile{Worker: tunnelOpts.worker, Specs: specs, Note: tunnelOpts.note}
	if err := config.ValidateTunnelProfile(name, profile); err != nil {
		return err
	}
	cli, cerr := tunnelClient()
	if cerr == nil {
		_, err := cli.PutTunnelPreset(name, client.TunnelPreset{Worker: profile.Worker, Specs: profile.Specs, Note: profile.Note}, tunnelOpts.force)
		if err == nil {
			c.Printf("saved preset %s on the server (%s: %s)\n", name, profile.Worker, strings.Join(profile.Specs, " "))
			return nil
		}
		if client.StatusOf(err) != 0 {
			// The server ANSWERED and refused (a name conflict without --force, a rejected
			// profile). That is a real error: writing a local copy the server refuses would
			// leave the two ends disagreeing about the same name.
			return err
		}
		c.Printf("warning: server unreachable (%v); saving the preset locally\n", err)
	} else {
		c.Printf("warning: no server configured (%v); saving the preset locally\n", cerr)
	}
	if err := config.UpsertTunnel(name, profile, tunnelOpts.force); err != nil {
		return err
	}
	c.Printf("saved preset %s locally (%s: %s)\n", name, profile.Worker, strings.Join(profile.Specs, " "))
	c.Println("upload it to the server with `gofer tun presets push`")
	return nil
}

func runTunnelSaved(c *gcli.Command, _ []string) error {
	cli, err := tunnelClient()
	if err != nil {
		return err
	}
	presets, err := cli.ListTunnelPresets()
	if err != nil {
		return err
	}
	if len(presets) == 0 {
		c.Println("no saved presets on the server")
	} else {
		c.Println("NAME WORKER SPECS NOTE")
		for _, p := range presets {
			c.Printf("%s %s %s %s\n", p.Name, p.Worker, strings.Join(p.Specs, ","), p.Note)
		}
	}
	// The local file is only a fallback now, so a preset that lives ONLY there is worth
	// reporting: it is invisible to every other machine until it is uploaded.
	local, lerr := config.LoadTunnels()
	if lerr != nil {
		return lerr
	}
	onServer := make(map[string]bool, len(presets))
	for _, p := range presets {
		onServer[p.Name] = true
	}
	missing := make([]string, 0, len(local.Forwards))
	for name := range local.Forwards {
		if !onServer[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		c.Printf("local: %d preset(s) not on the server: %s (upload with `gofer tun presets push`)\n",
			len(missing), strings.Join(missing, " "))
	}
	return nil
}

func runTunnelForget(c *gcli.Command, _ []string) error {
	name := c.Arg("name").String()
	deleted := make([]string, 0, 2)
	if cli, cerr := tunnelClient(); cerr == nil {
		switch err := cli.DeleteTunnelPreset(name); client.StatusOf(err) {
		case 0:
			deleted = append(deleted, "server")
		case http.StatusNotFound:
			// Not on the server: the local fallback copy may be the one.
		default:
			return err
		}
	}
	// The local copy goes too. It is only a fallback, but a leftover one would still
	// answer `tun forward -n <name>` — that is the opposite of forgetting it.
	local, lerr := config.LoadTunnels()
	if lerr != nil {
		return lerr
	}
	if _, ok := local.Forwards[name]; ok {
		if err := config.DeleteTunnel(name); err != nil {
			return err
		}
		deleted = append(deleted, "local")
	}
	if len(deleted) == 0 {
		return fmt.Errorf("tunnel preset %q not found", name)
	}
	c.Printf("deleted preset %s (%s)\n", name, strings.Join(deleted, " and "))
	return nil
}

// runTunnelPresetsPush uploads the local preset file to the server (TUN-03's migration
// path: presets used to live only on the machine that saved them). A name the server
// already has is SKIPPED and listed — overwriting a preset another machine may be using
// is a decision for `--force`, not a side effect of a migration.
func runTunnelPresetsPush(c *gcli.Command, _ []string) error {
	cli, err := tunnelClient()
	if err != nil {
		return err
	}
	local, err := config.LoadTunnels()
	if err != nil {
		return err
	}
	if len(local.Forwards) == 0 {
		c.Println("no local presets to upload")
		return nil
	}
	names := make([]string, 0, len(local.Forwards))
	for name := range local.Forwards {
		names = append(names, name)
	}
	sort.Strings(names)
	pushed, skipped := 0, make([]string, 0)
	for _, name := range names {
		p := local.Forwards[name]
		_, err := cli.PutTunnelPreset(name, client.TunnelPreset{Worker: p.Worker, Specs: p.Specs, Note: p.Note}, tunnelOpts.force)
		if err != nil {
			if client.StatusOf(err) == http.StatusConflict {
				skipped = append(skipped, name)
				continue
			}
			return fmt.Errorf("upload preset %q: %w", name, err)
		}
		pushed++
	}
	c.Printf("pushed %d preset(s)\n", pushed)
	if len(skipped) > 0 {
		c.Printf("skipped %d already on the server (use --force to overwrite): %s\n",
			len(skipped), strings.Join(skipped, " "))
	}
	return nil
}

func runTunnelList(c *gcli.Command, _ []string) error {
	cli, e := tunnelClient()
	if e != nil {
		return e
	}
	fws, e := cli.ListTunnelForwarders()
	if e != nil {
		return e
	}
	ts, e := cli.ListTunnels()
	if e != nil {
		return e
	}
	// Two sections on purpose: "no forwarder is running" and "nothing is connected" are
	// different diagnoses, and the first one used to be invisible here.
	c.Println("FORWARDERS")
	if len(fws) == 0 {
		c.Println("no forward processes registered")
	} else {
		c.Println("ID WORKER RULES HOST PID AGE CONNS UP DOWN")
		for _, f := range fws {
			rules := make([]string, 0, len(f.Specs))
			for _, sp := range f.Specs {
				rules = append(rules, sp.Display())
			}
			c.Printf("%s %s %s %s %d %s %d %d %d\n", f.ID, f.Worker, strings.Join(rules, ";"),
				f.Host, f.PID, time.Since(f.StartedAt).Round(time.Second), f.Connections, f.BytesUp, f.BytesDown)
		}
	}
	c.Println("CONNECTIONS")
	if len(ts) == 0 {
		c.Println("no active tunnels")
		return nil
	}
	c.Println("ID CALLER WORKER TARGET CLIENT AGE UP DOWN")
	for _, t := range ts {
		c.Printf("%s %s %s %s %s %s %d %d\n", t.ID, t.CallerID, t.WorkerID, t.Target, t.ClientRemote, time.Since(t.StartedAt).Round(time.Second), t.BytesUp, t.BytesDown)
	}
	return nil
}

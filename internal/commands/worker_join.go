package commands

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/wsproto"
)

// `gofer worker join` (worker self-enrollment): this machine asks a server to admit it,
// a person with worker.manage types the pairing code printed here into the console, and
// the command then collects the token and writes worker.yaml + .env exactly the way
// `worker init` does.
//
// Everything up to the token travels the four ANONYMOUS enrollment routes: the machine
// joining holds no gofer credential at all, which is the point of the flow (it never
// presents a config token, and it never sees the server's admin token either).

// workerJoinFlags holds `gofer worker join` flags.
type workerJoinFlags struct {
	server    string
	id        string
	labels    gcli.Strings
	roots     gcli.Strings
	workspace string
	start     bool
	yes       bool
	force     bool
	timeout   string
}

var workerJoinOpts = workerJoinFlags{}

// joinPollWait is one long poll's wait. The server caps a single poll at 25 s, below
// the client's 30 s default timeout, so a poll always ends on its own.
const joinPollWait = 25 * time.Second

// joinRetryMin / joinRetryMax bound the backoff between polls after a transport
// failure: a server restarting mid-flow must not fail the join, because the request
// lives on the server until its deadline and this machine is still the only one that
// can collect the token.
const (
	joinRetryMin = time.Second
	joinRetryMax = 15 * time.Second
)

// workerJoinInterrupts hands back the channel a Ctrl-C (os.Interrupt) arrives on plus
// its stop func. A var so tests can drive the give-up path without raising a real
// signal (G053).
var workerJoinInterrupts = func() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	return ch, func() { signal.Stop(ch) }
}

// joinPollResult carries one long poll's outcome out of its goroutine, so the loop can
// watch the interrupt channel while the request is in flight.
type joinPollResult struct {
	poll client.EnrollPoll
	err  error
}

// NewWorkerJoinCmd builds `gofer worker join`: ask a server to admit this machine. The
// command shows a pairing code, waits (long-polling) while a person accepts it in the
// console, then writes the token and worker.yaml here and optionally starts the worker.
// It never starts a worker unless --start is given, and it never prints the token.
func NewWorkerJoinCmd(info buildinfo.Info) *gcli.Command {
	return &gcli.Command{
		Name: "join",
		Desc: "Ask a server to admit this machine as a worker: it prints a pairing code a person accepts in the console, then writes the token and worker.yaml here",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c) // G011: one shared -c/--config binding
			c.StrOpt(&workerJoinOpts.server, "server", "s", "", "hub address to join, e.g. http://<server-ip>:8767 (required)")
			c.StrOpt(&workerJoinOpts.id, "id", "", "", "worker id to propose (the accepting person may choose another one)")
			c.VarOpt(&workerJoinOpts.labels, "labels", "", "worker label to propose (repeatable, or comma separated)")
			c.VarOpt(&workerJoinOpts.roots, "roots", "", "explicit roots mapping from=to (repeatable)")
			c.StrOpt(&workerJoinOpts.workspace, "workspace", "", "", "directory to create as the default workspace (default: $GOFER_WORKSPACE, else ~/.gofer/workspace)")
			c.BoolOpt(&workerJoinOpts.start, "start", "", false, "start the worker in the background once the config is written (Windows: prints the foreground command instead)")
			c.BoolOpt(&workerJoinOpts.yes, "yes", "y", false, "non-interactive: accept inferred values without prompting")
			c.BoolOpt(&workerJoinOpts.force, "force", "f", false, "overwrite an existing worker.yaml (the old file is backed up to worker.yaml.bak-<time>)")
			c.StrOpt(&workerJoinOpts.timeout, "timeout", "", "10s", "per-check timeout for the doctor run, e.g. 10s")
		},
		Func: func(c *gcli.Command, _ []string) error { return runWorkerJoin(c, info) },
	}
}

// runWorkerJoin performs the join: local pre-checks, the request, the wait, then the
// token hand-off into the `worker init` flow. Every network call before the accept is
// anonymous; the only writes are the generated .env / worker.yaml under the config dir.
func runWorkerJoin(c *gcli.Command, info buildinfo.Info) error {
	server := strings.TrimSpace(workerJoinOpts.server)
	if server == "" {
		return errorx.Failf(configExitErr, "--server is required (the hub address, e.g. http://<server-ip>:8767)")
	}
	requestedID := strings.TrimSpace(workerJoinOpts.id)
	if requestedID != "" {
		if err := config.CheckWorkerID(requestedID); err != nil {
			return errorx.Failf(configExitErr, "--id: %v", err)
		}
	}
	roots, err := joinRootFlags()
	if err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}
	cfgDir, err := config.ConfigDir()
	if err != nil {
		return errorx.Failf(configExitErr, "resolve config dir: %v", err)
	}
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return errorx.Failf(configExitErr, "create config dir %s: %v", cfgDir, err)
	}
	workerPath := filepath.Join(cfgDir, config.WorkerConfigFileName)
	envPath := filepath.Join(cfgDir, config.EnvFileName)
	// Refuse a real worker.yaml BEFORE asking the server for anything: a request that
	// would immediately be overwritten only puts a pending row in front of a person.
	if _, statErr := os.Stat(workerPath); statErr == nil && !workerJoinOpts.force {
		return errorx.Failf(configExitErr, "%s already exists; use --force to overwrite (the old file is backed up)", workerPath)
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return errorx.Failf(configExitErr, "stat %s: %v", workerPath, statErr)
	}

	cli, err := newClient(config.InputCfgFile, server, "")
	if err != nil {
		return err
	}
	warnIfInsecureJoinServer(c, server)
	secret, hash, err := client.NewEnrollSecret()
	if err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}
	ticket, err := cli.Enroll(client.EnrollRequest{
		RequestedID: requestedID,
		Labels:      joinLabels(workerJoinOpts.labels),
		Host:        joinHostInfo(info, roots),
		SecretHash:  hash,
	})
	if err != nil {
		if joinEnrollmentDisabled(err) {
			return errorx.Failf(configExitErr, "self-enrollment is not enabled on this server (server.worker_enrollment.enabled is false); ask an admin to enable it, then re-run this command")
		}
		return errorx.Failf(configExitErr, "enrollment request failed: %v", err)
	}
	printJoinTicket(c, ticket)

	deadline := time.Unix(ticket.DecideUntil, 0)
	poll, giveUp, err := waitJoinDecision(c, cli, ticket.EnrollID, secret, deadline)
	if err != nil {
		return err
	}
	if giveUp {
		// Ctrl-C: hand the request back so it does not sit in front of a person for the
		// rest of the decide window. Best effort — an unreachable server still expires it.
		_ = cli.CancelEnroll(ticket.EnrollID, secret)
		return errorx.Failf(configExitErr, "enrollment request %s abandoned; run gofer worker join again to re-join", ticket.EnrollID)
	}
	switch poll.State {
	case jobstore.EnrollAccepted:
		return collectJoinedWorker(c, info, cli, ticket.EnrollID, secret, poll, server, envPath)
	case jobstore.EnrollRejected:
		return errorx.Failf(configExitErr, "enrollment request rejected: %s", joinReason(poll.Reason))
	case jobstore.EnrollExpired:
		return errorx.Failf(configExitErr, "enrollment request expired (nobody decided, or it was voided); run gofer worker join again to re-join")
	case jobstore.EnrollSuperseded:
		return errorx.Failf(configExitErr, "this request was superseded by a newer one from the same source; use the pairing code shown by the latest command")
	case jobstore.EnrollCancelled:
		return errorx.Failf(configExitErr, "enrollment request was cancelled; run gofer worker join again to re-join")
	case jobstore.EnrollCollected:
		return errorx.Failf(configExitErr, "the token for this request was already collected (worker_id=%s, possibly by an earlier run); run gofer worker join again to re-join", poll.WorkerID)
	default:
		return errorx.Failf(configExitErr, "unknown enrollment result: state=%q %s", poll.State, joinReason(poll.Reason))
	}
}

// collectJoinedWorker handles the accepted request: the token goes to disk FIRST, the
// confirmation follows immediately, and only then does the `worker init` flow write
// worker.yaml and run the doctor.
//
// The order matters: the collect window is 60 s (design「默认数值」) and the doctor
// probes the hub, which can take longer. Confirming only after the doctor would let the
// server revoke a registration whose credential is already safe on this host.
func collectJoinedWorker(c *gcli.Command, info buildinfo.Info, cli *client.Client, enrollID, secret string,
	poll client.EnrollPoll, server, envPath string) error {
	id := strings.TrimSpace(poll.WorkerID)
	token := strings.TrimSpace(poll.WorkerToken)
	if id == "" || token == "" {
		return errorx.Failf(configExitErr, "server accepted the request but returned no token (worker_id=%q); ask an admin to remove that worker, then re-run gofer worker join", id)
	}
	if err := upsertEnvFile(envPath, workerTokenEnv, token); err != nil {
		return errorx.Failf(configExitErr, "write token to %s: %v", envPath, err)
	}
	c.Printf("✓ request accepted: worker_id=%s, token written to %s\n", id, envPath)
	if err := cli.ConfirmEnroll(enrollID, secret); err != nil {
		c.Printf("warning: confirming collection failed (%v) -- the server confirms on the worker's first connection to the hub\n", err)
	}

	// From here on this is `worker init` with a token in hand. workerInitOpts is filled
	// the same way `gofer init worker` fills it from its own flags
	// (runInitDelegatedWorker): save the old value, set the globals, restore on return.
	prev := workerInitOpts
	defer func() { workerInitOpts = prev }()
	workerInitOpts = workerInitFlags{
		server:    server,
		token:     token,
		id:        id, // the id the ACCEPTING person chose, not the proposed one
		roots:     workerJoinOpts.roots,
		yes:       workerJoinOpts.yes,
		force:     workerJoinOpts.force,
		workspace: workerJoinOpts.workspace,
		timeout:   workerJoinOpts.timeout,
	}
	if err := runWorkerInit(c, info); err != nil {
		// The registration and the token are already in place, and the request is
		// confirmed — so this is a fixable local problem, not a lost registration.
		c.Printf("worker registered as %s, token saved in %s; fix the problem, then run gofer worker init --server %s --id %s --yes to continue\n",
			id, envPath, server, id)
		return errorx.Failf(configExitErr, "join: config written / checks not completed: %v", err)
	}
	if workerJoinOpts.start {
		return startJoinedWorker(c, id)
	}
	return nil
}

// waitJoinDecision long-polls until the server decides, the operator presses Ctrl-C
// (giveUp=true) or the decide window runs out. A transport failure or a server restart
// backs off and retries instead of failing: the request keeps living on the server until
// its deadline. A definite server verdict (404 / 409) is terminal.
func waitJoinDecision(c *gcli.Command, cli *client.Client, enrollID, secret string, deadline time.Time) (client.EnrollPoll, bool, error) {
	interrupts, stop := workerJoinInterrupts()
	defer stop()
	var retry time.Duration
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return client.EnrollPoll{}, false, errorx.Failf(configExitErr,
				"nobody accepted by %s; the request expired, run gofer worker join again to re-join", formatJoinTime(deadline))
		}
		// The poll runs in its own goroutine so Ctrl-C is honoured during the wait; the
		// channel is buffered, so an abandoned poll exits without leaking a writer.
		results := make(chan joinPollResult, 1)
		go func() {
			poll, err := cli.PollEnroll(enrollID, secret, min(joinPollWait, left))
			results <- joinPollResult{poll: poll, err: err}
		}()
		select {
		case <-interrupts:
			return client.EnrollPoll{}, true, nil
		case r := <-results:
			if r.err == nil {
				retry = 0
				if r.poll.State == jobstore.EnrollPending {
					c.Printf("waiting for an admin to accept... (%s left)\n", formatJoinDuration(time.Until(deadline)))
					continue
				}
				return r.poll, false, nil
			}
			if joinEnrollmentDisabled(r.err) {
				return client.EnrollPoll{}, false, errorx.Failf(configExitErr,
					"self-enrollment is not enabled on this server (server.worker_enrollment.enabled is false); this request is void, ask an admin to enable it, then re-run this command")
			}
			if code := client.StatusOf(r.err); code == http.StatusNotFound || code == http.StatusConflict {
				return client.EnrollPoll{}, false, errorx.Failf(configExitErr, "query request status: %v", r.err)
			}
			retry = nextJoinRetry(retry)
			c.Printf("connection to the server failed for now (%v); retrying in %s (the request is still waiting)\n", r.err, retry)
			select {
			case <-interrupts:
				return client.EnrollPoll{}, true, nil
			case <-time.After(retry):
			}
		}
	}
}

// nextJoinRetry grows the retry delay from joinRetryMin up to joinRetryMax (doubling).
func nextJoinRetry(prev time.Duration) time.Duration {
	if prev <= 0 {
		return joinRetryMin
	}
	if next := prev * 2; next < joinRetryMax {
		return next
	}
	return joinRetryMax
}

// joinRootFlags parses the roots this join proposes. join never infers roots: the
// projects are picked by the accepting person, so at request time there is nothing to
// infer from. The same flag value is handed to `worker init` after the accept.
func joinRootFlags() ([]config.WorkerRoot, error) {
	if len(workerJoinOpts.roots) == 0 {
		return nil, nil
	}
	return parseWorkerRootFlags(workerJoinOpts.roots)
}

// joinRootStrings renders the explicit roots for the request's host block (display
// only — roots stay a local capability, the server never sends them back).
func joinRootStrings(roots []config.WorkerRoot) []string {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		out = append(out, r.From+"="+r.To)
	}
	return out
}

// joinLabels flattens the repeated / comma-separated --labels values.
func joinLabels(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		for _, v := range strings.Split(item, ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// joinHostInfo describes this machine for the admin list: hostname, os / arch, gofer and
// protocol version, the agent CLIs detected here, and the roots the operator named
// explicitly (display only).
func joinHostInfo(info buildinfo.Info, roots []config.WorkerRoot) client.EnrollHost {
	hostname, _ := os.Hostname()
	agentKeys, _ := detectedAgents(workerInitDetector)
	return client.EnrollHost{
		Hostname:        hostname,
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		GoferVersion:    info.DisplayVersion(),
		ProtocolVersion: strconv.Itoa(wsproto.CurrentProtocolVersion),
		Agents:          agentKeys,
		Roots:           joinRootStrings(roots),
	}
}

// printJoinTicket shows the pairing code, the deadline and where to accept. The code is
// the ONLY proof that the accepted request is this machine's (an impostor's request
// shows its own code on the attacker's terminal), so it gets a block of its own and is
// split 4+4 for character-by-character comparison.
func printJoinTicket(c *gcli.Command, ticket client.EnrollTicket) {
	until := time.Unix(ticket.DecideUntil, 0)
	c.Printf("\n  ======================== worker join ========================\n")
	c.Printf("   pairing code:      %s\n", joinPairingDisplay(ticket.PairingCode))
	c.Printf("   decide before:     %s (%s left)\n", formatJoinTime(until), formatJoinDuration(time.Until(until)))
	c.Printf("  ============================================================\n")
	c.Printf("  Enter this pairing code in the server console -> Runners -> Pending, then accept\n\n")
}

// joinPairingDisplay splits an 8-character pairing code into the 4+4 form the console
// shows; anything unexpected is shown unchanged.
func joinPairingDisplay(code string) string {
	code = strings.TrimSpace(code)
	if len(code) != 8 {
		return code
	}
	return code[:4] + "-" + code[4:]
}

// formatJoinTime renders an absolute time in local time to the second.
func formatJoinTime(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") }

// formatJoinDuration renders a remaining time at second granularity (never negative).
func formatJoinDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Round(time.Second).String()
}

// joinReason renders the server's reason for a terminal state, so no error line ends in
// a bare colon.
func joinReason(reason string) string {
	if r := strings.TrimSpace(reason); r != "" {
		return r
	}
	return "(no reason given by the server)"
}

// joinEnrollmentDisabled reports the server's "feature off" answer. The anonymous routes
// answer 404 with this detail when server.worker_enrollment.enabled is false, which is a
// different situation for the operator than an unknown request.
func joinEnrollmentDisabled(err error) bool {
	return err != nil && strings.Contains(err.Error(), "worker enrollment is disabled on this server")
}

// warnIfInsecureJoinServer warns when the token will cross a plain-http link to a host
// that is not this machine (the same exposure `worker add` has, but here the token is
// handed over on that very connection).
func warnIfInsecureJoinServer(c *gcli.Command, server string) {
	if !joinServerInsecure(server) {
		return
	}
	c.Printf("warning: %s is not https -- the worker token will cross the network in plaintext; use https across networks\n", strings.TrimSpace(server))
}

// joinServerInsecure reports whether addr is a plain-http address outside the loopback
// interface. The address is normalised first (a bare `host:port` is http), exactly like
// the client normalises it, so the warning matches the connection that is really made.
func joinServerInsecure(addr string) bool {
	raw := client.NormalizeBaseURL(addr)
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "http") {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	return !isLoopbackHost(host)
}

// isLoopbackHost reports whether host is an IP on the loopback interface. A hostname
// other than "localhost" counts as remote: whether it resolves back to this machine is
// not knowable here, and warning about it costs nothing.
func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// startJoinedWorker starts the freshly configured worker the way `gofer worker -d` does:
// a detached child of this binary runs `worker -d`, so the pidfile, log file and
// duplicate-start refusal are the ones the daemon path owns. The child is started with
// the CURRENT argv replaced (not this command's), which is why it re-execs explicitly
// instead of calling daemon.Spawn.
func startJoinedWorker(c *gcli.Command, id string) error {
	if runtime.GOOS == "windows" {
		// Windows has no -d here (G013): print the foreground command instead of
		// pretending to have started a daemon.
		c.Printf("background start (-d) is not supported on Windows: run `gofer worker` in the foreground (or as a login task); stop with `gofer worker stop %s`\n", id)
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return errorx.Failf(configExitErr, "resolve the gofer executable: %v", err)
	}
	cmd, err := daemon.StartDetached(exe, []string{"worker", "-d"}, os.Environ(), workerOutFile(id))
	if err != nil {
		return errorx.Failf(configExitErr, "start worker in background: %v", err)
	}
	// The intermediate process exits as soon as it has spawned the daemon; it is reaped
	// so it never lingers as a zombie.
	go func() { _ = cmd.Wait() }()
	c.Printf("worker %s started in background (log %s; stop with gofer worker stop %s)\n", id, workerLogFile(id), id)
	return nil
}

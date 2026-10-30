package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/daemon"
	ptyrunner "github.com/inhere/gofer/internal/runner/pty"
	"github.com/inhere/gofer/internal/worker"
	"github.com/inhere/gofer/internal/wsproto"
)

// `gofer worker doctor` (CFG-09) answers the pre-flight question "can THIS
// machine be a working worker?" in one table: is the config readable, does the
// hub host resolve and accept a TCP connection, does the token resolve, do the
// roots exist, which agents are actually installed, and does the hub ACCEPT this
// worker_id's register frame — the four failures that otherwise only show up as a
// disconnected worker in `gofer worker list` (and a stack of log lines).
//
// Rows are PASS / WARN / FAIL: any FAIL exits 1 (the worker is not ready), WARNs
// exit 0 (it works, but something is worth a look).

// workerDoctorExitErr is the exit code for "at least one FAIL" (design §三.1:
// 任一 FAIL → 1, only WARN → 0).
const workerDoctorExitErr = 1

// workerDoctorOpts holds the `worker doctor` flags. --worker-config is its own
// flag (not the app-level -c) exactly like the rest of the `worker` group: the
// doctor inspects a WORKER config, which has different semantics from config.yaml
// (D-A1).
// lookupHost resolves the hub host. A variable so a test can pin the answer:
// a fake-ip DNS (TUN proxy) resolves even a .invalid name, which would make the
// unresolvable-host case depend on the machine running it.
var lookupHost = net.LookupHost

var workerDoctorOpts = struct {
	config  string
	timeout string
	connect bool
	json    bool
}{}

// workerDoctorStatus is one row's verdict.
type workerDoctorStatus string

const (
	doctorPass workerDoctorStatus = "PASS"
	doctorWarn workerDoctorStatus = "WARN"
	doctorFail workerDoctorStatus = "FAIL"
)

// workerDoctorRow is one check's line in the table (and one element of the --json
// document, so a script reads exactly what the operator sees).
type workerDoctorRow struct {
	Check  string             `json:"check"`
	Status workerDoctorStatus `json:"status"`
	Detail string             `json:"detail"`
}

// workerDoctorReport is the whole result; Failures/Warnings are precomputed so a
// caller (or a script reading --json) never re-counts the rows.
type workerDoctorReport struct {
	Config       string            `json:"config"`
	WorkerID     string            `json:"worker_id,omitempty"`
	GoferVersion string            `json:"gofer_version,omitempty"`
	Rows         []workerDoctorRow `json:"checks"`
	Failures     int               `json:"failures"`
	Warnings     int               `json:"warnings"`
}

func (r *workerDoctorReport) add(status workerDoctorStatus, check, detail string) {
	r.Rows = append(r.Rows, workerDoctorRow{Check: check, Status: status, Detail: detail})
	switch status {
	case doctorFail:
		r.Failures++
	case doctorWarn:
		r.Warnings++
	}
}

// addRow appends one row produced by a check helper.
func (r *workerDoctorReport) addRow(row workerDoctorRow) {
	r.add(row.Status, row.Check, row.Detail)
}

// extend appends a batch of rows produced by a helper (the roots walk).
func (r *workerDoctorReport) extend(rows []workerDoctorRow) {
	for _, row := range rows {
		r.addRow(row)
	}
}

// NewWorkerDoctorCmd builds `gofer worker doctor`: a read-only self-check of this
// machine's worker.yaml (CFG-09). It NEVER starts a worker and never writes
// config; the only outbound action is the optional register probe (--connect),
// which is skipped while this worker_id is running locally (see doctorConnectRow).
func NewWorkerDoctorCmd(info buildinfo.Info) *gcli.Command {
	return &gcli.Command{
		Name: "doctor",
		Desc: "Self-check this worker: config, hub urls, token, roots, agents, register handshake",
		Config: func(c *gcli.Command) {
			c.StrOpt(&workerDoctorOpts.config, "worker-config", "", "", "path to the worker config file (default: <config-dir>/worker.yaml)")
			c.StrOpt(&workerDoctorOpts.timeout, "timeout", "", "10s", "per-check timeout, e.g. 10s (host lookup / TCP dial / register handshake)")
			c.BoolOpt(&workerDoctorOpts.connect, "connect", "", true, "run the hub register handshake (skipped automatically while this worker_id runs locally)")
			c.BoolOpt(&workerDoctorOpts.json, "json", "", false, "machine-readable JSON instead of the table")
		},
		Func: func(c *gcli.Command, _ []string) error {
			rep, err := runWorkerDoctor(c, info)
			if err == nil || rep == nil || rep.Failures == 0 {
				return err // usage/config errors keep gcli's normal rendering
			}
			if workerDoctorOpts.json {
				// In --json mode the document IS the output, and it already carries
				// the verdict (failures/warnings) together with the exit code. gcli
				// renders a returned error onto STDOUT (color.Error.Tips), which would
				// append a non-JSON line and break every consumer — so exit directly,
				// the same os.Exit(code) pattern `job run` uses to carry a status
				// through the CLI layer.
				os.Exit(workerDoctorExitErr)
			}
			return err
		},
	}
}

// runWorkerDoctor builds the report, prints it and maps it onto an exit code. The
// report is returned even on failure so a caller (a test) can assert on the rows
// behind the exit code.
func runWorkerDoctor(c *gcli.Command, info buildinfo.Info) (*workerDoctorReport, error) {
	timeout, err := workerDoctorTimeout()
	if err != nil {
		return nil, err
	}
	path := workerDoctorOpts.config
	if path == "" {
		def, derr := config.UserWorkerConfigPath()
		if derr != nil {
			return nil, errorx.Failf(workerDoctorExitErr, "resolve default worker config path: %v", derr)
		}
		path = def
	}
	rep := buildWorkerDoctorReport(path, timeout, info, agent.DefaultDetector(), workerDoctorOpts.connect)
	out, rerr := renderWorkerDoctor(rep, workerDoctorOpts.json)
	if rerr != nil {
		return rep, rerr
	}
	c.Printf("%s", out)
	if rep.Failures > 0 {
		return rep, errorx.Failf(workerDoctorExitErr, "worker doctor: %d check(s) failed", rep.Failures)
	}
	return rep, nil
}

// workerDoctorTimeout parses --timeout (a duration string, default 10s).
func workerDoctorTimeout() (time.Duration, error) {
	raw := strings.TrimSpace(workerDoctorOpts.timeout)
	if raw == "" {
		return 10 * time.Second, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid --timeout %q: %w", raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid --timeout %q: must be positive", raw)
	}
	return d, nil
}

// buildWorkerDoctorReport runs every check in order and returns the rows. It stops
// at an unreadable config: every later check needs the decoded config, so nothing
// after it could say anything true.
func buildWorkerDoctorReport(path string, timeout time.Duration, info buildinfo.Info, det agent.Detector, connect bool) *workerDoctorReport {
	rep := &workerDoctorReport{Config: path, GoferVersion: info.DisplayVersion()}
	wc, err := loadWorkerConfig(path)
	if err != nil {
		rep.add(doctorFail, "config", err.Error())
		return rep
	}
	rep.add(doctorPass, "config", path)
	rep.WorkerID = wc.WorkerID

	// 1) identity + hub addresses.
	if wc.WorkerID == "" {
		rep.add(doctorFail, "worker_id", cliText("cli.worker.doctor.no_worker_id"))
	} else {
		rep.add(doctorPass, "worker_id", wc.WorkerID)
	}
	if len(wc.ServerLink.URLs) == 0 {
		rep.add(doctorFail, "url", cliText("cli.worker.doctor.no_urls"))
	}
	for _, raw := range wc.ServerLink.URLs {
		rep.addRow(doctorURLRow(raw, timeout))
	}

	// 2) token (never printed — only where it came from, and whether it is empty).
	rep.addRow(doctorTokenRow(wc))

	// 3) project-sourcing mode + roots. The mode decides what "a runnable worker"
	// even means: POLICY maps server-pushed projects onto roots, LEGACY runs its own
	// projects (deprecated), EMPTY can run nothing.
	mode := workerModeOf(wc)
	// initialWorkerConfig is the SAME startup projection runWorker uses, so a POLICY
	// worker's "current" project set here is the one it would register with (from the
	// last-known-good cache) rather than a raw count off disk.
	cfg, projects, _ := initialWorkerConfig(mode, wc, workerPolicyCachePath(wc.WorkerID))
	switch mode {
	case modePolicy:
		rep.add(doctorPass, "mode", cliText("cli.worker.doctor.mode_policy", "roots", len(wc.Roots), "projects", len(projects)))
		rep.extend(doctorRootRows(wc))
	case modeLegacy:
		rep.add(doctorWarn, "mode", cliText("cli.worker.doctor.mode_legacy", "projects", len(wc.Projects), "doc", migrationDoc))
	default:
		rep.add(doctorFail, "mode", cliText("cli.worker.doctor.mode_empty"))
	}

	// 4) guards / concurrency summary, then what this host can actually run.
	rep.addRow(doctorGuardsRow(wc))
	rep.addRow(doctorMaxConcurrentRow(wc))
	rep.addRow(doctorToolsRow(wc))
	if det == nil {
		det = agent.DefaultDetector()
	}
	resolved, detected := agent.Resolve(cfg, det)
	for _, key := range resolvedAgentKeys(resolved) {
		rep.addRow(doctorAgentRow(key, resolved.Agents[key], detected[key]))
	}

	// 5) the register handshake, unless the operator turned it off.
	if !connect {
		rep.add(doctorWarn, "connect", cliText("cli.worker.doctor.connect_disabled"))
	} else {
		rep.addRow(doctorConnectRow(wc, cfg, detected, projects, timeout, info))
	}
	return rep
}

// doctorURLRow checks one server_link url: scheme, host resolution and TCP
// reachability. The TCP dial (not a full websocket handshake — that is the
// --connect step) is what separates "the name resolves but nothing listens" from
// "the hub is up"; TLS is deliberately not exercised here, so a wss url whose
// certificate is broken still passes this row and fails the register probe.
func doctorURLRow(raw string, timeout time.Duration) workerDoctorRow {
	row := workerDoctorRow{Check: "url", Status: doctorPass, Detail: raw}
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil {
		row.Status, row.Detail = doctorFail, cliText("cli.worker.doctor.url_parse_failed", "url", trimmed, "err", err)
		return row
	}
	// ws/wss is the documented form. http(s):// is accepted by the dialer
	// (wsDialURLs), so it is a WARN rather than a FAIL — it works, it is just not
	// what worker.yaml should say.
	switch strings.ToLower(u.Scheme) {
	case "ws", "wss":
	case "http", "https":
		row.Status = doctorWarn
	case "":
		row.Status, row.Detail = doctorFail, cliText("cli.worker.doctor.url_no_scheme", "url", trimmed)
		return row
	default:
		row.Status, row.Detail = doctorFail, cliText("cli.worker.doctor.url_bad_scheme", "url", trimmed, "scheme", strconv.Quote(u.Scheme))
		return row
	}
	host := u.Hostname()
	if host == "" {
		row.Status, row.Detail = doctorFail, cliText("cli.worker.doctor.url_no_host", "url", trimmed)
		return row
	}
	if _, err := lookupHost(host); err != nil {
		// The single most common container failure: worker.yaml still names
		// host.docker.internal, which a Linux container cannot resolve. Say the IP
		// instead of leaving the operator with a bare DNS error.
		row.Status = doctorFail
		row.Detail = cliText("cli.worker.doctor.url_host_failed", "url", trimmed, "host", host, "err", err)
		return row
	}
	addr := net.JoinHostPort(host, portOrDefault(u))
	conn, derr := net.DialTimeout("tcp", addr, timeout)
	if derr != nil {
		row.Status, row.Detail = doctorFail, cliText("cli.worker.doctor.url_unreachable", "url", trimmed, "addr", addr, "err", derr)
		return row
	}
	_ = conn.Close()
	row.Detail = cliText("cli.worker.doctor.url_reachable", "url", trimmed, "addr", addr)
	return row
}

// portOrDefault resolves the port to dial, defaulting by scheme.
func portOrDefault(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "wss") || strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

// doctorTokenRow reports whether a hub credential resolves — the value is never
// printed (design §三.1: token_env 指向的环境变量非空，值不打印), only its source.
func doctorTokenRow(wc *config.WorkerConfig) workerDoctorRow {
	link := wc.ServerLink
	switch {
	case link.TokenEnv != "":
		if os.Getenv(link.TokenEnv) != "" {
			return workerDoctorRow{Check: "token", Status: doctorPass,
				Detail: cliText("cli.worker.doctor.token_from_env", "name", link.TokenEnv)}
		}
		return workerDoctorRow{Check: "token", Status: doctorFail,
			Detail: cliText("cli.worker.doctor.token_env_empty", "name", link.TokenEnv)}
	case link.Token != "":
		return workerDoctorRow{Check: "token", Status: doctorPass,
			Detail: cliText("cli.worker.doctor.token_inline")}
	default:
		return workerDoctorRow{Check: "token", Status: doctorWarn,
			Detail: cliText("cli.worker.doctor.token_unset")}
	}
}

// doctorRootRows checks each POLICY-mode root the same way `gofer config validate
// worker` does (to must be an existing, readable directory; from must be set and
// unique) — the doctor adds the readability probe, because a root that exists but
// cannot be listed still fails every dispatch from that tree.
func doctorRootRows(wc *config.WorkerConfig) []workerDoctorRow {
	rows := make([]workerDoctorRow, 0, len(wc.Roots))
	seen := make(map[string]bool, len(wc.Roots))
	for i, r := range wc.Roots {
		row := workerDoctorRow{Check: fmt.Sprintf("roots[%d]", i), Status: doctorPass}
		from := normRootPrefix(r.From)
		to := strings.TrimSpace(r.To)
		switch {
		case from == "":
			row.Status, row.Detail = doctorFail, cliText("cli.worker.doctor.root_from_empty", "from", r.From, "to", to)
		case to == "":
			row.Status, row.Detail = doctorFail, cliText("cli.worker.doctor.root_to_empty", "from", r.From)
		case seen[from]:
			row.Status, row.Detail = doctorFail, cliText("cli.worker.doctor.root_dup_from", "from", r.From)
		default:
			seen[from] = true
			fi, err := os.Stat(to)
			if err != nil || !fi.IsDir() {
				row.Status, row.Detail = doctorFail, cliText("cli.worker.doctor.root_to_missing", "from", r.From, "to", to)
				break
			}
			if err := dirReadable(to); err != nil {
				row.Status, row.Detail = doctorFail, cliText("cli.worker.doctor.root_to_unreadable", "from", r.From, "to", to, "err", err)
				break
			}
			row.Detail = fmt.Sprintf("%s -> %s", r.From, to)
		}
		rows = append(rows, row)
	}
	return rows
}

// dirReadable reports whether dir can actually be listed (open + one readdir), not
// merely stat'd: a root whose tree cannot be enumerated cannot serve a dispatch.
func dirReadable(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// doctorGuardsRow mirrors `config validate worker`'s guard check: an entirely
// unset guards block is the "do not tighten" default (every exec/interactive job
// runs), so it is a WARN — declaration is the recommended posture, not a
// requirement (T2-D).
func doctorGuardsRow(wc *config.WorkerConfig) workerDoctorRow {
	if wc.Guards.AllowExec == nil && wc.Guards.AllowInteractive == nil {
		return workerDoctorRow{Check: "guards", Status: doctorWarn,
			Detail: cliText("cli.worker.doctor.guards_unset")}
	}
	return workerDoctorRow{Check: "guards", Status: doctorPass,
		Detail: fmt.Sprintf("allow_exec=%s allow_interactive=%s", boolPtrStr(wc.Guards.AllowExec), boolPtrStr(wc.Guards.AllowInteractive))}
}

// doctorMaxConcurrentRow summarises the hub-side concurrency budget: an unset cap
// is legal but means the hub will not throttle this worker, so it is a WARN.
func doctorMaxConcurrentRow(wc *config.WorkerConfig) workerDoctorRow {
	if wc.MaxConcurrent > 0 {
		return workerDoctorRow{Check: "max_concurrent", Status: doctorPass,
			Detail: cliText("cli.worker.doctor.max_concurrent_set", "value", wc.MaxConcurrent)}
	}
	return workerDoctorRow{Check: "max_concurrent", Status: doctorWarn,
		Detail: cliText("cli.worker.doctor.max_concurrent_unset")}
}

// doctorToolsRow summarises the declared external tools (CFG-16): how many, and
// which executables this host cannot find. A miss is a WARN, never a FAIL — the
// list only informs the agent, and an absent tool fails nothing on its own.
func doctorToolsRow(wc *config.WorkerConfig) workerDoctorRow {
	if len(wc.Tools) == 0 {
		return workerDoctorRow{Check: "tools", Status: doctorPass,
			Detail: cliText("cli.worker.doctor.tools_none")}
	}
	if missing := config.MissingToolBins(wc.Tools); len(missing) > 0 {
		return workerDoctorRow{Check: "tools", Status: doctorWarn,
			Detail: cliText("cli.worker.doctor.tools_missing", "declared", len(wc.Tools), "missing", len(missing), "names", strings.Join(missing, ", "))}
	}
	return workerDoctorRow{Check: "tools", Status: doctorPass,
		Detail: cliText("cli.worker.doctor.tools_ok", "declared", len(wc.Tools))}
}

// doctorAgentRow reports one agent's availability. A declared agent whose CLI is
// NOT installed is a FAIL: the hub advertises it as runnable (availability is
// display-only, it never filters routing) and re-validates on dispatch, so every
// job routed to it dies at startup — exactly the failure a pre-flight check exists
// to catch.
func doctorAgentRow(key string, ac config.AgentConfig, res agent.DetectResult) workerDoctorRow {
	row := workerDoctorRow{Check: "agent." + key}
	if res.Available {
		row.Status = doctorPass
		row.Detail = "available"
		if res.Version != "" {
			row.Detail = "available version=" + res.Version
		}
		if res.Error != "" {
			row.Detail += " (" + res.Error + ")"
		}
		return row
	}
	row.Status = doctorFail
	row.Detail = "unavailable"
	if res.Error != "" {
		row.Detail += ": " + res.Error
	}
	if info, ok := agent.AgentInstallInfo(key, ac, runtime.GOOS); ok {
		if info.Command != "" {
			row.Detail += "; " + cliText("cli.worker.init.agent_missing_hint", "command", info.Command, "url", info.URL)
		} else {
			row.Detail += "; " + cliText("cli.worker.init.agent_missing_docs", "url", info.URL)
		}
	}
	return row
}

// doctorConnectRow runs the register handshake the design asks for (CFG-09 §三.1):
// dial each hub address with the worker's token, send the register frame this
// worker WOULD send, and report the hub's own verdict (accepted / protocol / or
// the rejection reason verbatim — a bad token binding is the #1 operator trap).
//
// SAFETY INTERLOCK: registering a worker_id that is already connected REPLACES the
// live connection on the hub (registry.Put + gracefulClose), which fails the old
// connection's in-flight jobs — a diagnostic must never kill a running worker's
// work. So while a `gofer worker -d` for this worker_id is alive on THIS machine,
// the probe is skipped with a WARN pointing at the log. (A worker running on
// ANOTHER machine cannot be seen from here; the runbook tells the operator to run
// the doctor on the worker's own host.)
//
// Inflight is left nil on purpose: nil means "cannot prove anything" to the hub, so
// recovering jobs stay with the recovery-window timer instead of being failed by a
// probe that knows nothing about them.
func doctorConnectRow(wc *config.WorkerConfig, cfg *config.Config, detected map[string]agent.DetectResult,
	projects []string, timeout time.Duration, info buildinfo.Info) workerDoctorRow {
	row := workerDoctorRow{Check: "connect"}
	if pid, running := localWorkerPID(wc.WorkerID); running {
		row.Status = doctorWarn
		row.Detail = cliText("cli.worker.doctor.connect_skipped", "id", wc.WorkerID, "pid", pid, "log", workerLogFile(wc.WorkerID))
		return row
	}
	urls := wsDialURLs(wc.ServerLink.URLs)
	if len(urls) == 0 {
		row.Status, row.Detail = doctorFail, cliText("cli.worker.doctor.connect_no_urls")
		return row
	}
	caps := workerCaps(wc, cfg, detected, projects)
	// The frame mirrors what this worker WOULD send on startup, so the hub applies
	// the same gates (token↔worker_id binding, protocol floor) and the operator sees
	// the same verdict in `worker list`. Hostname identifies the machine; StartedAt is
	// deliberately left out — a probe is not a worker process, and a bogus start time
	// would outlive the probe in the hub's snapshot.
	hostname, _ := os.Hostname()
	reg := wsproto.Register{
		WorkerID:        wc.WorkerID,
		ProtocolVersion: wsproto.CurrentProtocolVersion,
		PtyCapable:      ptyrunner.Available(),
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		Hostname:        hostname,
		GoferVersion:    info.DisplayVersion(),
		Labels:          caps.Labels,
		Projects:        caps.Projects,
		Agents:          caps.Agents,
		AgentCaps:       caps.AgentCaps,
		MaxConcurrent:   caps.MaxConc,
		Tools:           caps.Tools,
	}
	var failures []string
	for _, raw := range urls {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		ack, err := worker.Probe(ctx, raw, resolveWorkerToken(wc.ServerLink), reg)
		cancel()
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v%s", raw, err, dialFailureHint(err)))
			continue
		}
		if !ack.Accepted {
			row.Status = doctorFail
			row.Detail = cliText("cli.worker.doctor.connect_rejected", "url", raw, "reason", ack.Reason)
			return row
		}
		row.Status = doctorPass
		row.Detail = fmt.Sprintf("%s: accepted=true protocol=%d server_time=%d", raw, ack.ProtocolVersion, ack.ServerTime)
		return row
	}
	row.Status = doctorFail
	row.Detail = cliText("cli.worker.doctor.connect_failed", "failures", strings.Join(failures, "; "))
	return row
}

// dialFailureHint turns the one transport failure an operator cannot read off the
// error alone into something actionable: the hub rejects an unknown bearer token at
// the UPGRADE, so the websocket error only carries the status code (no body) and
// looks like a generic handshake failure.
func dialFailureHint(err error) string {
	if err != nil && strings.Contains(err.Error(), "401") {
		return cliText("cli.worker.doctor.dial_401_hint")
	}
	return ""
}

// localWorkerPID reports the pid of a running `gofer worker -d` for this
// worker_id on THIS machine (0 = none). It is the connect probe's safety
// interlock, so it errs towards "running": on Windows PIDAlive always reports
// false (daemon_windows.go), where an existing pidfile is the only signal — and a
// stale pidfile only costs a skipped probe (the WARN names it, and `gofer worker
// stop <id>` clears it), while the opposite error would replace a live worker's
// connection.
func localWorkerPID(id string) (int, bool) {
	if id == "" {
		return 0, false
	}
	pid, err := daemon.ReadPIDFile(workerPIDFile(id))
	if err != nil || pid <= 0 {
		return 0, false
	}
	if runtime.GOOS != "windows" && !daemon.PIDAlive(pid) {
		return 0, false
	}
	return pid, true
}

// renderWorkerDoctor renders the report as the operator table or as one JSON
// document (--json). Split out from the command so the exact output is testable
// without a gcli command in the way.
func renderWorkerDoctor(rep *workerDoctorReport, asJSON bool) (string, error) {
	if asJSON {
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return "", err
		}
		return string(b) + "\n", nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "worker doctor: %s\n", rep.Config)
	if rep.WorkerID != "" {
		fmt.Fprintf(&sb, "worker_id:     %s\n", rep.WorkerID)
	}
	if rep.GoferVersion != "" {
		fmt.Fprintf(&sb, "gofer:         %s (worker protocol %d)\n", rep.GoferVersion, wsproto.CurrentProtocolVersion)
	}
	width := 0
	for _, row := range rep.Rows {
		if n := len(row.Check); n > width {
			width = n
		}
	}
	for _, row := range rep.Rows {
		fmt.Fprintf(&sb, "%-4s  %-*s  %s\n", row.Status, width, row.Check, row.Detail)
	}
	fmt.Fprintf(&sb, "result: %s\n", doctorVerdict(rep))
	return sb.String(), nil
}

// doctorVerdict is the one-line summary printed under the table.
func doctorVerdict(rep *workerDoctorReport) string {
	switch {
	case rep.Failures > 0:
		return cliText("cli.worker.doctor.verdict_failed", "failures", rep.Failures, "warnings", rep.Warnings)
	case rep.Warnings > 0:
		return cliText("cli.worker.doctor.verdict_warn", "warnings", rep.Warnings)
	default:
		return cliText("cli.worker.doctor.verdict_ok")
	}
}

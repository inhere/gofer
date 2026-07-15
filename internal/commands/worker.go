package commands

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	yaml "github.com/goccy/go-yaml"
	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
	"github.com/inhere/gofer/internal/daemon"
	ptyrunner "github.com/inhere/gofer/internal/runner/pty"
	"github.com/inhere/gofer/internal/worker"
	"github.com/inhere/gofer/internal/wsproto"
)

// workerExitErr is the process exit code used when the worker fails to start or
// run (mirrors serveExitErr).
const workerExitErr = 2

// workerOpts holds the worker command flags. The worker config (worker.yaml)
// has a DIFFERENT semantics from the gofer server config, so it uses its own
// --worker-config flag (no -c short name) and does NOT read the app-level
// global -c (config.InputCfgFile) — D-A1.
var workerOpts = struct {
	config string
	daemon bool
}{}

// workerStopOpts holds `worker stop` flags. Its own --worker-config (separate
// from workerOpts so subcommand flag parsing stays isolated) lets `worker stop`
// resolve the default worker_id from worker.yaml when no <id> is given.
var workerStopOpts = struct {
	config string
}{}

// workerReloadOpts holds `worker reload` flags. It carries NO --worker-config: the
// reload is driven through the SERVER (the worker re-reads its own config file on
// its own host), so the only connection this command needs is the app -c/--config +
// --server/--token pair every other server-facing command uses.
var workerReloadOpts = struct {
	reason  string
	timeout int
}{}

// workerPIDFile / workerLogFile are the daemon-mode runtime files (c44),
// namespaced by worker id so multiple workers on one host never collide:
// <config-dir>/run/worker-<id>.{pid,log}.
func workerPIDFile(id string) string { return config.RuntimeFilePath("run", "worker-"+id+".pid") }
func workerLogFile(id string) string { return config.RuntimeFilePath("run", "worker-"+id+".log") }

// NewWorkerCmd builds the `worker` command: load worker.yaml, build the local
// job service (the worker runs jobs locally with its OWN config), dial the hub,
// register and run the dispatch loop (ws-worker §4/§6).
// info carries the linker build metadata down to the worker client, which reports
// it to the hub on register (gofer_version node info).
func NewWorkerCmd(info buildinfo.Info) *gcli.Command {
	return &gcli.Command{
		Name:    "worker",
		Desc:    "As worker that dials a central hub and executes dispatched jobs locally",
		Aliases: []string{"w"},
		Config: func(c *gcli.Command) {
			c.StrOpt(&workerOpts.config, "worker-config", "", "", "path to the worker config file (default: <config-dir>/worker.yaml)")
			c.BoolOpt(&workerOpts.daemon, "daemon", "d", false, "run in background (detached); logs to <config-dir>/run/worker-<id>.log")
		},
		Subs: []*gcli.Command{NewWorkerStopCmd(), NewWorkerReloadCmd()},
		Func: func(c *gcli.Command, args []string) error {
			return runWorker(c, args, info)
		},
	}
}

// NewWorkerStopCmd builds `gofer worker stop [<id>]`: stop the backgrounded (-d)
// worker via its id-namespaced pidfile (counterpart to `worker -d`). When <id> is
// omitted and exactly one worker is running it is auto-detected, so the common
// single-worker host can just run `gofer worker stop` (resolveDefaultWorkerID).
func NewWorkerStopCmd() *gcli.Command {
	return &gcli.Command{
		Name: "stop",
		Desc: "Stop the backgrounded (-d) worker via its pidfile (id auto-detected when only one is running)",
		Config: func(c *gcli.Command) {
			c.StrOpt(&workerStopOpts.config, "worker-config", "", "", "worker config to resolve the default worker_id when none is running (default: <config-dir>/worker.yaml)")
			c.AddArg("id", "worker id (optional; auto-detected when a single worker is running)", false)
		},
		Func: runWorkerStop,
	}
}

// NewWorkerReloadCmd builds `gofer worker reload <id>`: ask a CONNECTED worker to
// re-read its own worker.yaml, without restarting it (its running jobs keep going).
//
// Unlike `worker stop`, this is a SERVER-side operation — it goes over HTTP to the
// hub, which relays the request down the worker's live connection — so it runs
// wherever the CLI can reach the server, not necessarily on the worker's host.
// It waits for the worker's receipt and reports what the worker said: a config the
// worker refuses fails the command (non-zero exit) with the worker's own reason.
func NewWorkerReloadCmd() *gcli.Command {
	return &gcli.Command{
		Name:    "reload",
		Desc:    "Ask a connected worker to re-read its config (no restart); waits for its receipt",
		Aliases: []string{"rl"},
		Config: func(c *gcli.Command) {
			bindConfigFlag(c) // G011: one shared -c/--config binding
			bindServerFlags(c)
			c.StrOpt(&workerReloadOpts.reason, "reason", "", "", "why the reload was triggered (forwarded to the worker, logged)")
			c.IntOpt(&workerReloadOpts.timeout, "timeout", "", 0, "seconds to wait for the worker's receipt (0 = server default)")
			c.AddArg("id", "worker id (as registered in server.workers)", true)
		},
		Func: runWorkerReload,
	}
}

// runWorkerReload posts the reload and prints the outcome. On failure the error is
// surfaced VERBATIM (it carries the worker's own words — the bad key, the yaml line);
// summarising it would hide the only actionable part.
func runWorkerReload(c *gcli.Command, _ []string) error {
	id := ""
	if a := c.Arg("id"); a != nil {
		id = strings.TrimSpace(a.String())
	}
	if id == "" {
		return errorx.Failf(workerExitErr, "worker id is required: gofer worker reload <id>")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	wait := time.Duration(workerReloadOpts.timeout) * time.Second
	out, err := cli.ReloadWorker(id, workerReloadOpts.reason, wait)
	if err != nil {
		return errorx.Failf(workerExitErr, "worker %s reload failed: %v", id, err)
	}
	c.Printf("worker %s reloaded its config\n", id)
	if out.Caps != nil {
		c.Printf("  agents:         %s\n", strings.Join(out.Caps.Agents, ", "))
		c.Printf("  projects:       %s\n", strings.Join(out.Caps.Projects, ", "))
		if len(out.Caps.Labels) > 0 {
			c.Printf("  labels:         %s\n", strings.Join(out.Caps.Labels, ", "))
		}
		c.Printf("  max_concurrent: %d\n", out.Caps.MaxConcurrent)
	}
	return nil
}

func runWorkerStop(c *gcli.Command, _ []string) error {
	id := ""
	if a := c.Arg("id"); a != nil {
		id = a.String()
	}
	if id == "" {
		resolved, err := resolveDefaultWorkerID()
		if err != nil {
			return errorx.Failf(stopExitErr, "%v", err)
		}
		id = resolved
	}
	return stopDaemon(c, workerPIDFile(id), "worker-"+id)
}

// resolveDefaultWorkerID picks the worker to stop when no <id> is given, so a
// single-worker host can run a bare `gofer worker stop`:
//  1. exactly one worker currently running (live worker-*.pid) → that one;
//  2. several running → error listing them (ambiguous, the <id> is required);
//  3. none running → fall back to worker.yaml's worker_id, so stopDaemon can
//     report "not running" for the canonical worker (and clean a stale pidfile).
func resolveDefaultWorkerID() (string, error) {
	ids, err := runningWorkerIDs()
	if err != nil {
		return "", err
	}
	switch len(ids) {
	case 1:
		return ids[0], nil
	case 0:
		wc, cErr := loadWorkerConfig(workerStopOpts.config)
		if cErr != nil {
			return "", fmt.Errorf("no running worker; pass an <id> or provide a readable worker config: %v", cErr)
		}
		if wc.WorkerID == "" {
			return "", fmt.Errorf("no running worker and worker config has no worker_id; pass an <id>")
		}
		return wc.WorkerID, nil
	default:
		return "", fmt.Errorf("multiple workers running (%s); pass the <id> to stop one", strings.Join(ids, ", "))
	}
}

// runningWorkerIDs returns the ids of currently-alive workers by scanning the
// runtime dir for worker-<id>.pid files whose recorded pid is still alive. Stale
// pidfiles (dead pid) are skipped so they never create false ambiguity.
func runningWorkerIDs() ([]string, error) {
	// run dir = parent of any worker pidfile path.
	runDir := filepath.Dir(workerPIDFile("x"))
	matches, err := filepath.Glob(filepath.Join(runDir, "worker-*.pid"))
	if err != nil {
		return nil, fmt.Errorf("scan worker pidfiles: %w", err)
	}
	var ids []string
	for _, p := range matches {
		base := filepath.Base(p)
		id := strings.TrimSuffix(strings.TrimPrefix(base, "worker-"), ".pid")
		if id == "" {
			continue
		}
		pid, rErr := daemon.ReadPIDFile(p)
		if rErr != nil || !daemon.PIDAlive(pid) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func runWorker(c *gcli.Command, _ []string, info buildinfo.Info) error {
	wc, err := loadWorkerConfig(workerOpts.config)
	if err != nil {
		return errorx.Failf(workerExitErr, "%v", err)
	}
	if wc.WorkerID == "" {
		return errorx.Failf(workerExitErr, "worker config: worker_id is required")
	}
	if len(wc.ServerLink.URLs) == 0 {
		return errorx.Failf(workerExitErr, "worker config: server_link.urls is required")
	}

	// -d/--daemon: the parent re-execs itself detached, prints the child pid and
	// returns; the detached child re-enters runWorker with daemon.Daemonized()==true
	// and runs the dispatch loop below. worker.Serve already does SIGINT/SIGTERM
	// graceful shutdown, so the child only needs to clean up its pidfile on exit
	// (c44). The pidfile is namespaced by worker id (multiple workers per host).
	if workerOpts.daemon && !daemon.Daemonized() {
		pid, err := daemon.Spawn(daemon.Options{
			Name:    "worker-" + wc.WorkerID,
			PIDPath: workerPIDFile(wc.WorkerID),
			LogPath: workerLogFile(wc.WorkerID),
		})
		if err != nil {
			return errorx.Failf(workerExitErr, "%v", err)
		}
		c.Printf("gofer worker(%s) 已后台启动 pid=%d log=%s\n", wc.WorkerID, pid, workerLogFile(wc.WorkerID))
		return nil
	}
	if daemon.Daemonized() {
		defer daemon.RemovePIDFile(workerPIDFile(wc.WorkerID))
	}

	// Build the worker's LOCAL core (projects/agents/local runner/job.Service)
	// from its own config — this is what re-validates dispatched jobs (review #8).
	// wcfg is kept: the capability report below is resolved from the SAME snapshot,
	// so what the worker advertises is exactly what it will accept on dispatch.
	//
	// The detector is wrapped in a recorder rather than left to core.Build's default:
	// the caps report needs the availability/version the resolve pass produced, and the
	// only alternative — probing the host a second time from workerCaps — would break
	// "advertise exactly what was applied" (a CLI installed between the two passes would
	// be reported but not merged) on top of doubling the startup/reload cost.
	det := &availabilityRecorder{inner: agent.DefaultDetector()}
	// T5-A: the initial config depends on the mode. LEGACY keeps its local projects;
	// POLICY starts from the last-known-good cache (server-pushed projects survive a
	// restart) or empty until the first policy arrives; EMPTY runs nothing.
	mode := workerModeOf(wc)
	cachePath := workerPolicyCachePath(wc.WorkerID)
	wcfg, initialProjects, initialPolicy := initialWorkerConfig(mode, wc, cachePath)
	cr, err := core.Build(wcfg, core.WithAgentDetector(det))
	if err != nil {
		return errorx.Failf(workerExitErr, "%v", err)
	}
	defer func() { _ = cr.Close() }()

	rc := wc.ServerLink.Reconnect
	caps := workerCaps(wc, wcfg, det.snapshot(), initialProjects)
	policyCachePath := ""
	if mode == modePolicy {
		policyCachePath = cachePath // only POLICY workers persist a last-known-good cache
	}
	cl := worker.New(worker.Config{
		WorkerID:  wc.WorkerID,
		URLs:      wsDialURLs(wc.ServerLink.URLs),
		Token:     resolveWorkerToken(wc.ServerLink),
		Labels:    caps.Labels,
		Projects:  caps.Projects,
		Agents:    caps.Agents,
		AgentCaps: caps.AgentCaps,
		// Config hot-reload (SIGHUP / hub request) + policy apply: the command owns "how
		// to read worker.yaml / how to project a policy", the worker package owns
		// when/how a reload or policy is applied (G021).
		Reload:         newWorkerReloadFn(cr, det, workerOpts.config, wc.WorkerID),
		PolicyMode:     mode == modePolicy,
		CachePath:      policyCachePath,
		InitialPolicy:  initialPolicy,
		GoferVersion:   info.DisplayVersion(),
		MaxConc:        caps.MaxConc,
		InitialBackoff: msToDuration(rc.InitialBackoffMS),
		MaxBackoff:     msToDuration(rc.MaxBackoffMS),
		PingInterval:   secToDuration(rc.PingIntervalSec),
		ReadDeadline:   secToDuration(rc.ReadDeadlineSec),
	}, cr.Jobs)

	// Wire the worker Client as the pty session observer so an interactive local
	// job hands its pty output to the worker's pump (D-P2-3). Serve wires its own
	// observer for local browser attach; this worker observer is only for jobs
	// dispatched to a worker process. core.Build registers PtyRunner under
	// ptyrunner.Name only when a pty backend is available.
	if pr, ok := cr.Runners[ptyrunner.Name].(*ptyrunner.PtyRunner); ok {
		pr.SetObserver(cl)
	}

	// Run until SIGINT/SIGTERM; the signal/ctx start-stop orchestration lives in
	// internal/worker (D-B4), the command keeps only config loading + assembly.
	if err := worker.Serve(cl, wc); err != nil {
		return errorx.Failf(workerExitErr, "%v", err)
	}
	return nil
}

// newWorkerReloadFn builds the worker's ReloadFunc: re-read worker.yaml from the
// SAME path the process started from, rebuild the config, apply it to the running
// core and report the capabilities of the config it applied.
//
// Order is the whole point (worker.ReloadFunc contract): read + decode + validate
// FIRST, and only then apply. Every failure returns before core.ReloadWith is
// reached, so a broken worker.yaml leaves the worker running its previous config
// instead of half-applying a bad one — the apply itself cannot fail and therefore
// cannot roll back. The caps are derived from the very config that was applied, so
// what the worker advertises is exactly what it will accept on dispatch.
//
// Reload scope mirrors core.ReloadWith: projects / agents / labels / max_concurrent
// take effect; process-level facts (worker id, hub urls/token, storage db path) are
// frozen at startup and need a restart.
func newWorkerReloadFn(cr *core.Core, det *availabilityRecorder, path, workerID string) worker.ReloadFunc {
	return func(p *wsproto.Policy) (worker.ReloadOutcome, error) {
		wc, err := loadWorkerConfig(path)
		if err != nil {
			return worker.ReloadOutcome{}, err
		}
		if wc.WorkerID != workerID {
			return worker.ReloadOutcome{}, fmt.Errorf(
				"worker config: worker_id changed (%q -> %q); restart the worker to change its identity",
				workerID, wc.WorkerID)
		}
		switch workerModeOf(wc) {
		case modePolicy:
			// A policy to apply (a server push, or a SIGHUP re-projecting the in-memory
			// last-known-good). Project it, apply, report caps from the PROJECTION.
			if p != nil {
				cfg, rejected := projectPolicy(wc, *p)
				if err := cr.ReloadWith(cfg); err != nil {
					return worker.ReloadOutcome{}, err
				}
				detected := det.snapshot()
				return worker.ReloadOutcome{
					Caps:       workerCaps(wc, cfg, detected, mapKeys(cfg.Projects)),
					AppliedRev: p.Rev,
					Rejected:   rejected,
					Degraded:   diagnosePolicy(cfg, *p, wc, detected),
				}, nil
			}
			// p == nil: a SIGHUP with no last-known-good yet (fresh POLICY worker, or one
			// just switched from LEGACY). NO-OP on the project set (E-B2): keep whatever is
			// running, re-report its caps — NEVER construct an empty cfg + ReloadWith, which
			// would silently wipe the running projects. roots/guards changes take effect the
			// next time a real policy is projected.
			active := cr.Config()
			return worker.ReloadOutcome{
				Caps: workerCaps(wc, active, det.snapshot(), mapKeys(active.Projects)),
			}, nil
		default:
			// LEGACY / EMPTY: source projects from the worker's own config. ReloadWith runs
			// THE resolve pass for this snapshot (one detect, reusing the recorder it was
			// built with), so the caps read straight after come from that same probe.
			cfg := workerConfigToConfig(wc)
			if err := cr.ReloadWith(cfg); err != nil {
				return worker.ReloadOutcome{}, err
			}
			return worker.ReloadOutcome{
				Caps: workerCaps(wc, cfg, det.snapshot(), mapKeys(wc.Projects)),
			}, nil
		}
	}
}

// availabilityRecorder is the seam that carries ONE detect pass from the resolve that
// applied it (core.Build / core.ReloadWith) to the capability report built from the
// same config snapshot. It is a Detector decorator, not a second probe: it forwards to
// inner and remembers what came back.
//
// Why a decorator and not a Core accessor: the merge point (agent.Resolve) is owned by
// core and must stay the only one; the worker command needs the DetectResult map that
// pass produced, and the injected Detector is the only handle it already holds on it.
type availabilityRecorder struct {
	inner agent.Detector
	mu    sync.Mutex
	last  map[string]agent.DetectResult
}

// Detect implements agent.Detector.
func (r *availabilityRecorder) Detect(agents map[string]config.AgentConfig) map[string]agent.DetectResult {
	res := r.inner.Detect(agents)
	r.mu.Lock()
	r.last = res
	r.mu.Unlock()
	return res
}

// snapshot returns the most recent detect result (nil before the first pass). The map
// is never mutated after Detect returns it, so handing it out unlocked is safe; the
// lock only guards the pointer swap against a reload racing a register.
func (r *availabilityRecorder) snapshot() map[string]agent.DetectResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// workerCaps derives the capability report from ONE worker config snapshot (the
// raw worker.yaml for labels/max_concurrent, the mapped config.Config for the
// resolved agent keys/briefs) plus the detect result of the SAME resolve pass that
// produced that snapshot. Startup register and every reload go through it, so the two
// can never drift.
//
// projects is passed EXPLICITLY (rather than read from wc.Projects) because the source
// differs by mode (T5-F): LEGACY reports mapKeys(wc.Projects); POLICY reports the
// PROJECTED mapKeys(cfg.Projects). Making it a parameter forces each call site to name
// its source instead of silently reporting the wrong one.
//
// detected is display detail only (availability/version): the agent SET comes from the
// resolved config, never from the probe. That is the iron rule — an agent the operator
// declared stays in the caps report whether or not its CLI was found (only
// template-injected agents are detect-gated, and that gating already happened in
// agent.Resolve, before this config snapshot existed). A nil map simply leaves every
// availability unknown.
func workerCaps(wc *config.WorkerConfig, cfg *config.Config, detected map[string]agent.DetectResult, projects []string) wsproto.Caps {
	return wsproto.Caps{
		Labels:    wc.Labels,
		Projects:  projects,
		Agents:    agentKeys(cfg),
		AgentCaps: agentBriefs(cfg, detected),
		MaxConc:   wc.MaxConcurrent,
	}
}

// workerMode is the worker's project-sourcing mode, derived from worker.yaml (T5-A).
type workerMode int

const (
	// modeLegacy: no roots, has local projects → the worker sources projects from its
	// own worker.yaml and IGNORES any pushed policy (verification 1). The zero value.
	modeLegacy workerMode = iota
	// modePolicy: roots are configured → the server-pushed policy is authoritative and
	// projected onto the roots; any local `projects:` is ignored.
	modePolicy
	// modeEmpty: neither roots nor projects → the worker can run nothing (loud WARN).
	modeEmpty
)

// workerModeOf classifies a worker.yaml into its project-sourcing mode (T5-A):
// roots present ⇒ POLICY (regardless of any leftover projects); else projects ⇒
// LEGACY; else EMPTY.
func workerModeOf(wc *config.WorkerConfig) workerMode {
	switch {
	case len(wc.Roots) > 0:
		return modePolicy
	case len(wc.Projects) > 0:
		return modeLegacy
	default:
		return modeEmpty
	}
}

// projectPolicy projects a server Policy onto the worker's LOCAL config (T5-E). The
// project set is a COMPLETE-SNAPSHOT REPLACEMENT (E-B1): the returned cfg carries
// exactly the policy's projects (a shorter/empty policy revokes the missing ones —
// never a merge). A project whose host_path maps to no local root is REJECTED (never
// admitted with an empty HostPath, which filepath.Abs would resolve to the process
// CWD — verification 8). Everything else is a straight structure map:
//
//   - AllowedRunners is ALWAYS ["local"] (a worker executes dispatches locally);
//   - AllowExec is the policy's AND the worker's exec guard (guards only tighten);
//   - AllowedAgents is passed through VERBATIM — no intersection with cfg.Agents, whose
//     empty-list-means-all semantics would silently open every agent (D6, verification 13);
//   - InteractiveAllowedAgents is passed through, but CLEARED when the interactive guard
//     is explicitly false (empty = all forbidden, the opposite polarity of AllowedAgents);
//   - MaxConcurrentJobs / CaptureDiff are passed through verbatim (H2: dropping them
//     would silently mean unlimited concurrency / diff-on, verification 14).
//
// container_path / default_agent / storage / exchange_subdir / result_subdir / agent
// definitions are deliberately NOT projected (§4.4): the worker's own workerConfigToConfig
// supplies them, and cfg.Agents is left to agent.Resolve inside core.ReloadWith.
func projectPolicy(wc *config.WorkerConfig, p wsproto.Policy) (*config.Config, []wsproto.AppliedRejection) {
	cfg := workerConfigToConfig(wc) // storage/agents/runners/db/defaults; Projects replaced below
	projects := make(map[string]config.ProjectConfig, len(p.Projects))
	var rejected []wsproto.AppliedRejection
	for _, pp := range p.Projects {
		host, ok := wc.MapRoot(pp.HostPath)
		if !ok {
			rejected = append(rejected, wsproto.AppliedRejection{Key: pp.Key, Reason: "path_outside_roots"})
			continue // never admit an empty HostPath (verification 8)
		}
		projects[pp.Key] = config.ProjectConfig{
			HostPath:                 host,
			AllowedRunners:           []string{"local"},
			AllowExec:                pp.AllowExec && wc.Guards.IsExecAllowed(),
			AllowedAgents:            pp.AllowedAgents,
			InteractiveAllowedAgents: policyInteractiveAgents(pp, wc),
			MaxConcurrentJobs:        pp.MaxConcurrentJobs,
			CaptureDiff:              pp.CaptureDiff,
		}
	}
	cfg.Projects = projects // COMPLETE snapshot replace (E-B1); empty policy ⇒ empty set
	return cfg, rejected
}

// policyInteractiveAgents passes the policy's interactive allowlist through verbatim,
// but returns nil (clear) when the worker's interactive guard is explicitly false —
// an empty InteractiveAllowedAgents means "all interactive agents forbidden", so
// clearing it is how the guard tightens (opposite polarity to AllowedAgents).
func policyInteractiveAgents(pp wsproto.PolicyProject, wc *config.WorkerConfig) []string {
	if !wc.Guards.IsInteractiveAllowed() {
		return nil
	}
	return pp.InteractiveAllowedAgents
}

// diagnosePolicy computes the READ-ONLY Applied.Degraded list: projects the worker
// applied but with a capability the policy asked for gated off (guards) or an allowed
// agent the host does not have. It runs AFTER ReloadWith on the applied cfg, so it
// never writes config — pure diagnostics for the Cluster page, never routing.
func diagnosePolicy(cfg *config.Config, p wsproto.Policy, wc *config.WorkerConfig, detected map[string]agent.DetectResult) []wsproto.AppliedDegrade {
	var out []wsproto.AppliedDegrade
	for _, pp := range p.Projects {
		if _, ok := cfg.Projects[pp.Key]; !ok {
			continue // rejected (path_outside_roots); not applied, not degraded
		}
		if pp.AllowExec && !wc.Guards.IsExecAllowed() {
			out = append(out, wsproto.AppliedDegrade{Key: pp.Key, Gate: "exec"})
		}
		if len(pp.InteractiveAllowedAgents) > 0 && !wc.Guards.IsInteractiveAllowed() {
			out = append(out, wsproto.AppliedDegrade{Key: pp.Key, Gate: "interactive"})
		}
		for _, a := range pp.AllowedAgents {
			if !workerAgentAvailable(cfg, detected, a) {
				out = append(out, wsproto.AppliedDegrade{Key: pp.Key, Gate: "agent_unavailable:" + a})
			}
		}
	}
	return out
}

// workerAgentAvailable reports whether agent key resolves on this worker AND (when
// probed) its CLI was found. Unresolvable ⇒ unavailable; resolvable-but-unprobed ⇒
// assumed available (do not over-report a degrade for an agent we simply did not probe).
func workerAgentAvailable(cfg *config.Config, detected map[string]agent.DetectResult, key string) bool {
	if _, ok := agent.ResolveAgent(cfg, key); !ok {
		return false
	}
	if res, probed := detected[key]; probed {
		return res.Available
	}
	return true
}

// loadWorkerConfig reads and decodes the worker.yaml at path (the
// --worker-config flag). When path is empty it falls back to the user-level
// default <config-dir>/worker.yaml (config.UserWorkerConfigPath) — so a worker
// launched from a fixed config home needs no flag. There is still no current-dir
// discovery chain (the worker home is the config dir, not the cwd).
func loadWorkerConfig(path string) (*config.WorkerConfig, error) {
	if path == "" {
		def, err := config.UserWorkerConfigPath()
		if err != nil {
			return nil, fmt.Errorf("worker requires --worker-config <worker.yaml> (default path unresolved: %w)", err)
		}
		path = def
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read worker config %s: %w", path, err)
	}
	var wc config.WorkerConfig
	if err := yaml.Unmarshal(data, &wc); err != nil {
		return nil, fmt.Errorf("decode worker config %s: %w", path, err)
	}
	return &wc, nil
}

// workerConfigToConfig maps a WorkerConfig onto the server-shaped config.Config
// so buildCore can assemble the worker's local job service. The worker has no
// server.workers / token / web console; its hub singleton stays idle (no worker
// runners are configured locally).
//
// It is a pure STRUCTURE mapping and must stay one: it does NOT detect agent CLIs and
// does NOT merge the built-in agent templates (P2 T0-B). That merge belongs to
// agent.Resolve, invoked exactly once per config snapshot by core.Build / core.ReloadWith,
// both of which every caller here runs immediately afterwards. Merging here as well
// would (a) probe the host twice per startup and (b) hand Build a config whose injected
// keys already exist — which Build would read as operator declarations and promote to
// un-gated escape hatches, leaving the detect gate effective only on the first pass.
func workerConfigToConfig(wc *config.WorkerConfig) *config.Config {
	cfg := &config.Config{
		Storage:  wc.Storage,
		Projects: wc.Projects,
		Agents:   wc.Agents,
		Runners:  wc.Runners,
	}
	// Pin the worker's metadata db to its id-namespaced path (<config-dir>/worker/
	// <worker-id>.db by default) so it never collides with a serve's gofer.db in a
	// shared config dir; an explicit db_path / root is honoured (ResolveWorkerDBPath).
	cfg.Storage.DBPath = cfg.ResolveWorkerDBPath(wc.WorkerID)
	// Defaults (result subdirs / nil maps) so the local store + registries behave
	// identically to a serve process.
	config.ApplyDefaults(cfg)
	return cfg
}

// workerPolicyCachePath is the last-known-good policy cache file for a worker
// (<config-dir>/run/worker-<id>.policy.json). It is where a POLICY worker persists the
// applied policy so a restart with an unreachable server keeps its projects (T5-F);
// the T6 CLI reads the same path.
func workerPolicyCachePath(workerID string) string {
	return config.RuntimeFilePath("run", "worker-"+workerID+".policy.json")
}

// initialWorkerConfig builds the worker's STARTUP config per mode (T5-A), returning
// the config, the projects to advertise on the first register, and the policy to seed
// as the in-memory last-known-good (POLICY cold start from cache; nil otherwise). It
// logs the migration/error hints that the three-state model requires.
func initialWorkerConfig(mode workerMode, wc *config.WorkerConfig, cachePath string) (*config.Config, []string, *wsproto.Policy) {
	switch mode {
	case modePolicy:
		if len(wc.Projects) > 0 {
			slog.Warn("worker in policy mode ignores its local projects (the server pushes policy); delete `projects:` from worker.yaml",
				"worker_id", wc.WorkerID, "ignored_projects", len(wc.Projects))
		}
		p, rerr := worker.ReadPolicyCacheFile(cachePath, wc.WorkerID)
		if rerr != nil {
			slog.Warn("worker ignoring unusable policy cache; starting with no projects until a policy arrives",
				"worker_id", wc.WorkerID, "err", rerr)
		}
		if p != nil {
			cfg, _ := projectPolicy(wc, *p)
			slog.Info("worker recovered last-known-good policy from cache",
				"worker_id", wc.WorkerID, "rev", p.Rev, "projects", len(cfg.Projects))
			return cfg, mapKeys(cfg.Projects), p
		}
		// No usable cache: start empty (the projection of an empty policy) — the worker
		// registers with zero projects and converges once the server pushes one.
		cfg, _ := projectPolicy(wc, wsproto.Policy{})
		return cfg, nil, nil
	case modeEmpty:
		slog.Error("worker has neither roots (policy mode) nor projects (legacy) — it will run no jobs",
			"worker_id", wc.WorkerID)
		return workerConfigToConfig(wc), nil, nil
	default: // modeLegacy
		slog.Warn("worker running in legacy local-projects mode; migrate by adding `roots:` and letting the server push policy",
			"worker_id", wc.WorkerID, "projects", len(wc.Projects))
		cfg := workerConfigToConfig(wc)
		return cfg, mapKeys(wc.Projects), nil
	}
}

// resolveWorkerToken resolves the hub Bearer token from the server_link (env var
// takes precedence over a literal token).
func resolveWorkerToken(link config.WorkerServerLink) string {
	if link.TokenEnv != "" {
		if v := os.Getenv(link.TokenEnv); v != "" {
			return v
		}
	}
	return link.Token
}

// wsDialURLs normalises the hub URL list for dialing (C7 multi-address). Each
// ws:// or wss:// URL passes through; a bare http(s):// is left as-is
// (coder/websocket.Dial accepts http/https too). The order is preserved — the
// client rotates through them on connect failure.
func wsDialURLs(urls []string) []string {
	out := make([]string, len(urls))
	copy(out, urls)
	return out
}

// msToDuration converts a milliseconds config value to a Duration (<= 0 → 0, the
// client then applies its default).
func msToDuration(ms int) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// secToDuration converts a seconds config value to a Duration (<= 0 → 0, the
// client then applies its default).
func secToDuration(sec int) time.Duration {
	if sec <= 0 {
		return 0
	}
	return time.Duration(sec) * time.Second
}

// mapKeys returns the keys of a project map (for the register capability hint).
func mapKeys(m map[string]config.ProjectConfig) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// resolvedAgentKeys returns every agent key the worker can ACTUALLY run: the keys
// declared in its config PLUS the built-in exec agent, which agent.ResolveAgent
// makes available even when undeclared (agent.ExecAgentKey). Reporting only the raw
// config map would under-report the canonical exec-only worker — whose agents block
// is typically absent entirely — and the hub now treats this report as authoritative
// for validation/routing. Sorted for a stable report (Go map iteration is not).
func resolvedAgentKeys(cfg *config.Config) []string {
	seen := map[string]bool{agent.ExecAgentKey: true}
	if cfg != nil {
		for k := range cfg.Agents {
			seen[k] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// agentKeys returns the worker's runnable agent keys for the register frame's
// back-compat key list (Register.Agents). It is the same key set as agentBriefs so
// the two never drift.
func agentKeys(cfg *config.Config) []string { return resolvedAgentKeys(cfg) }

// agentBriefs builds the TYPED capability report the worker sends on register
// (wsproto.Register.AgentCaps): the hub/UI needs each agent's type + interactive
// flag, not just its key, and the worker is the authority for its own agents.
//
// It resolves each key through agent.ResolveAgent — the SAME resolution the worker's
// job service applies when it re-validates a dispatch — rather than reading the raw
// config map. That is what makes the report true: the built-in exec agent is included
// even when undeclared, and a bare `exec:` block with no explicit `type:` is reported
// with its normalised Type (agent.TypeExec), not an empty string.
//
// detected (the result of the one resolve pass this cfg came out of) fills the
// DISPLAY-ONLY availability/version. It NEVER filters: a key present in the resolved
// config is advertised even when its probe failed — see workerCaps and
// wsproto.AgentBrief.Available. A key absent from detected keeps Available nil
// (unknown), which is exactly what an old worker reports.
func agentBriefs(cfg *config.Config, detected map[string]agent.DetectResult) []wsproto.AgentBrief {
	keys := resolvedAgentKeys(cfg)
	out := make([]wsproto.AgentBrief, 0, len(keys))
	for _, k := range keys {
		ac, ok := agent.ResolveAgent(cfg, k)
		if !ok {
			continue
		}
		b := wsproto.AgentBrief{Key: k, Type: ac.Type, Interactive: ac.Interactive}
		if res, probed := detected[k]; probed {
			avail := res.Available
			b.Available = &avail
			b.Version = res.Version
		}
		out = append(out, b)
	}
	return out
}

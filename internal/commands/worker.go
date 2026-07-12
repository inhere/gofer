package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
		Subs: []*gcli.Command{NewWorkerStopCmd()},
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
	wcfg := workerConfigToConfig(wc)
	cr, err := core.Build(wcfg)
	if err != nil {
		return errorx.Failf(workerExitErr, "%v", err)
	}
	defer func() { _ = cr.Close() }()

	rc := wc.ServerLink.Reconnect
	cl := worker.New(worker.Config{
		WorkerID:       wc.WorkerID,
		URLs:           wsDialURLs(wc.ServerLink.URLs),
		Token:          resolveWorkerToken(wc.ServerLink),
		Labels:         wc.Labels,
		Projects:       mapKeys(wc.Projects),
		Agents:         agentKeys(wcfg),
		AgentCaps:      agentBriefs(wcfg),
		GoferVersion:   info.DisplayVersion(),
		MaxConc:        wc.MaxConcurrent,
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
func agentBriefs(cfg *config.Config) []wsproto.AgentBrief {
	keys := resolvedAgentKeys(cfg)
	out := make([]wsproto.AgentBrief, 0, len(keys))
	for _, k := range keys {
		ac, ok := agent.ResolveAgent(cfg, k)
		if !ok {
			continue
		}
		out = append(out, wsproto.AgentBrief{Key: k, Type: ac.Type, Interactive: ac.Interactive})
	}
	return out
}

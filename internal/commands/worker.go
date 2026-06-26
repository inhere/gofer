package commands

import (
	"fmt"
	"os"
	"time"

	yaml "github.com/goccy/go-yaml"
	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/worker"
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

// workerPIDFile / workerLogFile are the daemon-mode runtime files (c44),
// namespaced by worker id so multiple workers on one host never collide:
// <config-dir>/run/worker-<id>.{pid,log}.
func workerPIDFile(id string) string { return config.RuntimeFilePath("run", "worker-"+id+".pid") }
func workerLogFile(id string) string { return config.RuntimeFilePath("run", "worker-"+id+".log") }

// NewWorkerCmd builds the `worker` command: load worker.yaml, build the local
// job service (the worker runs jobs locally with its OWN config), dial the hub,
// register and run the dispatch loop (ws-worker §4/§6).
func NewWorkerCmd() *gcli.Command {
	return &gcli.Command{
		Name:    "worker",
		Desc:    "As worker that dials a central hub and executes dispatched jobs locally",
		Aliases: []string{"w"},
		Config: func(c *gcli.Command) {
			c.StrOpt(&workerOpts.config, "worker-config", "", "", "path to the worker config file (default: <config-dir>/worker.yaml)")
			c.BoolOpt(&workerOpts.daemon, "daemon", "d", false, "run in background (detached); logs to <config-dir>/run/worker-<id>.log")
		},
		Func: runWorker,
	}
}

func runWorker(c *gcli.Command, _ []string) error {
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
	cr, err := core.Build(workerConfigToConfig(wc))
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
		Agents:         agentKeys(wc.Agents),
		MaxConc:        wc.MaxConcurrent,
		InitialBackoff: msToDuration(rc.InitialBackoffMS),
		MaxBackoff:     msToDuration(rc.MaxBackoffMS),
		PingInterval:   secToDuration(rc.PingIntervalSec),
		ReadDeadline:   secToDuration(rc.ReadDeadlineSec),
	}, cr.Jobs)

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

// agentKeys returns the keys of an agent map (for the register capability hint).
func agentKeys(m map[string]config.AgentConfig) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

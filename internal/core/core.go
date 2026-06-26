package core

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	peerhttprunner "github.com/inhere/gofer/internal/runner/peerhttp"
	workerrunner "github.com/inhere/gofer/internal/runner/worker"
	"github.com/inhere/gofer/internal/wshub"
)

// Core bundles the runtime objects assembled from a loaded config: the
// project/agent registries, the runner set, the SQLite metadata store and the
// job service. Both `serve` (HTTP control plane) and `mcp` (stdio MCP server)
// wire the same Core so the MCP tools reuse the identical job.Service (plan §P8:
// "MCP 内部复用 job.Service", 不复制执行逻辑).
type Core struct {
	Cfg      *config.Config
	Projects *project.Registry
	Agents   *agent.Registry
	Runners  map[string]runner.Runner
	Store    *jobstore.Store
	Jobs     *job.Service
	// workflowEngine is the job-chain workflow engine bound to Jobs (layering design
	// §13). It is the WorkflowAdvancer injected into Jobs (finish→Advance) and the
	// handle serve/httpapi consume via Workflow() to drive workflow
	// submit/advance/query. Always non-nil after Build.
	workflowEngine *workflow.Engine
	// Hub is the ws-worker hub singleton (serve mounts it on /v1/workers/connect;
	// every type=worker runner references this one instance). Always non-nil.
	Hub *wshub.Hub
}

// Workflow returns the Core's workflow engine (the handle serve/httpapi consume to
// drive job-chain workflows). Always non-nil after Build.
func (c *Core) Workflow() *workflow.Engine { return c.workflowEngine }

// Close releases the Core's owned resources — currently the SQLite metadata
// store. Callers (serve/mcp) defer it for graceful shutdown so WAL is
// checkpointed and the db handle closed cleanly (design §14).
func (c *Core) Close() error {
	if c == nil || c.Store == nil {
		return nil
	}
	return c.Store.Close()
}

// Build assembles the registries, runner set, metadata store and job service
// from cfg. It is the single wiring point shared by serve and mcp; peer-http
// runners declared in config are registered too (plan §11.1, P7). It opens the
// SQLite metadata db (design §11 ResolveDBPath) and returns an error if that
// fails, since the job service cannot operate without it.
func Build(cfg *config.Config) (*Core, error) {
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}

	// Build the ws-worker hub singleton ONCE (unlike peer-http, every worker
	// runner references the same hub instance). Its token→worker bindings come
	// from cfg.Server.Workers (review #1: worker_id is its own caller id).
	hub := wshub.New(workerBindings(cfg))

	for name, rc := range cfg.Runners {
		switch rc.Type {
		case "peer-http":
			token := ""
			if rc.TokenEnv != "" {
				token = os.Getenv(rc.TokenEnv)
			}
			runners[name] = peerhttprunner.New(name, rc.BaseURL, token)
		case "worker":
			runners[name] = workerrunner.New(name, rc.WorkerID, hub)
		}
	}
	store, err := jobstore.Open(cfg.ResolveDBPath())
	if err != nil {
		return nil, fmt.Errorf("open metadata store: %w", err)
	}
	// Label-based worker auto-selection (P2/D3) reads live candidates from the hub
	// registry; allowed is the config-registered worker set so only in-册 ids are
	// ever considered.
	sel := &hubWorkerSelector{hub: hub, allowed: cfg.Server.Workers}
	jobs := job.NewService(cfg, projects, agents, runners, store, sel)
	// Bind the workflow engine to the job service (layering design §13.4): the engine
	// reads/drives job-chain workflows over jobs, and is injected back as the
	// WorkflowAdvancer so finish() can advance a chain when a step-job reaches terminal.
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	return &Core{Cfg: cfg, Projects: projects, Agents: agents, Runners: runners, Store: store, Jobs: jobs, workflowEngine: eng, Hub: hub}, nil
}

// hubWorkerSelector adapts the ws-worker hub registry to job.WorkerSelector (P2):
// it walks the config-registered worker ids and emits a candidate for each one
// that currently has a live connection (hub.WorkerSnapshot ok), computing the
// heartbeat age from the snapshot's last-heartbeat timestamp. Offline workers are
// simply absent, so selectWorker only ever picks a connected worker.
type hubWorkerSelector struct {
	hub     *wshub.Hub
	allowed map[string]config.WorkerAuthConfig
}

// Candidates implements job.WorkerSelector.
func (h *hubWorkerSelector) Candidates() []job.WorkerCandidate {
	out := make([]job.WorkerCandidate, 0, len(h.allowed))
	now := time.Now().Unix()
	for id := range h.allowed {
		ws, ok := h.hub.WorkerSnapshot(id) // ok only when the worker is connected
		if !ok {
			continue
		}
		out = append(out, job.WorkerCandidate{
			WorkerID:     ws.WorkerID,
			Labels:       ws.Labels,
			InFlight:     ws.InFlight,
			HeartbeatAge: time.Duration(now-ws.LastHeartbeat) * time.Second,
		})
	}
	return out
}

// workerBindings builds the hub's worker_id → caller-id binding map from
// cfg.Server.Workers. The worker's own id IS its caller id (review #1): the
// presented token must resolve (via the server's caller table) to the same
// worker_id. A nil/empty map means no worker may register.
func workerBindings(cfg *config.Config) map[string]string {
	if len(cfg.Server.Workers) == 0 {
		return nil
	}
	out := make(map[string]string, len(cfg.Server.Workers))
	for workerID := range cfg.Server.Workers {
		out[workerID] = workerID
	}
	return out
}

// Reload re-loads the config from path and atomically swaps it into every
// component that holds a config snapshot — the project/agent registries and the
// job service (C3 SIGHUP hot-reload). It does NOT restart the process, touch
// in-flight jobs or reopen the jobstore.
//
// Fail-safe: if the new config fails to load/validate, the OLD config is kept
// (nothing is swapped) and the error is returned so the caller can log and keep
// serving. The swap itself never partially applies — all three components are
// repointed at the same already-validated *config.Config.
//
// LIMITATION: the runner instances in Core.Runners are built once here at
// assemble time and are NOT rebuilt on reload. Swapping the config makes the
// peer-runner classification and every allowlist/validation observe the new
// config, but adding a brand-new runner TYPE (a new peer-http entry) still
// needs a restart to instantiate its runner. Reload covers adding/removing
// projects and agents and any config-derived validation.
func (c *Core) Reload(path string) error {
	// Fail-safe for a deleted config file: config.Load returns a fresh EMPTY
	// config (no error) when the file is missing — fine on first run, but on a
	// reload that would silently wipe all projects/agents. When an explicit path
	// was given and it no longer exists, treat the reload as a failure and keep
	// the old config (path=="" is default-resolution mode and keeps prior behaviour).
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("reload config: %w", err)
		}
	}
	newCfg, _, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("reload config: %w", err)
	}
	// D6: reload merges overlays too. Fail-safe — overlay parse failures only
	// warn (returned slice), they never make the reload fail. The startup path
	// surfaces these on the console; on this runtime reload path there is no console
	// handle, so log each warn via slog instead of dropping it silently.
	for _, w := range config.ApplyProjectOverlays(newCfg) {
		slog.Warn("config reload: overlay warn", "detail", w)
	}
	c.Cfg = newCfg
	c.Projects.Reload(newCfg)
	c.Agents.Reload(newCfg)
	c.Jobs.Reload(newCfg)
	return nil
}

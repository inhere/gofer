package job

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/store"
)

// Timeout bounds (plan §9 P4, §11). TimeoutSec defaults to DefaultTimeoutSec
// when unset and is clamped to MaxTimeoutSec.
const (
	DefaultTimeoutSec = 300
	MaxTimeoutSec     = 3600
)

// jobIDLayout is the time prefix for a job id (no separators that would clash
// with directory names). A random suffix makes it unique across process
// restarts (plan §9 P4: a seconds+seq scheme collides after a restart).
const jobIDLayout = "20060102-150405"

// jobIDCreateRetries bounds how many times Submit re-rolls a colliding id.
const jobIDCreateRetries = 5

// builtinLocalRunner is the runner key that needs no config declaration,
// mirroring the built-in exec agent.
const builtinLocalRunner = "local"

// Submit validation sentinels. They let the HTTP layer (internal/httpapi) map a
// rejected Submit to the right status code without string-matching: an unknown
// project is a 404, everything else (agent not allowed / exec gate / runner not
// allowed / bad request) is a 400. validate wraps these so errors.Is works.
var (
	// ErrUnknownProject is returned (wrapped) when the project_key is not
	// registered. HTTP layer maps it to 404.
	ErrUnknownProject = errors.New("unknown project")
	// ErrInvalidRequest marks a request that is well-formed but not permitted
	// (agent not allowed, exec gate, runner not allowed, missing fields). HTTP
	// layer maps it to 400.
	ErrInvalidRequest = errors.New("invalid request")
	// ErrNoEligibleWorker is returned when a worker job supplies worker_labels but
	// no connected worker advertises all of them (or all such workers are stale).
	// HTTP layer maps it to 503 (temporarily unavailable — retry / pick another).
	ErrNoEligibleWorker = errors.New("no eligible worker")
)

// MetricsSink receives job lifecycle counters (E16, design §6). The job package
// records through this narrow interface so it never imports prometheus; the
// metrics package implements it and commands.buildCore injects it via
// SetMetrics. Every call site guards `if s.metrics != nil`, so a service with no
// sink wired is a clean no-op.
type MetricsSink interface {
	// JobSubmitted is called once per accepted Submit.
	JobSubmitted(caller, project, agent, runner string)
	// JobTerminal is called once when a job reaches a terminal state, with the
	// end-to-end (submit→terminal) duration in seconds.
	JobTerminal(status, caller, project, agent, runner string, durationSec float64)
	// WorkflowTerminal is called once when a workflow (job chain) reaches a terminal
	// state (done/failed/cancelled), with its submit→terminal duration in seconds
	// (P4 / T4.3, design §9). It is the workflow analogue of JobTerminal; the job
	// package records it through this narrow interface so it never imports prometheus.
	WorkflowTerminal(status string, durationSec float64)
}

// ServiceStats is the live in-memory job snapshot the metrics GaugeFuncs read at
// scrape time (design §6.4): InFlight = entries currently tracked in s.jobs
// (queued+running+pending, since terminal jobs are evicted in finish), Queued /
// Running break that down by status.
type ServiceStats struct {
	InFlight int
	Queued   int
	Running  int
}

// Service accepts job requests, runs them asynchronously and tracks their state.
// It is safe for concurrent use.
type Service struct {
	// cfg holds the active config behind an atomic.Pointer so SIGHUP-driven
	// hot-reload (C3) can atomically swap it (see Reload). Read it via config():
	// every method that consults cfg takes ONE snapshot at entry and uses that
	// snapshot for its whole call, so a concurrent Reload can never make a single
	// call observe two different configs.
	cfg      atomic.Pointer[config.Config]
	projects *project.Registry
	agents   *agent.Registry
	runners  map[string]runner.Runner
	// newStore builds a Store for a given absolute result base dir. Defaults to a
	// FileStore; overridable in tests.
	newStore func(base string) store.Store

	// meta is the SQLite-backed job metadata/index store. It replaces the
	// per-project jobs.jsonl index and result.json metadata writes: job snapshots
	// are upserted here (one row per job id) and ListJobs/Get read it back. Logs
	// stay as files in the per-job result dir. See design §6/§8/§10.
	meta *jobstore.Store

	// events is the append-only lifecycle event sink for recordEvent (E13). It is
	// nil in production (recordEvent falls back to s.meta); tests inject a failing
	// sink to prove event recording is best-effort and never affects job terminal
	// state.
	events eventSink

	// deliveries is the E14 webhook delivery-enqueue sink. It is nil in production
	// (enqueueDeliveries falls back to s.meta); tests inject a sink to observe or
	// fail the enqueue and prove it is best-effort.
	deliveries deliverySink

	// workers supplies connected-worker candidates for label-based auto-selection
	// (P2 / D3). Injected by commands.buildCore (hub-backed); may be nil — Submit
	// only consults it on the runner=worker + worker_labels path, so every other
	// runner is unaffected.
	workers WorkerSelector

	mu   sync.Mutex
	jobs map[string]*jobEntry
	// sems holds per-project concurrency semaphores (buffered channels). Lazily
	// created from ProjectConfig.MaxConcurrentJobs on first use; a value <= 0
	// means unbounded (no semaphore).
	sems map[string]chan struct{}
	// callerSems holds per-caller concurrency semaphores (E17, design §7.2). Same
	// lazy-create + fixed-capacity model as sems (guarded by s.mu); a caller with no
	// limit (<= 0) or an empty caller id gets nil (no gating). NOTE (design §7.4):
	// like sems, a semaphore's capacity is frozen at first creation, so a hot-reload
	// changing a caller's MaxConcurrentJobs does NOT resize an already-built sem —
	// the NEW value takes effect when the caller next has no live sem / on restart.
	callerSems map[string]chan struct{}

	// nowFn yields the current time; overridable in tests.
	nowFn func() time.Time

	// postFn is the webhook POST used by the E14 delivery sweeper (deliverOne). It
	// defaults to notify.PostWebhook (validated + signed real HTTP POST) and is
	// overridable in tests so the claim→post→mark state machine can be driven
	// deterministically without network / the loopback SSRF block.
	postFn func(ctx context.Context, target, eventType string, body []byte, secretValue string, cfg config.NotificationConfig) error

	// metrics is the E16 lifecycle-counter sink (nil = no metrics wired). It is
	// injected post-construction by SetMetrics (commands.buildCore) so the job
	// package never imports prometheus. All埋点 sites guard `if s.metrics != nil`.
	metrics MetricsSink
}

// SetMetrics injects the E16 lifecycle-counter sink (design §6). It is called
// once at assemble time (commands.buildCore) before the service starts handling
// jobs; passing nil leaves metrics disabled (every埋点 is a no-op).
func (s *Service) SetMetrics(m MetricsSink) { s.metrics = m }

// Stats returns a live snapshot of the in-flight job set for the metrics
// GaugeFuncs (design §6.4). It is evaluated at scrape time, so the critical
// section is kept short. Lock order is s.mu THEN entry.mu, matching every other
// Service path (Submit/finish) so there is no reverse-hold deadlock.
func (s *Service) Stats() ServiceStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := ServiceStats{InFlight: len(s.jobs)}
	for _, e := range s.jobs {
		e.mu.Lock()
		switch e.result.Status {
		case StatusQueued:
			st.Queued++
		case StatusRunning:
			st.Running++
		}
		e.mu.Unlock()
	}
	return st
}

// jobEntry is the in-process record for one job: its current result snapshot,
// the cancel func for the running context, and a per-job lock.
type jobEntry struct {
	mu     sync.Mutex
	result JobResult
	cancel context.CancelFunc
	store  store.Store
	done   chan struct{} // closed when the job reaches a terminal state
	// interactions holds this process's authoritative interaction state for the
	// job, in creation order. Guarded by mu (shared with result, so a status
	// flip and an interaction edit never race). P9.
	interactions []*interactionRec
}

// NewService builds a job service. runners is the set of usable runners keyed by
// name (at least "local"). project/agent registries and config come from the
// loaded config. meta is the SQLite metadata store (job index/persistence); it
// must be non-nil — the caller (commands.buildCore / tests) opens it. sel
// supplies connected-worker candidates for label-based auto-selection (P2); it
// may be nil (the non-worker paths never touch it).
func NewService(cfg *config.Config, projects *project.Registry, agents *agent.Registry, runners map[string]runner.Runner, meta *jobstore.Store, sel WorkerSelector) *Service {
	s := &Service{
		projects:   projects,
		agents:     agents,
		runners:    runners,
		meta:       meta,
		workers:    sel,
		newStore:   func(base string) store.Store { return store.NewFileStore(base) },
		jobs:       map[string]*jobEntry{},
		sems:       map[string]chan struct{}{},
		callerSems: map[string]chan struct{}{},
		nowFn:      time.Now,
	}
	s.cfg.Store(cfg)
	return s
}

// config returns the current config snapshot. Callers must take a single
// snapshot at entry and reuse it for the whole call (see the Service.cfg note),
// so a concurrent Reload cannot tear a single operation.
func (s *Service) config() *config.Config { return s.cfg.Load() }

// Reload atomically swaps the service's config to newCfg (C3 SIGHUP hot-reload).
// It is safe to call concurrently with Submit/ListJobs/Prune; in-flight calls
// keep using the snapshot they already loaded.
//
// LIMITATION: this swaps only the config pointer. The runners map holds concrete
// runner instances built once at assemble time (commands.buildCore) and is NOT
// rebuilt here, so adding a brand-new runner TYPE still needs a restart. Reload
// covers added/removed projects and agents and any cfg-derived validation
// (allowlists, exec gate, peer-runner classification, result dirs, retention).
func (s *Service) Reload(newCfg *config.Config) { s.cfg.Store(newCfg) }

// semaphore returns (creating if needed) the per-project concurrency semaphore.
// A limit <= 0 means unbounded and returns nil (no gating).
func (s *Service) semaphore(projectKey string, limit int) chan struct{} {
	if limit <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sem, ok := s.sems[projectKey]; ok {
		return sem
	}
	sem := make(chan struct{}, limit)
	s.sems[projectKey] = sem
	return sem
}

// callerSemaphore returns (creating if needed) the per-caller concurrency
// semaphore (E17, design §7.2). Mirrors semaphore(): an empty caller id or a
// limit <= 0 means unbounded and returns nil (no gating). Guarded by s.mu like
// sems. Capacity is frozen at creation (hot-reload caveat, design §7.4).
func (s *Service) callerSemaphore(callerID string, limit int) chan struct{} {
	if callerID == "" || limit <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sem, ok := s.callerSems[callerID]; ok {
		return sem
	}
	sem := make(chan struct{}, limit)
	s.callerSems[callerID] = sem
	return sem
}

// CallerRate exposes the effective per-caller submit rate for the HTTP
// rate-limit middleware (E17, design §7.3). It reads the CURRENT config snapshot
// (s.config(), the atomic.Pointer that Reload swaps), so a SIGHUP change to a
// caller's rate_limit / rate_burst is observed on the very next request — the
// rate-limit config真源 is here, NOT a copy held by httpapi.Server (which is not
// on the reload path). Returns (0, 0) when the caller has no rate gating.
func (s *Service) CallerRate(callerID string) (rps float64, burst int) {
	return s.config().Server.CallerRate(callerID)
}

// isPeerRunner reports whether name is a configured peer-http runner (a remote
// runner that forwards the job to a peer bridge) in the given config snapshot.
// Such runners resolve the agent/command on the peer, so the host skips local
// exec resolution.
func isPeerRunner(cfg *config.Config, name string) bool {
	rc, ok := cfg.Runners[name]
	return ok && rc.Type == "peer-http"
}

// isWorkerRunner reports whether name is a configured ws-worker runner (a remote
// runner that dispatches the job to a worker over the hub WebSocket). Like
// peer-http it resolves the agent/command on the remote side, so the host skips
// local exec resolution.
func isWorkerRunner(cfg *config.Config, name string) bool {
	rc, ok := cfg.Runners[name]
	return ok && rc.Type == "worker"
}

// isRemoteRunner reports whether name is any remote runner (peer-http or
// ws-worker); both share the host-side "skip local resolution + set Forward"
// path in Submit.
func isRemoteRunner(cfg *config.Config, name string) bool {
	return isPeerRunner(cfg, name) || isWorkerRunner(cfg, name)
}

// validate enforces the project/agent/runner/exec allowlists (plan §11) and
// returns the resolved project config.
//
// remote (a peer-http runner) relaxes two host-local checks: the exec-type
// security gate and the agent-must-be-known check. A remote job is resolved and
// executed on the peer with ITS config, so the host may legitimately not know
// the agent (it can be a peer-only agent) and must not impose its own exec gate.
// The agent allowlist (CheckAllowed) and the runner allowlist still apply on the
// host as the access-control boundary.
func (s *Service) validate(cfg *config.Config, req JobRequest, remote bool) (config.ProjectConfig, error) {
	proj, ok := cfg.Projects[req.ProjectKey]
	if !ok {
		return config.ProjectConfig{}, fmt.Errorf("%w: unknown project %q", ErrUnknownProject, req.ProjectKey)
	}

	// Agent must be in the project's allowed_agents (exec is not exempt).
	if err := agent.CheckAllowed(cfg, req.ProjectKey, req.Agent); err != nil {
		return config.ProjectConfig{}, fmt.Errorf("%w: %s", ErrInvalidRequest, err.Error())
	}

	if !remote {
		// exec security gate: the agent must be type exec AND the project must opt
		// in. Skipped for remote jobs — the peer enforces its own exec gate.
		ac, ok := agent.ResolveAgent(cfg, req.Agent)
		if !ok {
			return config.ProjectConfig{}, fmt.Errorf("%w: unknown agent %q", ErrInvalidRequest, req.Agent)
		}
		if ac.Type == agent.TypeExec && !proj.AllowExec {
			return config.ProjectConfig{}, fmt.Errorf("%w: exec agent %q not allowed: project %q has allow_exec=false", ErrInvalidRequest, req.Agent, req.ProjectKey)
		}
	}

	// Runner must be in allowed_runners ("local" is a built-in default).
	if req.Runner == "" {
		return config.ProjectConfig{}, fmt.Errorf("%w: runner is required", ErrInvalidRequest)
	}
	if err := checkRunnerAllowed(proj, req.Runner); err != nil {
		return config.ProjectConfig{}, fmt.Errorf("%w: %s", ErrInvalidRequest, err.Error())
	}

	// ws-worker runner: an EXPLICIT worker_id must be a known server.workers entry
	// (review #1: worker_id is part of the worker's caller identity; an unknown id
	// has no live binding/conn). An empty worker_id is allowed now — the worker is
	// resolved post-validate by selectTargetWorker (labels → auto-select, else the
	// runner's configured default, D4).
	if isWorkerRunner(cfg, req.Runner) && req.WorkerID != "" {
		if _, ok := cfg.Server.Workers[req.WorkerID]; !ok {
			return config.ProjectConfig{}, fmt.Errorf("%w: unknown worker_id %q", ErrInvalidRequest, req.WorkerID)
		}
	}
	return proj, nil
}

// selectTargetWorker resolves req.WorkerID for a worker runner when it was not
// given explicitly (D3/D4). It runs in Submit right after validate so the chosen
// id flows into both the Forward and the persisted JobResult.worker_id. It is a
// no-op for non-worker runners and for an explicit worker_id (explicit routing
// wins, labels ignored).
//
// Resolution order when worker_id is empty:
//   - worker_labels given: auto-select a connected worker advertising ALL labels
//     (least loaded / freshest, D3). No eligible candidate → ErrNoEligibleWorker.
//   - no labels: leave worker_id empty and rely on the runner's configured default
//     worker (D4 fallback); the worker runner errors if it has no default binding.
func (s *Service) selectTargetWorker(cfg *config.Config, req *JobRequest) error {
	if !isWorkerRunner(cfg, req.Runner) || req.WorkerID != "" {
		return nil
	}
	if len(req.WorkerLabels) == 0 {
		return nil // D4: fall back to the runner's configured default worker.
	}
	var cands []WorkerCandidate
	if s.workers != nil {
		cands = s.workers.Candidates()
	}
	picked := selectWorker(cands, req.WorkerLabels)
	if picked == "" {
		return fmt.Errorf("%w: no eligible worker for labels %v", ErrNoEligibleWorker, req.WorkerLabels)
	}
	req.WorkerID = picked // inject: Forward + JobResult.worker_id now use it.
	return nil
}

// checkRunnerAllowed verifies req.Runner is in the project allowlist. The
// built-in "local" runner is accepted when the allowlist is empty or lists it.
func checkRunnerAllowed(proj config.ProjectConfig, runnerKey string) error {
	for _, r := range proj.AllowedRunners {
		if r == runnerKey {
			return nil
		}
	}
	if runnerKey == builtinLocalRunner && len(proj.AllowedRunners) == 0 {
		return nil
	}
	return fmt.Errorf("runner %q is not allowed in project", runnerKey)
}

// snapshot returns a copy of the entry's current result under its lock.
func (e *jobEntry) snapshot() JobResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.result
}

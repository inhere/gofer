package job

import (
	"context"
	"errors"
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

// JobIDLayout is the time prefix for a job id (no separators that would clash
// with directory names). A random suffix makes it unique across process
// restarts (plan §9 P4: a seconds+seq scheme collides after a restart).
const JobIDLayout = "20060102-150405"

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

	// wf is the workflow engine seam (layering design §13.4): the ONLY reverse
	// dependency from the single-job path to the workflow sub-package. finish drives
	// chain advancement through it. nil = no workflow engine wired (pure single-job
	// deployment / unit tests), in which case finish never triggers advance — exactly
	// the old "a non-workflow job does not advance" behaviour.
	wf WorkflowAdvancer
}

// WorkflowAdvancer is the job→workflow seam (layering design §13.4, D-B9): the job
// package defines it so it never imports the workflow sub-package; the workflow
// Engine implements it and core injects it via SetWorkflow. finish calls Advance
// when a step-job reaches a terminal state.
type WorkflowAdvancer interface{ Advance(wfID string) }

// SetWorkflow injects the workflow engine (the WorkflowAdvancer seam). It is called
// once at assemble time (internal/core) after the service is built; passing nil (or
// never calling it) leaves workflow advancement disabled (finish is a no-op for the
// workflow path).
func (s *Service) SetWorkflow(w WorkflowAdvancer) { s.wf = w }

// Meta / Now / Config / Metrics / Validate are the narrow accessor surface the
// workflow sub-package consumes through its JobOps interface (layering design §13.3).
// They expose existing private state/behaviour read-only without widening it further;
// Service thereby satisfies workflow.JobOps structurally (no workflow import here).
func (s *Service) Meta() *jobstore.Store  { return s.meta }
func (s *Service) Now() time.Time         { return s.nowFn() }
func (s *Service) Config() *config.Config { return s.config() }
func (s *Service) Metrics() MetricsSink   { return s.metrics }

// Validate exposes the single-job admission check (project/agent/runner allowlist +
// exec gate) so the workflow engine validates every step through the SAME gate
// (安全要点). It wraps the private validate unchanged.
func (s *Service) Validate(cfg *config.Config, req JobRequest, remote bool) (config.ProjectConfig, error) {
	return s.validate(cfg, req, remote)
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

// snapshot returns a copy of the entry's current result under its lock.
func (e *jobEntry) snapshot() JobResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.result
}

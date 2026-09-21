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

// Timeout bounds (plan §9 P4, §11; bd h-aii-s9ck). TimeoutSec defaults to
// DefaultTimeoutSec when unset and is clamped to the ceiling the CALLER resolves
// (config.Config.EffectiveMaxTimeoutSec — server.max_job_timeout_sec, overridable
// per project by max_timeout_sec). cli-agent jobs (claude/codex sessions run long)
// get their own larger default DefaultAgentTimeoutSec.
const (
	DefaultTimeoutSec      = 300
	DefaultAgentTimeoutSec = 1200
	// DefaultMaxTimeoutSec is the clamp ceiling for a caller that configured none
	// (normalizeTimeout's max <= 0). It is aliased to config.DefaultMaxJobTimeoutSec
	// so both layers agree on one literal — the value this clamp used to be
	// hard-coded to — and a ceiling-less caller still cannot run a job unbounded.
	DefaultMaxTimeoutSec = config.DefaultMaxJobTimeoutSec
)

// JobIDLayout is the time prefix for a job id (no separators that would clash
// with directory names). A random suffix makes it unique across process
// restarts (plan §9 P4: a seconds+seq scheme collides after a restart).
const JobIDLayout = "20060102-150405"

// jobIDCreateRetries bounds how many times Submit re-rolls a colliding id.
const jobIDCreateRetries = 5

// builtinLocalRunner is the runner key that needs no config declaration,
// mirroring the built-in exec agent. It is the CANONICAL key: a caller may also
// spell it "server" (config.BuiltinLocalRunnerAlias), and normalizeRunner
// translates that onto this key at every input boundary.
const builtinLocalRunner = config.BuiltinLocalRunner

// builtinPtyRunner is the runner key an interactive job is routed to (WEB-03).
// It is registered by core ONLY when a pty backend is available; the job service
// selects it by key (submit.go) so it never imports the pty runner package.
const builtinPtyRunner = "pty"

// builtinACPRunner is the runner key an acp-agent job is routed to (ACP-01 S0).
// core always registers it (internal/runner/acp); the job service selects it by key
// for the same reason as the pty runner, and refuses the job when it is absent
// (running the ACP server's argv through the plain local runner would exec an agent
// that never receives its prompt).
const builtinACPRunner = "acp"

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
	// ErrUnknownRole is returned when JobRequest.Role references a role not present
	// in cfg.Roles (E35). HTTP layer maps it to 400 (via submitStatus default).
	ErrUnknownRole = errors.New("unknown role")
	// ErrNoEligibleWorker is returned when a worker job supplies worker_labels but
	// no connected worker advertises all of them (or all such workers are stale).
	// HTTP layer maps it to 503 (temporarily unavailable — retry / pick another).
	ErrNoEligibleWorker = errors.New("no eligible worker")
	// ErrJobNotFound is returned by the job-scoped management helpers (WT-01
	// WorktreeStatus / RemoveWorktree) for an id that no job has. The HTTP layer maps
	// it to 404 (the Get-based read paths signal the same condition with ok=false; a
	// helper that must distinguish "no such job" from "no worktree" needs a sentinel).
	ErrJobNotFound = errors.New("unknown job")
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

	// eventObserver, when set, receives the whitelisted events a REMOTE executor must
	// mirror to the machine that submitted the job (SUP-01 G). Set only by the worker
	// client (SetEventObserver); nil everywhere else, where it is a no-op on the
	// event path.
	eventObserver atomic.Pointer[JobEventObserver]

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
	// agentSems holds per-agent concurrency semaphores (JOB-11,
	// agents.<key>.max_concurrent). Same lazy-create + fixed-capacity model as sems
	// (guarded by s.mu); an agent with no limit (<= 0) gets nil (no gating).
	agentSems map[string]chan struct{}
	// dirLock serializes the exclusive jobs that share a working directory (JOB-11).
	// It is process-scoped: the machine that RUNS a job is the one that locks its
	// directory (a dispatched remote job is locked by the worker's own Service).
	dirLock *dirLocks

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

	// adoptWake is the RECOV-01 R4 adoption signal: a capacity-1 channel pinged (never
	// blocking) whenever an adoption runs, so serve's one-shot startup recovery window
	// can re-check whether any `recovering` row is still unclaimed instead of failing
	// jobs a reconnecting worker already took back. Created in NewService; read-only
	// afterwards.
	adoptWake chan struct{}
	// answerGuard is the派生作答白名单闸 seam (监督分层升级路由 P3.1, design §8.5). It gates
	// an ATTRIBUTED driver answer (AnswerInteractionBy with a non-empty responder) so a
	// 通用 supervisor cannot answer outside the whitelist; owner/human are放行. nil = no gate
	// wired (unit tests / pure deployments) → AnswerInteractionBy is ungated. Injected by
	// core.Build via SetAnswerGuard; job never imports answerguard/presence/supervisor (G022).
	answerGuard AnswerGuard

	// terminalMu guards terminalHooks, the OUTBOUND seam of a terminal job (SUP-02
	// R1; see OnTerminal). The hooks are assembled once at startup and copied under
	// this mutex before each dispatch, so registering one never races a finish. The
	// job package defines the hook type, so it stays unaware of what the hub does
	// with an outcome it cannot see (a takeover job handing its session back).
	terminalMu    sync.Mutex
	terminalHooks []JobTerminalHook

	// xfer is the XFER-01 X2 file-transfer seam (see XferBridge): uploads placed
	// before the agent starts and collected files published after the job. Injected
	// at assemble time (core.Build on a hub, the worker command on a worker); nil
	// means this deployment has no transfer wiring, and a job that asked for files
	// fails (uploads) or records without moving bytes (collect).
	xfer XferBridge
}

// AnswerGuard is the job→answer-gate seam (监督分层升级路由 P3.1, design §8.5, dependency
// inversion like WorkflowAdvancer/MetricsSink): the job package defines it so it never imports
// the gate impl (internal/answerguard) nor presence/supervisor. answerguard.Guard satisfies it
// structurally; core injects it via SetAnswerGuard. Check returns nil to allow the answer, or a
// non-nil error to refuse it (the interaction stays pending). Primitive params keep the seam
// free of job types.
type AnswerGuard interface {
	Check(responder, originAgent, itType string, hasOptions bool, prompt string) error
}

// SetAnswerGuard injects the派生作答白名单闸 (P3.1). Called once at assemble time
// (internal/core) after the service is built; passing nil (or never calling it) leaves the
// attributed-answer path ungated (AnswerInteractionBy then allows every responder).
func (s *Service) SetAnswerGuard(g AnswerGuard) { s.answerGuard = g }

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
// (安全要点). It wraps the private validate unchanged, after normalizing the
// runner spelling exactly as Submit does — a workflow step (or the schedule
// validator at internal/httpapi/schedule_handler.go) may name the built-in runner
// with either spelling, and it must not be admitted any differently.
func (s *Service) Validate(cfg *config.Config, req JobRequest, remote bool) (config.ProjectConfig, error) {
	req.Runner = normalizeRunner(cfg, req.Runner)
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
		case StatusWaitingDir:
			// JOB-11: a job parked on a directory lock is QUEUED for occupancy
			// purposes (it holds no execution slot yet), exactly like the jobs waiting
			// on the project/caller/agent semaphores — the in-flight gauge must keep
			// counting it, and the queue must not look empty behind a busy directory.
			st.Queued++
		case StatusRunning:
			st.Running++
		case StatusRecovering:
			// RECOV-01: a recovering job is still occupying an execution slot on its
			// worker (the worker process is expected back), so it counts as running —
			// otherwise the in-flight gauge would drop during every network blip.
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
	// adopted, when non-nil, is the RECOV-01 R4 adoption handle that drives this job:
	// the entry was rebuilt from the store by a serve process that did NOT start it, so
	// there is no execute/Run goroutine behind it. Set once at creation under Service.mu
	// before the entry is published in s.jobs; read under the same lock (adoptRecoveringJob).
	adopted *AdoptedJob
	// wt is the WT-01 managed worktree this job runs in (nil for a plain job). It is
	// set once at Submit, before the entry is published, and only read afterwards, so
	// it needs no lock: it carries the live paths the terminal capture (head sha /
	// commits ahead / two-section diff) probes. A job never created in this process
	// (adopted / read back from the store) has no entry at all.
	wt *worktreeRef
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
		agentSems:  map[string]chan struct{}{},
		dirLock:    newDirLocks(),
		adoptWake:  make(chan struct{}, 1),
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

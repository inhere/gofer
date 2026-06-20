package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

	// nowFn yields the current time; overridable in tests.
	nowFn func() time.Time
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
		projects: projects,
		agents:   agents,
		runners:  runners,
		meta:     meta,
		workers:  sel,
		newStore: func(base string) store.Store { return store.NewFileStore(base) },
		jobs:     map[string]*jobEntry{},
		sems:     map[string]chan struct{}{},
		nowFn:    time.Now,
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

// Submit validates the request, creates the result dir, persists the request and
// starts the job asynchronously. It returns the initial JobResult (status
// running) once the goroutine is launched. Validation/setup failures return an
// error and no job.
func (s *Service) Submit(req JobRequest) (JobResult, error) {
	// Snapshot the config ONCE for the whole Submit so a concurrent Reload cannot
	// make this single submit observe two different configs (peer classification,
	// validation, result base dir all read the same snapshot).
	cfg := s.config()

	// A remote runner (peer-http OR ws-worker) forwards the original request to a
	// remote executor that resolves agent/cwd/command with its OWN config. The host
	// therefore skips local agent/cwd resolution for remote jobs (it still validates
	// the project, the agent allowlist and the runner allowlist).
	remote := isRemoteRunner(cfg, req.Runner)

	proj, err := s.validate(cfg, req, remote)
	if err != nil {
		return JobResult{}, err
	}

	// Resolve the target worker for a worker runner when worker_id was not given
	// explicitly (P2: labels → auto-select, else the runner's configured default).
	// Done right after validate so the chosen id rides the Forward + JobResult.
	if err := s.selectTargetWorker(cfg, &req); err != nil {
		return JobResult{}, err
	}

	// C5 idempotency: if this request carries an idempotency key already claimed
	// by an earlier job, reuse it (no new job/dir). The concurrent-submit race
	// (two submits both miss this lookup) is caught below by the unique-index
	// conflict on the first persist.
	if req.RequestID != "" {
		if rec, ok, gerr := s.meta.GetJobByRequestID(req.RequestID); gerr != nil {
			return JobResult{}, gerr
		} else if ok {
			return fromRecord(rec), nil
		}
	}

	// Resolve cwd to an absolute host dir inside the project root. Skipped for
	// remote jobs: the cwd is an opaque relative path the peer SafeJoins against
	// its own project root.
	var workDir string
	if !remote {
		workDir, err = project.SafeJoin(proj, req.Cwd)
		if err != nil {
			return JobResult{}, err
		}
	}

	// Result base dir + a collision-resistant job id; create the dir up front.
	// The host keeps a local result dir even for proxied jobs so its logs (mirrored
	// from the peer) and DB index entry stay queryable.
	base, err := project.ResultBaseDir(cfg, req.ProjectKey, proj)
	if err != nil {
		return JobResult{}, err
	}
	st := s.newStore(base)
	jobID, err := s.createJobDir(st)
	if err != nil {
		return JobResult{}, err
	}
	resultDir := st.Dir(jobID)

	// Marshal the original request for audit / re-submit. It rides along on the
	// entry's result so every persist (queued/running/terminal) carries it into the
	// jobs.request_json column (SP5: replaces the on-disk request.json file).
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return JobResult{}, fmt.Errorf("marshal request: %w", err)
	}

	run := s.runners[req.Runner]
	if run == nil {
		return JobResult{}, fmt.Errorf("runner %q is not available", req.Runner)
	}

	// Build the runner request. Remote jobs carry the ORIGINAL request in Forward
	// and leave Command/Args/WorkDir unset; local jobs resolve the executable form
	// (exec uses req.Cmd; cli-agent renders the prompt with cwd/job_id/result_dir).
	runReq := runner.Request{JobID: jobID, WorkDir: workDir}
	if remote {
		runReq.Forward = &runner.Forward{
			ProjectKey: req.ProjectKey,
			Agent:      req.Agent,
			PeerRunner: builtinLocalRunner,
			Prompt:     req.Prompt,
			Cmd:        req.Cmd,
			Cwd:        req.Cwd,
			TimeoutSec: req.TimeoutSec,
			// P2: the resolved target worker (explicit req.WorkerID or label-selected
			// in selectTargetWorker). Empty for peer-http and for worker jobs relying
			// on the runner's configured default (D4).
			WorkerID: req.WorkerID,
		}
		// Bridge the peer's running-job interactions (P9) onto this host job.
		runReq.Interactions = remoteInteractionSink{s: s, jobID: jobID}
	} else {
		resolved, berr := s.agents.Build(req.Agent, req.Prompt, req.Cmd, agent.Vars{
			Cwd:       workDir,
			JobID:     jobID,
			ResultDir: resultDir,
		})
		if berr != nil {
			return JobResult{}, berr
		}
		runReq.Command = resolved.Command
		runReq.Args = resolved.Args
		// Inject gofer-owned job metadata env so ANY job type (exec or cli-agent)
		// can locate its result dir / cwd / id. exec argv is executed verbatim
		// (no {{result_dir}} templating, unlike cli-agent args) — env is the only
		// channel an exec wrapper has to find <result_dir> for writing E1 artifacts
		// / E6 result.json. Set on the worker/peer side too (they run this same
		// local branch), so remote exec jobs get the executor-local paths.
		runReq.Env = goferJobEnv(resolved.Env, jobID, workDir, resultDir)
	}

	now := s.nowFn().Unix()
	entry := &jobEntry{
		store: st,
		done:  make(chan struct{}),
		result: JobResult{
			ID:          jobID,
			ProjectKey:  req.ProjectKey,
			Agent:       req.Agent,
			Runner:      req.Runner,
			Title:       req.Title,
			WorkerID:    req.WorkerID,
			Status:      StatusQueued,
			Cwd:         workDir,
			ResultDir:   resultDir,
			StartedAt:   now,
			RequestJSON: string(reqJSON),
			CallerID:    req.CallerID,
			RequestID:   req.RequestID,
			Tags:        req.Tags,
		},
	}
	s.mu.Lock()
	s.jobs[jobID] = entry
	s.mu.Unlock()

	// Record the initial (queued) snapshot in the metadata store. Capture the
	// error so the C5 concurrent-submit race can be recovered: if a competing
	// submit with the SAME request_id won the unique index, this insert returns
	// ErrRequestIDConflict and we hand back the winner instead of launching a
	// duplicate job. For the no-request_id case the write stays best-effort
	// (legacy behaviour: ignore the error, the entry lives in memory).
	persistErr := s.persist(entry.snapshot())
	if req.RequestID != "" && errors.Is(persistErr, jobstore.ErrRequestIDConflict) {
		// Lost the race: drop our just-created entry + dir and return the winner.
		s.mu.Lock()
		delete(s.jobs, jobID)
		s.mu.Unlock()
		_ = os.RemoveAll(resultDir)
		if rec, ok, gerr := s.meta.GetJobByRequestID(req.RequestID); gerr != nil {
			return JobResult{}, gerr
		} else if ok {
			return fromRecord(rec), nil
		}
		// The winner's row is unexpectedly absent (should not happen, since the
		// conflict means a row with this request_id exists); surface the conflict.
		return JobResult{}, persistErr
	}

	sem := s.semaphore(req.ProjectKey, proj.MaxConcurrentJobs)
	timeout := normalizeTimeout(req.TimeoutSec)
	go s.execute(entry, run, sem, runReq, timeout)

	return entry.snapshot(), nil
}

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

// execute runs the job: it acquires the project concurrency slot, opens the log
// files, runs the command under a timeout context and persists the terminal
// status to the metadata store. While the job waits for a slot it stays in
// `queued`.
func (s *Service) execute(entry *jobEntry, run runner.Runner, sem chan struct{}, req runner.Request, timeout time.Duration) {
	defer close(entry.done)

	// Establish the cancellable context first so a cancel issued while the job is
	// still queued (waiting for a concurrency slot) is honoured too.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	entry.mu.Lock()
	entry.cancel = cancel
	entry.mu.Unlock()

	// Wait for a project concurrency slot (if limited), but abort if cancelled
	// while queued.
	if sem != nil {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-ctx.Done():
			status, code, runErr := classify(ctx, runner.Result{ExitCode: -1})
			s.finish(entry, req.JobID, status, code, runErr)
			return
		}
	}

	entry.mu.Lock()
	entry.result.Status = StatusRunning
	snap := entry.result
	entry.mu.Unlock()

	// Persist a running snapshot so a crash leaves an inspectable DB row.
	_ = s.persist(snap)

	stdout, errOut := entry.store.LogWriter(req.JobID, store.StreamStdout)
	if errOut != nil {
		s.finish(entry, req.JobID, StatusFailed, -1, fmt.Errorf("open stdout log: %w", errOut))
		return
	}
	defer stdout.Close()
	stderr, errErr := entry.store.LogWriter(req.JobID, store.StreamStderr)
	if errErr != nil {
		s.finish(entry, req.JobID, StatusFailed, -1, fmt.Errorf("open stderr log: %w", errErr))
		return
	}
	defer stderr.Close()

	req.Stdout = stdout
	req.Stderr = stderr
	res := run.Run(ctx, req)

	// 产出与审计(job-outcomes-audit)：在终态前 best-effort 采集产出
	// (渲染命令/结构化结果/…)，写入 entry.result，由随后的 finish 一并 persist。
	// 绝不影响 job 终态(classify/finish 不受其结果影响)。res 携带远端回传的 Outcome
	// (worker/peer)，captureOutcomes 据此分流：远端直接落、本地扫盘(P4)。
	s.captureOutcomes(entry, req, res)

	status, code, runErr := classify(ctx, res)
	s.finish(entry, req.JobID, status, code, runErr)
}

// finish records the terminal state for a job: it updates the in-memory snapshot,
// upserts the terminal row into the metadata store, then evicts the entry from
// the in-memory map so memory stays bounded by the live (queued/running/
// pending_interaction) set rather than the historical job count (SP3, design §8 —
// roots out the C1 in-memory unbounded growth).
//
// Eviction order matters for concurrency: it removes the entry from s.jobs only;
// it does NOT touch entry.done. The execute goroutine still closes entry.done via
// its deferred close after finish returns, so any Wait caller that already grabbed
// the entry pointer (before eviction) still unblocks and snapshots the terminal
// result — the eviction only severs the map lookup for future callers, which then
// fall back to the metadata store (see Wait/Get/Cancel).
func (s *Service) finish(entry *jobEntry, jobID, status string, exitCode int, err error) {
	entry.mu.Lock()
	entry.result.Status = status
	entry.result.ExitCode = exitCode
	entry.result.EndedAt = s.nowFn().Unix()
	if err != nil {
		entry.result.Error = err.Error()
	}
	snap := entry.result
	entry.mu.Unlock()

	// Record the terminal snapshot in the metadata store FIRST, so the DB always
	// has the terminal row before the entry stops being reachable in memory: a
	// reader that misses the evicted entry must find the terminal state in the DB.
	//
	// Evict ONLY when that write durably succeeded. Otherwise the in-memory entry
	// is the job's sole surviving copy (it never reached the DB), so we keep it in
	// the map rather than lose the job. Live-job memory is then bounded by the
	// (near-zero, given Store.writeMu) count of jobs whose terminal write failed,
	// not by history — C1's invariant still holds.
	persistErr := s.persist(snap)
	if persistErr == nil && isTerminal(status) {
		s.mu.Lock()
		delete(s.jobs, jobID)
		s.mu.Unlock()
	}
}

// persist upserts one JobResult snapshot into the metadata store, stamping
// UpdatedAt with the current time. It returns the write error so finish can gate
// eviction on a durable terminal write; non-terminal callers (queued/running/
// interaction snapshots, where the entry stays in memory) ignore it best-effort.
func (s *Service) persist(snap JobResult) error {
	snap.UpdatedAt = s.nowFn().Unix()
	return s.meta.UpsertJob(toRecord(snap))
}

// toRecord projects a JobResult onto the neutral jobstore.JobRecord written to
// SQLite. SP5 carries RequestJSON into the request_json column (the on-disk
// request.json file is no longer written). WorkerID is mapped through for
// ws-worker jobs (jobs.worker_id already exists from C1; no migration).
func toRecord(r JobResult) jobstore.JobRecord {
	return jobstore.JobRecord{
		ID:          r.ID,
		ProjectKey:  r.ProjectKey,
		Agent:       r.Agent,
		Runner:      r.Runner,
		WorkerID:    r.WorkerID,
		Status:      r.Status,
		ExitCode:    r.ExitCode,
		Cwd:         r.Cwd,
		ResultDir:   r.ResultDir,
		RequestJSON: r.RequestJSON,
		Error:       r.Error,
		StartedAt:   r.StartedAt,
		EndedAt:     r.EndedAt,
		UpdatedAt:   r.UpdatedAt,
		CallerID:    r.CallerID,
		RequestID:   r.RequestID,
		// 产出与审计字段（job-outcomes-audit）。
		RenderedCommand: r.RenderedCommand,
		ResultJSON:      r.ResultJSON,
		ArtifactsJSON:   r.ArtifactsJSON,
		DiffSummary:     r.DiffSummary,
		Source:          r.Source,
		TagsJSON:        marshalTags(r.Tags),
	}
}

// marshalTags 把 tags 序列化为 tags_json 入库原文（E5）。best-effort：空/失败存 ""。
func marshalTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return ""
	}
	return string(b)
}

// unmarshalTags 把 tags_json 原文反序列化为 tags（E5）。空/非法返回 nil（omitempty 不出现）。
func unmarshalTags(s string) []string {
	if s == "" {
		return nil
	}
	var t []string
	if json.Unmarshal([]byte(s), &t) != nil {
		return nil
	}
	return t
}

// fromRecord rebuilds a JobResult from a persisted jobstore.JobRecord. It is the
// read path for ListJobs/Get when a job is not (or no longer) in memory.
func fromRecord(rec jobstore.JobRecord) JobResult {
	return JobResult{
		ID:          rec.ID,
		ProjectKey:  rec.ProjectKey,
		Agent:       rec.Agent,
		Runner:      rec.Runner,
		Title:       titleFromRequestJSON(rec.RequestJSON),
		WorkerID:    rec.WorkerID,
		Status:      rec.Status,
		ExitCode:    rec.ExitCode,
		Cwd:         rec.Cwd,
		ResultDir:   rec.ResultDir,
		RequestJSON: rec.RequestJSON,
		StartedAt:   rec.StartedAt,
		EndedAt:     rec.EndedAt,
		UpdatedAt:   rec.UpdatedAt,
		Error:       rec.Error,
		CallerID:    rec.CallerID,
		RequestID:   rec.RequestID,
		// 产出与审计字段（job-outcomes-audit）。
		RenderedCommand: rec.RenderedCommand,
		ResultJSON:      rec.ResultJSON,
		ArtifactsJSON:   rec.ArtifactsJSON,
		DiffSummary:     rec.DiffSummary,
		Source:          rec.Source,
		Tags:            unmarshalTags(rec.TagsJSON),
	}
}

// titleFromRequestJSON recovers the optional job Title from the persisted
// request_json blob. The jobs table has no title column (SP5), so the DB read
// path (fromRecord) parses it back out of the stored JobRequest to keep Title
// round-tripping through Get/ListJobs.
func titleFromRequestJSON(s string) string {
	if s == "" {
		return ""
	}
	var t struct {
		Title string `json:"title"`
	}
	_ = json.Unmarshal([]byte(s), &t)
	return t.Title
}

// classify maps a runner result + context state to a job status, exit code and
// error. The context reason distinguishes timeout from cancellation.
func classify(ctx context.Context, res runner.Result) (string, int, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		switch {
		case errors.Is(ctxErr, context.DeadlineExceeded):
			return StatusTimeout, res.ExitCode, fmt.Errorf("job timed out")
		case errors.Is(ctxErr, context.Canceled):
			return StatusCancelled, res.ExitCode, fmt.Errorf("job cancelled")
		}
	}
	if res.Err != nil {
		return StatusFailed, res.ExitCode, res.Err
	}
	if res.ExitCode != 0 {
		return StatusFailed, res.ExitCode, nil
	}
	return StatusDone, 0, nil
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

// createJobDir generates a unique job id and creates its result dir, retrying on
// collision with a fresh suffix (plan §9 P4 cross-restart uniqueness).
func (s *Service) createJobDir(st store.Store) (string, error) {
	var lastErr error
	for i := 0; i < jobIDCreateRetries; i++ {
		id := s.genJobID()
		err := st.Ensure(id)
		if err == nil {
			return id, nil
		}
		if errors.Is(err, os.ErrExist) {
			lastErr = err
			continue
		}
		// A real filesystem error (permission, etc.) is not retryable.
		return "", err
	}
	return "", fmt.Errorf("could not allocate unique job id: %w", lastErr)
}

// genJobID returns a timestamp-prefixed id with a random hex suffix, e.g.
// 20060102-150405-1a2b3c4d. The random suffix guarantees uniqueness across
// process restarts (a seconds+in-memory-seq scheme would collide on restart).
func (s *Service) genJobID() string {
	ts := s.nowFn().Format(jobIDLayout)
	return ts + "-" + randomSuffix()
}

// randomSuffix returns 8 lowercase hex chars from crypto/rand, falling back to a
// nanosecond-derived value if the RNG is unavailable.
func randomSuffix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(b[:])
}

// normalizeTimeout applies the default and clamps to the max (plan §9 P4).
func normalizeTimeout(sec int) time.Duration {
	if sec <= 0 {
		sec = DefaultTimeoutSec
	}
	if sec > MaxTimeoutSec {
		sec = MaxTimeoutSec
	}
	return time.Duration(sec) * time.Second
}

// snapshot returns a copy of the entry's current result under its lock.
func (e *jobEntry) snapshot() JobResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.result
}

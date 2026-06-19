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
)

// Service accepts job requests, runs them asynchronously and tracks their state.
// It is safe for concurrent use.
type Service struct {
	cfg      *config.Config
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
// must be non-nil — the caller (commands.buildCore / tests) opens it.
func NewService(cfg *config.Config, projects *project.Registry, agents *agent.Registry, runners map[string]runner.Runner, meta *jobstore.Store) *Service {
	return &Service{
		cfg:      cfg,
		projects: projects,
		agents:   agents,
		runners:  runners,
		meta:     meta,
		newStore: func(base string) store.Store { return store.NewFileStore(base) },
		jobs:     map[string]*jobEntry{},
		sems:     map[string]chan struct{}{},
		nowFn:    time.Now,
	}
}

// Submit validates the request, creates the result dir, persists the request and
// starts the job asynchronously. It returns the initial JobResult (status
// running) once the goroutine is launched. Validation/setup failures return an
// error and no job.
func (s *Service) Submit(req JobRequest) (JobResult, error) {
	// A peer-http runner forwards the original request to a peer bridge, which
	// resolves agent/cwd/command with its OWN config. The host therefore skips
	// local agent/cwd resolution for remote jobs (it still validates the project,
	// the agent allowlist and the runner allowlist).
	remote := s.isPeerRunner(req.Runner)

	proj, err := s.validate(req, remote)
	if err != nil {
		return JobResult{}, err
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
	base, err := project.ResultBaseDir(s.cfg, req.ProjectKey, proj)
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
		runReq.Env = resolved.Env
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
			Status:      StatusQueued,
			Cwd:         workDir,
			ResultDir:   resultDir,
			StartedAt:   now,
			RequestJSON: string(reqJSON),
			CallerID:    req.CallerID,
		},
	}
	s.mu.Lock()
	s.jobs[jobID] = entry
	s.mu.Unlock()

	// Record the initial (queued) snapshot in the metadata store.
	_ = s.persist(entry.snapshot())

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
// request.json file is no longer written). WorkerID stays empty (reserved for
// the ws-worker runner).
func toRecord(r JobResult) jobstore.JobRecord {
	return jobstore.JobRecord{
		ID:          r.ID,
		ProjectKey:  r.ProjectKey,
		Agent:       r.Agent,
		Runner:      r.Runner,
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
	}
}

// fromRecord rebuilds a JobResult from a persisted jobstore.JobRecord. It is the
// read path for ListJobs/Get when a job is not (or no longer) in memory.
func fromRecord(rec jobstore.JobRecord) JobResult {
	return JobResult{
		ID:          rec.ID,
		ProjectKey:  rec.ProjectKey,
		Agent:       rec.Agent,
		Runner:      rec.Runner,
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
	}
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
// runner that forwards the job to a peer bridge). Such runners resolve the
// agent/command on the peer, so the host skips local exec resolution.
func (s *Service) isPeerRunner(name string) bool {
	rc, ok := s.cfg.Runners[name]
	return ok && rc.Type == "peer-http"
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
func (s *Service) validate(req JobRequest, remote bool) (config.ProjectConfig, error) {
	proj, err := s.projects.Get(req.ProjectKey)
	if err != nil {
		return config.ProjectConfig{}, fmt.Errorf("%w: %s", ErrUnknownProject, err.Error())
	}

	// Agent must be in the project's allowed_agents (exec is not exempt).
	if err := agent.CheckAllowed(s.cfg, req.ProjectKey, req.Agent); err != nil {
		return config.ProjectConfig{}, fmt.Errorf("%w: %s", ErrInvalidRequest, err.Error())
	}

	if !remote {
		// exec security gate: the agent must be type exec AND the project must opt
		// in. Skipped for remote jobs — the peer enforces its own exec gate.
		ac, ok := s.agents.Get(req.Agent)
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
	return proj, nil
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

package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"dev-agent-bridge/internal/agent"
	"dev-agent-bridge/internal/config"
	"dev-agent-bridge/internal/project"
	"dev-agent-bridge/internal/runner"
	"dev-agent-bridge/internal/store"
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
}

// NewService builds a job service. runners is the set of usable runners keyed by
// name (at least "local"). project/agent registries and config come from the
// loaded config.
func NewService(cfg *config.Config, projects *project.Registry, agents *agent.Registry, runners map[string]runner.Runner) *Service {
	return &Service{
		cfg:      cfg,
		projects: projects,
		agents:   agents,
		runners:  runners,
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
	proj, err := s.validate(req)
	if err != nil {
		return JobResult{}, err
	}

	// Resolve cwd to an absolute host dir inside the project root.
	workDir, err := project.SafeJoin(proj, req.Cwd)
	if err != nil {
		return JobResult{}, err
	}

	// Result base dir + a collision-resistant job id; create the dir up front.
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

	if err := st.WriteRequest(jobID, req); err != nil {
		return JobResult{}, fmt.Errorf("write request: %w", err)
	}

	// Build the executable form (exec uses req.Cmd; cli-agent renders the prompt
	// with cwd/job_id/result_dir vars).
	resolved, err := s.agents.Build(req.Agent, req.Prompt, req.Cmd, agent.Vars{
		Cwd:       workDir,
		JobID:     jobID,
		ResultDir: resultDir,
	})
	if err != nil {
		return JobResult{}, err
	}

	run := s.runners[req.Runner]
	if run == nil {
		return JobResult{}, fmt.Errorf("runner %q is not available", req.Runner)
	}

	now := s.nowFn().Unix()
	entry := &jobEntry{
		store: st,
		done:  make(chan struct{}),
		result: JobResult{
			ID:         jobID,
			ProjectKey: req.ProjectKey,
			Agent:      req.Agent,
			Runner:     req.Runner,
			Status:     StatusQueued,
			Cwd:        workDir,
			ResultDir:  resultDir,
			StartedAt:  now,
		},
	}
	s.mu.Lock()
	s.jobs[jobID] = entry
	s.mu.Unlock()

	sem := s.semaphore(req.ProjectKey, proj.MaxConcurrentJobs)
	timeout := normalizeTimeout(req.TimeoutSec)
	go s.execute(entry, run, sem, runner.Request{
		JobID:   jobID,
		WorkDir: workDir,
		Command: resolved.Command,
		Args:    resolved.Args,
		Env:     resolved.Env,
	}, timeout)

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
// files, runs the command under a timeout context and records the terminal
// status + result.json. While the job waits for a slot it stays in `queued`.
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

	// Persist a running snapshot so a crash leaves an inspectable result.json.
	_ = entry.store.WriteResult(req.JobID, snap)

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

// finish records the terminal state for a job: it updates the in-memory snapshot
// and atomically rewrites result.json.
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

	_ = entry.store.WriteResult(jobID, snap)
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

// validate enforces the project/agent/runner/exec allowlists (plan §11) and
// returns the resolved project config.
func (s *Service) validate(req JobRequest) (config.ProjectConfig, error) {
	proj, err := s.projects.Get(req.ProjectKey)
	if err != nil {
		return config.ProjectConfig{}, err
	}

	// Agent must be in the project's allowed_agents (exec is not exempt).
	if err := agent.CheckAllowed(s.cfg, req.ProjectKey, req.Agent); err != nil {
		return config.ProjectConfig{}, err
	}

	// exec security gate: the agent must be type exec AND the project must opt in.
	ac, ok := s.agents.Get(req.Agent)
	if !ok {
		return config.ProjectConfig{}, fmt.Errorf("unknown agent %q", req.Agent)
	}
	if ac.Type == agent.TypeExec && !proj.AllowExec {
		return config.ProjectConfig{}, fmt.Errorf("exec agent %q not allowed: project %q has allow_exec=false", req.Agent, req.ProjectKey)
	}

	// Runner must be in allowed_runners ("local" is a built-in default).
	if req.Runner == "" {
		return config.ProjectConfig{}, fmt.Errorf("runner is required")
	}
	if err := checkRunnerAllowed(proj, req.Runner); err != nil {
		return config.ProjectConfig{}, err
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

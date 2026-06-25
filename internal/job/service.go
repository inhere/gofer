package job

import (
	"context"
	"encoding/json"
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

// execute runs the job: it acquires the project concurrency slot, opens the log
// files, runs the command under a timeout context and persists the terminal
// status to the metadata store. While the job waits for a slot it stays in
// `queued`.
func (s *Service) execute(entry *jobEntry, run runner.Runner, sem, callerSem chan struct{}, req runner.Request, timeout time.Duration) {
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

	// E17: after the project slot, wait for the per-caller slot (design §7.2).
	// Superseding the quota means QUEUING (the job stays `queued`), not rejecting —
	// same削峰 semantics as the project semaphore. Acquired AFTER project so the
	// deferred releases unwind caller-then-project (reverse LIFO order), and a job
	// holding a project slot never blocks indefinitely on a caller slot another
	// project-slot holder is waiting to release.
	if callerSem != nil {
		select {
		case callerSem <- struct{}{}:
			defer func() { <-callerSem }()
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
	// E13: queued -> running transition is now a fact.
	s.recordEvent(req.JobID, EventJobRunning, nil)

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
	// E13: record the terminal event BEFORE the terminal status becomes observable.
	// The in-memory status flip below is visible via Get() immediately (the entry
	// is still in s.jobs) and persist() then exposes it from the DB too — so any
	// reader/SSE that keys off terminal status (waitDone; the stream's terminal→
	// `end` close) could otherwise observe "done" and read the event log BEFORE this
	// row lands, missing the terminal frame (and racing teardown into a closed DB).
	// Recording it first reflects the already-DECIDED terminal outcome (status/
	// exitCode/err are final here) and closes that race. best-effort — never gates
	// the status flip / persist / eviction.
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	s.recordEvent(jobID, EventJobTerminal, map[string]any{
		"status":    status,
		"exit_code": exitCode,
		"error":     errStr,
	})

	entry.mu.Lock()
	entry.result.Status = status
	entry.result.ExitCode = exitCode
	entry.result.EndedAt = s.nowFn().Unix()
	if err != nil {
		entry.result.Error = err.Error()
	}
	snap := entry.result
	entry.mu.Unlock()

	// E16: count the terminal + observe the submit→terminal duration (incl. queue
	// wait, design §6.3). nil-safe. Duration is clamped at 0 in case clock skew /
	// an unset StartedAt would make it negative.
	if s.metrics != nil {
		dur := float64(snap.EndedAt - snap.StartedAt)
		if dur < 0 {
			dur = 0
		}
		s.metrics.JobTerminal(status, snap.CallerID, snap.ProjectKey, snap.Agent, snap.Runner, dur)
	}

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

	// 工作流推进 (E7)：若此 job 属于某工作流，其终态可能解锁下一步。异步推进，绝不阻塞
	// finish/不改 entry.done 时序(execute 的 defer close(entry.done) 仍照常触发)。
	// advanceWorkflow 幂等(条件 UPDATE 抢推进权)，与 sweeper 叠加安全；persist 已先落终态
	// 行，故 advance 读到的 step job 状态已是终态。非工作流 job(WorkflowID=="")完全不触发。
	if snap.WorkflowID != "" {
		go s.advanceWorkflow(snap.WorkflowID)
		return
	}

	// E24 统一 job 级重试 (P1 最小版)：非工作流 job 若带 Retry 策略且本次失败可重试，
	// 进程内延迟重投 attempt+1。工作流 step 的重试走 advanceWorkflow（上面 return），
	// 二者不重叠。可靠版（sweeper 驱动 next_retry_at）留后续。
	s.maybeRetryJob(snap)
}

// maybeRetryJob implements the E24 unified job-level retry (P1 最小版, design §6.2)
// for a non-workflow job. It re-runs a failed job (attempt+1) when its JobRequest
// carries a Retry policy, the attempt budget is not exhausted, and the exit code is
// retryable — sharing the SAME RetryPolicy / backoffFor / retryableExit as the
// step-level retry (one semantics). The retry is scheduled with an in-process
// time.AfterFunc after the policy's backoff; a process restart loses a pending
// retry (the可靠版 sweeper-driven path is left for后续, see JobRequest.Retry doc).
//
// It is a no-op when: the job succeeded, the status is not a failure (cancelled /
// timeout are NOT retried — a cancel is intentional, and a timeout means the work
// itself overran), the request carries no Retry, the budget is spent, or the exit
// code is not in OnExitCodes. A nil/parse-failed request is also a no-op.
func (s *Service) maybeRetryJob(snap JobResult) {
	if snap.Status != StatusFailed {
		return // only a plain failure is retried (cancel/timeout are terminal-by-intent)
	}
	var req JobRequest
	if snap.RequestJSON == "" || json.Unmarshal([]byte(snap.RequestJSON), &req) != nil {
		return
	}
	// CallerID / WorkflowID / Attempt are not part of the client-facing JSON (tag
	// "-"), so restore them from the persisted snapshot for the re-submit.
	req.CallerID = snap.CallerID
	if req.Retry == nil {
		return
	}
	attempt := snap.Attempt
	if attempt < 1 {
		attempt = 1
	}
	if attempt >= maxAttemptsPolicy(req.Retry) || !retryableExitPolicy(req.Retry, snap.ExitCode) {
		return // budget spent or this exit code is not retryable
	}
	backoff := backoffForPolicy(req.Retry, attempt)
	next := req // copy: a fresh job for attempt+1
	next.Attempt = attempt + 1
	next.RequestID = "" // job-level retry: each attempt is a distinct NEW job (no C5 dedupe)
	next.Sync = false   // a re-run is always async (the original caller already returned)
	time.AfterFunc(time.Duration(backoff)*time.Second, func() {
		if _, err := s.Submit(next); err != nil {
			// best-effort: a failed re-submit is logged, never panics. The original
			// terminal state stands.
			s.recordEvent(snap.ID, EventJobTerminal, map[string]any{
				"retry_resubmit_error": err.Error(), "attempt": next.Attempt,
			})
		}
	})
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
		// 工作流(job 链)：step-job 反向关联其 workflow + 1-based 步序号 + 重试 attempt。
		WorkflowID: r.WorkflowID,
		StepIndex:  r.StepIndex,
		Attempt:    r.Attempt,
		FanIndex:   r.FanIndex, // P2: fan-out 并行序号
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
		// 工作流(job 链)。
		WorkflowID: rec.WorkflowID,
		StepIndex:  rec.StepIndex,
		Attempt:    rec.Attempt,
		FanIndex:   rec.FanIndex, // P2: fan-out 并行序号
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

// snapshot returns a copy of the entry's current result under its lock.
func (e *jobEntry) snapshot() JobResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.result
}

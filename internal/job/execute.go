package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/store"
)

// execGates are the gates a job's execute() passes before it runs (JOB-11): the
// project / caller / agent concurrency semaphores (nil = ungated, the job runs at
// once) plus the RESOLVED exclusive-directory flag. They are bundled because Submit
// resolves them all from ONE config snapshot and hands them over together — a struct
// keeps that hand-off readable instead of a five-argument tail.
type execGates struct {
	project   chan struct{}
	caller    chan struct{}
	agent     chan struct{}
	exclusive bool // hold the same-directory lock for this job's WorkDir
	stall     int  // AUTO-05: kill a job silent for this many seconds (0 = off)
}

// execute runs the job: it acquires the project concurrency slot, opens the log
// files, runs the command under a timeout context and persists the terminal
// status to the metadata store. While the job waits for a slot (or for the
// exclusive directory lock) it stays in a non-running holding state (`queued` /
// `waiting_dir`).
func (s *Service) execute(entry *jobEntry, run runner.Runner, gates execGates, req runner.Request, timeout time.Duration) {
	defer close(entry.done)

	// Establish the cancellable context first so a cancel issued while the job is
	// still queued (waiting for a concurrency slot) is honoured too.
	ctx, cancel := context.WithCancel(context.Background())
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
	}
	defer cancel()
	entry.mu.Lock()
	entry.cancel = cancel
	// F3: a cancel that landed between Submit publishing this entry and this line is
	// honoured now — that recorded intent is the only trace it left (see jobEntry).
	cancelledEarly := entry.cancelRequested
	entry.mu.Unlock()
	if cancelledEarly {
		cancel()
	}

	// Wait for a project concurrency slot (if limited), but abort if cancelled
	// while queued.
	if gates.project != nil {
		select {
		case gates.project <- struct{}{}:
			defer func() { <-gates.project }()
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
	if gates.caller != nil {
		select {
		case gates.caller <- struct{}{}:
			defer func() { <-gates.caller }()
		case <-ctx.Done():
			status, code, runErr := classify(ctx, runner.Result{ExitCode: -1})
			s.finish(entry, req.JobID, status, code, runErr)
			return
		}
	}

	// JOB-11: the per-agent slot (agents.<key>.max_concurrent), last of the three
	// semaphores so a job releases them in reverse order and a saturated agent never
	// holds a project slot another project is waiting for. Same queuing semantics.
	if gates.agent != nil {
		select {
		case gates.agent <- struct{}{}:
			defer func() { <-gates.agent }()
		case <-ctx.Done():
			status, code, runErr := classify(ctx, runner.Result{ExitCode: -1})
			s.finish(entry, req.JobID, status, code, runErr)
			return
		}
	}

	// JOB-11: serialize the jobs that must not share a working directory. Acquired
	// AFTER every concurrency slot (a job parked on a directory must not hold a slot
	// the directories do not need) and BEFORE the status flips to running, so the
	// whole wait is visible as `waiting_dir`. A managed-worktree job is skipped: its
	// directory is created for IT alone (<repo>/tmp/gofer/wt/<job-id>), so there is
	// nothing to serialize — and taking the ancestor lock of the enclosing checkout
	// would make every worktree job queue behind the main one, which is exactly the
	// parallelism WT-01 exists to provide.
	//
	// releaseDir is a no-op until a lock is taken, and Idempotent afterwards: every
	// finish below calls it FIRST, so the directory is free before the terminal row is
	// observable (a workflow step advancing out of finish() may want this very
	// directory), while the deferred call still covers a panic.
	releaseDir := func() {}
	if gates.exclusive && entry.wt == nil {
		release, holder, waited, derr := s.dirLock.Acquire(ctx, req.WorkDir, req.JobID, func(blocker string) {
			s.enterWaitingDir(entry, req.JobID, blocker, req.WorkDir)
		})
		if derr != nil {
			// Cancelled while queued (or the wait raced with a cancel): the job ends
			// through the same path as a cancelled semaphore wait.
			status, code, runErr := classify(ctx, runner.Result{ExitCode: -1})
			s.finish(entry, req.JobID, status, code, runErr)
			return
		}
		releaseDir = release
		defer func() { releaseDir() }()
		if waited {
			slog.Info("job.dir_wait", "job_id", req.JobID, "holder", holder, "dir", req.WorkDir)
		}
	}

	entry.mu.Lock()
	// The holder is no longer interesting once this job is the holder: it is cleared
	// in the same critical section that flips the status, so no reader can see a
	// running job that still claims to be waiting.
	entry.result.WaitingOnJob = ""
	entry.result.Status = StatusRunning
	entry.result.RenderedCommand = renderedCommandJSON(req)
	// SUP-01 C: the commit the job starts from, captured HERE (the executing machine,
	// before the agent runs — a worker re-enters this very function, so its row gets
	// its own checkout's HEAD and the outcome carries it back). A worktree job already
	// knows its baseline; outside a git checkout the capture yields "" rather than an
	// error, and the job runs exactly as before.
	entry.result.BaseSHA = captureBaseSHA(entry.result.WorktreeBaseSHA, entry.result.Cwd)
	snap := entry.result
	entry.mu.Unlock()

	// Persist a running snapshot so a crash leaves an inspectable DB row.
	// best-effort：失败不阻断执行，但记一条 warning，否则 DB 行与内存态静默漂移、无从排查。
	if err := s.persist(snap); err != nil {
		slog.Warn("persist running snapshot", "job_id", req.JobID, "err", err)
	}
	// E13: queued -> running transition is now a fact.
	s.recordEvent(req.JobID, EventJobRunning, nil)

	stdout, errOut := entry.store.LogWriter(req.JobID, store.StreamStdout)
	if errOut != nil {
		releaseDir()
		s.finish(entry, req.JobID, StatusFailed, -1, fmt.Errorf("open stdout log: %w", errOut))
		return
	}
	// Closure (not `defer stdout.Close()`): stdout is REBOUND below to the ndjson
	// capture wrapper, and this safety net must close whichever writer is live.
	defer func() { _ = stdout.Close() }()
	stderr, errErr := entry.store.LogWriter(req.JobID, store.StreamStderr)
	if errErr != nil {
		releaseDir()
		s.finish(entry, req.JobID, StatusFailed, -1, fmt.Errorf("open stderr log: %w", errErr))
		return
	}
	defer stderr.Close()

	// 结构化输出采集（bd h-aii-rpky / bd h-aii-525u）：ndjson agent 的 stdout 在被交给
	// runner 之前先包一层投影器 —— 逐 token 增量事件不入盘，中间过程进 stderr.log（紧凑
	// 事件行），stdout.log 只留 agent 的最终答复。只在本进程真正执行（local runner）时包
	// —— 远端 (worker/peer) job 的两路日志是执行机投影后镜像回来的，host 侧再包一层只会
	// 把已投影的流投影第二遍，并把 raw 旁路记成误导性内容。文本 agent 原样返回。
	stdout = s.captureNDJSON(entry, req.JobID, run.Name(), stdout, stderr)

	// AUTO-05: the stall watchdog measures SILENCE, so every non-empty write from the
	// agent (either stream — including the acp runner's own update lines on stderr) has
	// to count as activity. The wrapper is the RUNNER's view of both streams (outermost:
	// a line the ndjson projector drops still proves the agent is alive); the close /
	// accounting path below keeps using stdout/stderr themselves.
	mark := func() { s.markOutput(entry) }
	req.Stdout = activityWriter{w: stdout, mark: mark}
	req.Stderr = activityWriter{w: stderr, mark: mark}
	// G1: a remote runner (worker/peer) reports its rendered command out-of-band as
	// soon as it starts — the host can't render a remote agent's argv (line 65 above
	// yields "" for those). Apply it to the running entry at once so job show/web
	// reflect WHAT is running immediately, not only at completion. Local runs set it
	// inline above and never invoke this (OnRendered stays effectively unused).
	req.OnRendered = func(rendered string) {
		s.setRunningRenderedCommand(entry, req.JobID, rendered)
	}
	// RECOV-01: a worker connection drop holds the job in `recovering` instead of
	// failing it (the hub decides; the runner only relays what the hub reported), and
	// a successful reconnect returns it to `running`. Both are no-ops for a local job
	// (the runner never calls them) and for a job that already reached terminal.
	req.OnSuspend = func(reason string) { s.setRecovering(entry, req.JobID, reason) }
	req.OnResume = func() { s.clearRecovering(entry, req.JobID) }
	// RECOV-01 R4: the remote runner resolves its target worker at dispatch time
	// (including the D4 default-worker fallback), so the host row learns the resolved
	// worker_id + the process instance that owns the job from the runner — and only
	// then can a later serve process decide whether a reconnecting worker may ADOPT
	// this job. A no-op for local jobs (the runner never calls it).
	req.OnDispatchedWorker = func(workerID, instanceID string) {
		s.setDispatchedWorker(entry, req.JobID, workerID, instanceID)
	}
	// ACP-01: a runner may report the job events it observes (the acp runner's
	// job.permission_* rows and its one job.acp_summary per turn). Detail stays bounded
	// by recordEvent's own cap. A no-op for runners that never call it.
	req.OnJobEvent = func(eventType string, detail map[string]any) {
		s.recordEvent(req.JobID, eventType, detail)
	}
	// XFER-01 X2: the job's staged uploads are placed in its cwd BEFORE the agent
	// starts. A job that cannot be given its files must not run at all — the agent
	// would work on a cwd the caller did not describe, and its output would look like
	// a success. A remote job's uploads are placed by the EXECUTING machine
	// (req.Forward != nil here: this machine has no cwd of that job).
	if req.Forward == nil {
		// JOB-10: the job's skills are materialized in its OWN result dir before the
		// agent starts (not in the cwd — see skills.go). A job whose rules cannot be
		// placed must not run: an agent told to read a missing SKILL.md would guess.
		if err := s.mountSkills(entry, req); err != nil {
			_ = stdout.Close()
			_ = stderr.Close()
			releaseDir()
			s.finish(entry, req.JobID, StatusFailed, -1, err)
			return
		}
		if err := s.materializeUploads(ctx, entry, req, req.WorkDir, snap.ProjectKey, snap.ResultDir); err != nil {
			// Close the logs before the terminal state becomes observable, exactly as
			// the normal path does (an observer must never see "terminal" with the
			// files still open; on Windows that alone can block the dir's deletion).
			_ = stdout.Close()
			_ = stderr.Close()
			releaseDir()
			s.finish(entry, req.JobID, StatusFailed, -1, err)
			return
		}
	}
	// AUTO-05: arm the output-stall watchdog right before the agent starts. Only a
	// LOCAL job: a remote job's process lives on the worker, whose own job.Service
	// runs this very code with the window the hub resolved and sent with the dispatch —
	// watching this machine's log MIRROR instead would kill the job in the wrong place.
	if req.Forward == nil {
		stopWatchdog := s.startStallWatchdog(ctx, entry, req.JobID, gates.stall)
		defer stopWatchdog()
	}
	res := run.Run(ctx, req)

	// ACP-01: a runner may learn facts about the session it just drove beyond the
	// exit code — the acp runner returns the agent's sessionId (the uniform resume
	// entry point) and its session/prompt stopReason. Applied BEFORE
	// captureOutcomes so the terminal capture sees them (a non-empty SessionID also
	// suppresses regex session capture, which is for cli-agents). SUP-01 E: the same
	// runner reports the agent's token/cost tally through Usage.
	if res.SessionID != "" || res.StopReason != "" || res.Usage != nil {
		entry.mu.Lock()
		if res.SessionID != "" && entry.result.SessionID == "" {
			entry.result.SessionID = res.SessionID
		}
		if res.StopReason != "" {
			entry.result.StopReason = res.StopReason
		}
		if res.Usage != nil {
			u := *res.Usage
			entry.result.Usage = &u
		}
		entry.mu.Unlock()
	}

	// SUP-01 P2: the verify step runs on the machine that did the work. For a LOCAL
	// job that is right here (req.Forward is nil); a remote job's step runs on the
	// worker, whose result comes back on the outcome — running it again against the
	// host's checkout would verify a tree the job never touched.
	if req.Forward == nil {
		// AUTO-05: the verify step has its own deadline and may legitimately print
		// nothing for minutes, so the stall clock is suspended for its duration and
		// restarted fresh afterwards (that silence was not the agent's).
		s.pauseStall(entry)
		s.runVerify(ctx, entry, req, res)
		s.resumeStall(entry)
		// XFER-01 X2: collect runs LAST on that same machine — after the verify step,
		// and whatever the job's status is (a failed run's partial output is exactly
		// what the caller wants back). A remote job's files are collected by the
		// worker and pulled back by the hub once its outcome arrives (captureOutcomes).
		s.collectFiles(ctx, entry, req, req.WorkDir)
	}

	// Close the per-job log streams NOW, before finish() makes the terminal
	// state observable (persist + eviction + workflow advance). Observers key
	// teardown off the terminal DB row / Get() — not entry.done — and on
	// Windows an open stdout.log/stderr.log blocks deletion of the job dir, so
	// "terminal ⇒ log handles closed" must hold for every observer, not just
	// Wait callers. The deferred closes above stay as a panic safety net; a
	// second Close is a harmless (ignored) error.
	_ = stdout.Close()
	_ = stderr.Close()

	// 结构化采集结果（bd h-aii-rpky / bd h-aii-525u）：投影器已 flush + 关闭，计数即
	// 最终值；把保留/丢弃/截断行数与投影出的 session_id 写入 entry.result，由紧随其后的
	// captureOutcomes/finish 一并 persist（审计字段 + 会话捕获优先于正则兜底）。
	s.recordNDJSONCapture(entry, req.JobID, stdout)

	// 产出与审计(job-outcomes-audit)：在终态前 best-effort 采集产出
	// (渲染命令/结构化结果/…)，写入 entry.result，由随后的 finish 一并 persist。
	// 绝不影响 job 终态(classify/finish 不受其结果影响)。res 携带远端回传的 Outcome
	// (worker/peer)，captureOutcomes 据此分流：远端直接落、本地扫盘(P4)。
	s.captureOutcomes(entry, req, res)

	status, code, runErr := classify(ctx, res)
	// SUP-01 P2: a verify step that did not pass decides the job's status (the
	// result is already recorded locally or, for a remote job, applied by
	// captureOutcomes above).
	status, code, runErr = s.foldVerify(entry, status, code, runErr)
	// AUTO-05: a stall the watchdog killed IS the job's outcome — it replaces whatever
	// the killed runner reported (a cancel would otherwise classify as `cancelled`,
	// which is neither a failure nor retried). The message starts with `stalled:`,
	// which the built-in transient patterns match, so the auto-resume / failover chain
	// takes over from a hung provider instead of burning the job's whole deadline.
	if stallErr := entry.takeStall(); stallErr != nil {
		status, code, runErr = StatusFailed, -1, stallErr
	}
	releaseDir()
	s.finish(entry, req.JobID, status, code, runErr)
}

// enterWaitingDir parks a job on the same-directory lock (JOB-11): it flips the
// status to `waiting_dir` with the holder that blocked it, persists that holding
// snapshot (so a crash leaves an inspectable row rather than a job that looks
// queued for no reason) and records job.waiting_dir. Like the semaphore waits, the
// job is NOT running yet: no execution slot, no process — the state is queuing.
func (s *Service) enterWaitingDir(entry *jobEntry, jobID, holder, dir string) {
	// E13 ordering, same as finish(): the event that explains the state lands
	// BEFORE the state is observable, so a reader that sees waiting_dir (Get /
	// SSE / the web timeline) always finds the job.waiting_dir row naming the
	// holder — recording it after the flip let TestDirLockSerializesWritableAgentJobs
	// observe the status with no event yet under load.
	s.recordEvent(jobID, EventJobWaitingDir, map[string]any{"holder_job": holder, "dir": dir})

	entry.mu.Lock()
	entry.result.Status = StatusWaitingDir
	entry.result.WaitingOnJob = holder
	snap := entry.result
	entry.mu.Unlock()

	if err := s.persist(snap); err != nil {
		slog.Warn("persist waiting_dir snapshot", "job_id", jobID, "err", err)
	}
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
	// The outcome is DECIDED before anything becomes observable, so the one event
	// that describes it can be recorded first (the E13 invariant above):
	//   - needsReview (GATE-01 S3): a job that asked for人工验收 and finished
	//     NORMALLY parks in needs_review instead of done — no job.terminal event, no
	//     workflow advance, no retry and no auto-resume apply to work that is not
	//     accepted yet;
	//   - willAutoResume (v0.42): a transient failure the source agent can continue
	//     is announced as job.auto_resumed instead of job.terminal (an IM subscriber
	//     must not see a "failed" it is about to be spared) — the eligibility check
	//     is pure (config, session, attempt budget, stderr pattern) so it can run
	//     here; the continuation itself is submitted after the row is persisted, and
	//     a submit that fails then falls back to the late job.terminal below.
	entry.mu.Lock()
	// The failure's REASON is part of the state the takeover decision reads — the
	// transient pattern match looks at snap.Error, and a stall (AUTO-05) IS only
	// described there (the child printed nothing) — so it is recorded here, before the
	// decision below is taken. This is where the terminal row's error comes from.
	if err != nil {
		entry.result.Error = err.Error()
	}
	pre := entry.result
	entry.mu.Unlock()
	// SUP-01 P2: a failed verify step is a delivery a reviewer must rule on, so it
	// parks in needs_review exactly like a normal finish does (the failing Verify
	// stays on the result for the human, and there is no job.terminal yet).
	needsReview := pre.RequireReview && (status == StatusDone || verifyBlocked(pre.Verify))
	// SUP-01 P3: a failure is classified and (possibly) taken over from ONE decision —
	// the pattern match, whether this agent can still continue the work, and whether
	// the candidate chain has a link left. It is pure (it submits nothing), so it runs
	// here, before the failure becomes observable.
	dec := failureDecision{}
	if status == StatusFailed {
		dec = s.failureDecision(pre)
	}
	switch {
	case needsReview:
		s.recordEvent(jobID, EventJobNeedsReview, map[string]any{"job_id": jobID, "exit_code": exitCode})
	case dec.AutoResume || dec.Fallback != "":
		// A takeover is about to be submitted (the same agent's continuation, or the
		// next candidate): its own event is recorded once the submission succeeds, and
		// job.terminal is recorded late instead when it does not.
	default:
		s.recordEvent(jobID, EventJobTerminal, map[string]any{"status": status, "exit_code": exitCode, "error": errStr})
	}
	entry.mu.Lock()
	entry.result.Status = status
	entry.result.ExitCode = exitCode
	entry.result.EndedAt = s.nowFn().Unix()
	// SUP-01 P3: the classification rides the terminal row, unconditionally — health
	// must describe what the provider did, not which policies this job happened to
	// have enabled.
	entry.result.FailureClass = dec.Class
	// entry.result.Error was already set from `err` above (the decision reads it).
	// needs_review is set under the same lock that flips the status, so no reader
	// can ever observe a transient `done` (which would let a watcher report the
	// delivery as accepted and skip the review).
	if needsReview {
		entry.result.Status = StatusNeedsReview
	}
	snap := entry.result
	// 终态对账（E25, 复审 #4）：把残留 pending interaction 翻为 cancelled。否则一个
	// 在 pending_interaction 上结束/被取消的 job 会在 DB 留下僵尸 pending 行
	// （ListPendingInteractions 会误报、监督者会去答一个死 job），且任何阻塞在
	// WaitAnswer 的 in-process 等待者永不被唤醒。在设置终态状态的同一临界区里做，所以
	// CreateInteraction/AnswerInteraction（都查 IsTerminal）不会与之竞争。收集要落库的
	// 快照 + 要关闭的 channel，出锁后再持久化/唤醒（避免在锁内做 IO/close）。
	var cancelled []Interaction
	var toWake []chan struct{}
	for _, rec := range entry.interactions {
		if rec.data.Status == InteractionPending {
			rec.data.Status = InteractionCancelled
			rec.data.AnsweredAt = snap.EndedAt
			cancelled = append(cancelled, rec.data)
			toWake = append(toWake, rec.answered)
		}
	}
	entry.mu.Unlock()

	// Persist each cancelled interaction (best-effort) and wake its WaitAnswer
	// callers — they observe the cancelled snapshot (Status=cancelled), not a hang.
	for _, it := range cancelled {
		if uerr := s.meta.UpsertInteraction(toInteractionRecord(it)); uerr != nil {
			slog.Warn("upsert cancelled interaction on job finish", "job_id", jobID, "interaction_id", it.ID, "err", uerr)
		}
		s.recordEvent(jobID, EventInteractionAnswered, map[string]any{
			"interaction_id": it.ID,
			"status":         InteractionCancelled,
		})
	}
	for _, ch := range toWake {
		close(ch)
	}

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
	// SUP-01 C: the checklist item this job carries follows its outcome — before the
	// needs_review return below, so both a delivered-but-unreviewed job and a terminal
	// one are recorded on the todo. Best-effort: a todo write must never affect a job.
	if persistErr == nil {
		s.linkTodoOutcome(snap)
		// SUP-01 P3 / design §六: the provider's verdict for THIS agent may have just
		// changed — announce the transition (best-effort, and a no-op unless it moved).
		// It runs before the needs_review return below on purpose: a delivery parked for
		// review IS a provider success, so it can be the job that shows an agent
		// recovered.
		s.noteAgentHealth(snap)
	}
	// GATE-01 S3: the needs_review branch REPLACES the terminal one — the job is
	// FINISHED (its process is gone: evict it, close its SSE/log teardown) but not
	// terminal, and the only event is job.needs_review. No workflow advance, no
	// job-level retry, no automatic continuation happen here: they all wait for a
	// human's accept/reject (review.go).
	if needsReview {
		if persistErr == nil && isFinished(snap.Status) {
			s.mu.Lock()
			delete(s.jobs, jobID)
			s.mu.Unlock()
		}
		return
	}
	autoResumed, fellBack := false, false
	if persistErr == nil && dec.AutoResume {
		autoResumed = s.autoResume(snap, dec.Hit)
	}
	if persistErr == nil && dec.Fallback != "" {
		fellBack = s.fallBack(snap, dec.Hit, dec.Fallback)
	}
	if (dec.AutoResume && !autoResumed) || (dec.Fallback != "" && !fellBack) {
		// The takeover could not be submitted (or the row never landed): the failure
		// IS terminal after all — record it now, late but never missing.
		s.recordEvent(jobID, EventJobTerminal, map[string]any{"status": status, "exit_code": exitCode, "error": errStr})
	}
	if persistErr == nil && isFinished(status) {
		s.mu.Lock()
		delete(s.jobs, jobID)
		s.mu.Unlock()
	}

	// SUP-02 R1: the job is finished, durable and evicted — the moment the hub gets
	// to react (a terminal path-B takeover job hands its session back). Dispatched
	// BEFORE the workflow/retry branches below, because those return early and the
	// outcome is already final: neither of them un-terminals THIS job (a retry runs
	// as a new one).
	s.notifyTerminalHooks(snap)

	// PLAN-03: a chain job that really ENDED in failure parks its plan. This runs here
	// — after the takeover attempts above — because the persisted auto_resumed_by /
	// fell_back_to markers are what say the failure is being continued, and only the
	// row has them by now (a failure whose takeover could not be submitted fell through
	// to the late job.terminal and blocks, which is the correct reading of "it is over").
	if persistErr == nil {
		s.maybeBlockPlan(snap)
	}

	// 工作流推进 (E7)：若此 job 属于某工作流，其终态可能解锁下一步。异步推进，绝不阻塞
	// finish/不改 entry.done 时序(execute 的 defer close(entry.done) 仍照常触发)。
	// advanceWorkflow 幂等(条件 UPDATE 抢推进权)，与 sweeper 叠加安全；persist 已先落终态
	// 行，故 advance 读到的 step job 状态已是终态。非工作流 job(WorkflowID=="")完全不触发。
	if s.wf != nil && snap.WorkflowID != "" {
		go s.wf.Advance(snap.WorkflowID)
		return
	}

	// E24 统一 job 级重试 (P1 最小版)：非工作流 job 若带 Retry 策略且本次失败可重试，
	// 进程内延迟重投 attempt+1。工作流 step 的重试走 advanceWorkflow（上面 return），
	// 二者不重叠。可靠版（sweeper 驱动 next_retry_at）留后续。
	s.maybeRetryJob(snap)
}

// transientHit is the PATTERN-MATCHING half of the failure classification (SUP-01 P3):
// it reports whether a failed job's output carries one of its agent's transient
// (provider-error) patterns, and returns the matched text. It is PURE with respect to
// policy: it never looks at whether a continuation or a transfer is enabled, so the
// persisted failure_class reflects what actually happened to the provider. The
// evidence is the tail of stderr (8KB) plus the job's own error string — a provider
// error that ends the process without printing anything still lands in Error when the
// runner reported one.
//
// The agent's configured transient_error_patterns are the authority (built-in
// defaults per agent name, see agent.applySessionDefaults).
func (s *Service) transientHit(snap JobResult) (string, bool) {
	// SUP-01 P2: a failed verify step is evidence about the WORK, not a provider
	// glitch — re-running the same agent prompt would not repair it, and the
	// continuation would report success over a still-red build. The step's own status
	// is the authority (never its error text), so a step that passed or was skipped
	// leaves the ordinary transient machinery untouched.
	if verifyBlocked(snap.Verify) {
		return "", false
	}
	patterns := s.transientPatternsFor(snap)
	if len(patterns) == 0 {
		return "", false
	}
	var sb strings.Builder
	if b, err := os.ReadFile(filepath.Join(snap.ResultDir, "stderr.log")); err == nil {
		if len(b) > 8192 {
			b = b[len(b)-8192:]
		}
		sb.Write(b)
	}
	if snap.Error != "" {
		sb.WriteString("\n")
		sb.WriteString(snap.Error)
	}
	text := sb.String()
	if text == "" {
		return "", false
	}
	for _, p := range patterns {
		re, e := regexp.Compile("(?i)" + p)
		if e != nil {
			continue
		}
		if m := re.FindString(text); m != "" {
			if len(m) > 120 {
				m = m[:120]
			}
			return m, true
		}
	}
	return "", false
}

// transientPatternsFor resolves the transient-error patterns that apply to a failed
// job. They are normally the job's own agent's; a CONTINUATION CARRIER is the
// exception — a resume runs the SOURCE agent's CLI out of a built-in exec job, so its
// patterns are the source agent's. Without that hop a codex continuation dying of a
// provider error would be classified as an unrelated failure: it would never be handed
// to the next candidate, and the provider outage would be invisible to health.
func (s *Service) transientPatternsFor(snap JobResult) []string {
	if ac, ok := s.agents.Get(snap.Agent); ok && len(ac.TransientErrorPatterns) > 0 {
		return ac.TransientErrorPatterns
	}
	if snap.ResumedFrom == "" {
		return nil
	}
	src, ok := s.Get(snap.ResumedFrom)
	if !ok {
		return nil
	}
	ac, ok := s.agents.Get(src.Agent)
	if !ok {
		return nil
	}
	return ac.TransientErrorPatterns
}

// autoResumeEligible is the ELIGIBILITY half of automatic continuation (v0.42, split
// out in SUP-01 P3): auto-resume enabled with budget left, a session to continue, and
// an agent that can resume it. It answers only "may the SAME agent be asked to carry
// on?" — the transient pattern match is transientHit's business, so the two can be
// consulted independently (the failure class needs one, the takeover decision needs
// both).
func (s *Service) autoResumeEligible(snap JobResult) bool {
	cfg := s.config()
	if cfg == nil || cfg.Server.EffectiveAutoResumeMax() <= 0 || snap.SessionID == "" || snap.AutoResumeAttempt >= cfg.Server.EffectiveAutoResumeMax() {
		return false
	}
	ac, ok := s.agents.Get(snap.Agent)
	return ok && resumable(ac)
}

// autoResume submits an automatic continuation decided by failureDecision: a resume
// of the failed job with a prompt naming the transient error, one attempt deeper. The
// source row records the continuation (AutoResumedBy) and job.auto_resumed; the
// caller records job.terminal instead when this returns false.
func (s *Service) autoResume(snap JobResult, hit string) bool {
	prompt := "The previous run was interrupted by a transient error (" + strings.TrimSpace(hit) + "). Check git status / git log to see how far you got, finish only the remaining work, do not redo committed work, then report as originally asked."
	res, err := s.resumeJob(snap.ID, prompt, snap.Runner, snap.CallerID, snap.AutoResumeAttempt+1, nil)
	if err != nil {
		return false
	}
	snap.AutoResumedBy = res.ID
	_ = s.persist(snap)
	s.recordEvent(snap.ID, "job.auto_resumed", map[string]any{"job_id": res.ID})
	return true
}

// maybeRetryJob SCHEDULES the next attempt of a failed job as a durable row
// (R2/AUTO-03, design §二.1/§二.2). It no longer submits anything: the serve
// retry sweeper owns submission (Service.SweepDueRetries), which is what makes a
// pending retry survive a process restart — the in-process time.AfterFunc it
// replaces lost one on every restart.
//
// The policy comes from the four config layers (request > project > agent >
// server, config.EffectiveRetryPolicy); an absent policy — or an explicit
// MaxAttempts <= 1 — means retry is OFF and nothing at all is written, so a config
// that never mentioned retry keeps the pre-R2 behaviour.
//
// It is a no-op when:
//   - the status is not a plain failure (cancelled / timeout are terminal-by-intent,
//     and a needs_review job never reaches here — finish returns before this);
//   - the failure is TRANSIENT (FailureClassTransient): the auto-resume / AUTO-05
//     stall / fallback machinery owns those, and retrying on top of a takeover would
//     run the same work twice (design §二.2 边界);
//   - the policy is absent/off, the exit code is not in OnExitCodes, or the request
//     could not be parsed.
//
// The ONE thing it records besides the row: when the attempt budget is already
// spent, job.retry_exhausted — the signal that gofer has given up (it is in the
// notification default set).
func (s *Service) maybeRetryJob(snap JobResult) {
	if snap.Status != StatusFailed {
		return // only a plain failure is retried (cancel/timeout are terminal-by-intent)
	}
	if snap.FailureClass == FailureClassTransient {
		return // the takeover family (auto-resume / stall / fallback) owns transient failures
	}
	var req JobRequest
	if snap.RequestJSON == "" || json.Unmarshal([]byte(snap.RequestJSON), &req) != nil {
		return
	}
	policy := s.config().EffectiveRetryPolicy(snap.ProjectKey, snap.Agent, req.Retry)
	if policy == nil || MaxAttemptsPolicy(policy) <= 1 {
		return // retry is off at every layer (or explicitly off at the nearest one)
	}
	if !RetryableExitPolicy(policy, snap.ExitCode) {
		return // this exit code is not retryable under the resolved policy
	}
	attempt := snap.Attempt
	if attempt < 1 {
		attempt = 1
	}
	if attempt >= MaxAttemptsPolicy(policy) {
		// The budget is spent and this failure is the last word. Nothing is retried
		// again — a human has to look (the event is a default notification trigger).
		s.recordEvent(snap.ID, EventJobRetryExhausted, map[string]any{"attempts": attempt})
		return
	}
	// The row carries the re-submittable request plus the fields that cannot ride in
	// it: Attempt / CallerID / SourceJobID are json:"-" on JobRequest (they are not
	// client-settable), so Attempt lives in the row's own column and the lineage is
	// restored from the source job when the sweeper submits (see SweepDueRetries).
	next := req
	next.Attempt = attempt + 1
	next.RequestID = "" // each attempt is a distinct NEW job (no C5 dedupe)
	next.Sync = false   // a re-run is always async (the original caller already returned)
	// Stamp the RESOLVED policy onto the re-submitted request. The row then explains
	// itself (its own attempt ceiling, backoff table and exit-code filter travel with
	// it: the sweeper's retry-of-a-failed-submit backoff and the `attempt N/M` a human
	// reads are answered by the row alone), and a chain keeps the policy it was
	// scheduled under even if the config layer it came from changes mid-chain.
	next.Retry = policy
	body, err := json.Marshal(next)
	if err != nil {
		slog.Warn("retry: marshal request", "job_id", snap.ID, "err", err)
		return
	}
	now := s.nowFn().Unix()
	rec := jobstore.RetryRecord{
		ID:          jobstore.NewRetryID(),
		SourceJobID: snap.ID,
		Attempt:     next.Attempt,
		RequestJSON: string(body),
		Reason:      fmt.Sprintf("exit_code=%d", snap.ExitCode),
		NextRunAt:   now + int64(BackoffForPolicy(policy, attempt)),
		CreatedAt:   now,
	}
	if err := s.meta.InsertRetry(rec); err != nil {
		// The failure itself is already terminal and durable; a retry we could not
		// record must be visible, never silent (mirrors the old resubmit-error event).
		slog.Warn("retry: insert row", "job_id", snap.ID, "err", err)
		s.recordEvent(snap.ID, EventJobTerminal, map[string]any{
			"retry_schedule_error": err.Error(), "attempt": next.Attempt,
		})
		return
	}
	s.recordEvent(snap.ID, EventJobRetryScheduled, map[string]any{
		"retry_id": rec.ID, "attempt": rec.Attempt, "next_run_at": rec.NextRunAt, "reason": rec.Reason,
	})
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

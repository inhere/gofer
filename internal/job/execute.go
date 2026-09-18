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

	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/store"
)

// execute runs the job: it acquires the project concurrency slot, opens the log
// files, runs the command under a timeout context and persists the terminal
// status to the metadata store. While the job waits for a slot it stays in
// `queued`.
func (s *Service) execute(entry *jobEntry, run runner.Runner, sem, callerSem chan struct{}, req runner.Request, timeout time.Duration) {
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
		s.finish(entry, req.JobID, StatusFailed, -1, fmt.Errorf("open stdout log: %w", errOut))
		return
	}
	// Closure (not `defer stdout.Close()`): stdout is REBOUND below to the ndjson
	// capture wrapper, and this safety net must close whichever writer is live.
	defer func() { _ = stdout.Close() }()
	stderr, errErr := entry.store.LogWriter(req.JobID, store.StreamStderr)
	if errErr != nil {
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

	req.Stdout = stdout
	req.Stderr = stderr
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
	// ACP-01: a runner may report job events it observes (the acp runner's
	// job.tool_call on a tool-call status change). Detail stays bounded by
	// recordEvent's own cap. A no-op for runners that never call it.
	req.OnJobEvent = func(eventType string, detail map[string]any) {
		s.recordEvent(req.JobID, eventType, detail)
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
		s.runVerify(ctx, entry, req, res)
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
	if err != nil {
		entry.result.Error = err.Error()
	}
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
	res, err := s.resumeJob(snap.ID, prompt, snap.Runner, snap.CallerID, snap.AutoResumeAttempt+1)
	if err != nil {
		return false
	}
	snap.AutoResumedBy = res.ID
	_ = s.persist(snap)
	s.recordEvent(snap.ID, "job.auto_resumed", map[string]any{"job_id": res.ID})
	return true
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
	// P5: SourceJobID 亦 json:"-"（不入 request_json），从快照恢复，使派生 job 的重试保留血缘。
	req.SourceJobID = snap.SourceJobID
	if req.Retry == nil {
		return
	}
	attempt := snap.Attempt
	if attempt < 1 {
		attempt = 1
	}
	if attempt >= MaxAttemptsPolicy(req.Retry) || !RetryableExitPolicy(req.Retry, snap.ExitCode) {
		return // budget spent or this exit code is not retryable
	}
	backoff := BackoffForPolicy(req.Retry, attempt)
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

package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	if s.wf != nil && snap.WorkflowID != "" {
		go s.wf.Advance(snap.WorkflowID)
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

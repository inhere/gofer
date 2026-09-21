package job

import (
	"fmt"
	"time"
)

// Get returns the current snapshot of a job. The in-memory map is authoritative
// for live jobs; when the id is not tracked in this process (e.g. after a
// restart, or — from SP3 — a finished job evicted from memory) it falls back to
// the metadata store. The boolean is false only when neither has the job.
func (s *Service) Get(id string) (JobResult, bool) {
	if entry := s.entry(id); entry != nil {
		return entry.snapshot(), true
	}
	if rec, ok, _ := s.meta.GetJob(id); ok {
		return fromRecord(rec), true
	}
	return JobResult{}, false
}

// GetPersisted returns a job snapshot. The metadata-store fallback now lives in
// Get (so the after-restart path is covered for every caller), making this a
// thin alias kept for the existing call sites. base is unused.
func (s *Service) GetPersisted(_ string, id string) (JobResult, bool) {
	return s.Get(id)
}

// SetSessionID records a newly observed underlying agent CLI session id for a
// job. It is best-effort and additive: an existing session_id is never replaced.
// The PTY relay uses this for interactive agents whose session id is printed to
// terminal output instead of stdout/stderr logs.
func (s *Service) SetSessionID(id, sessionID string) {
	if id == "" || sessionID == "" {
		return
	}
	if entry := s.entry(id); entry != nil {
		entry.mu.Lock()
		if entry.result.SessionID != "" {
			entry.mu.Unlock()
			return
		}
		entry.result.SessionID = sessionID
		snap := entry.result
		entry.mu.Unlock()
		_ = s.persist(snap)
		return
	}
	rec, ok, err := s.meta.GetJob(id)
	if err != nil || !ok || rec.SessionID != "" {
		return
	}
	rec.SessionID = sessionID
	rec.UpdatedAt = s.nowFn().Unix()
	_ = s.meta.UpsertJob(rec)
}

// Cancel requests cancellation of a running job. It is a stable no-op (returns
// nil) for an already-terminal job, so callers/tests get deterministic
// behaviour. It returns an error only when the job id is unknown.
//
// After SP3 a finished job is evicted from the in-memory map, so an entry==nil
// can mean "never existed" OR "already terminal and evicted". The metadata store
// disambiguates: a known (terminal) job cancels as a no-op (nil); only a truly
// unknown id is an error.
func (s *Service) Cancel(id string) error {
	entry := s.entry(id)
	if entry == nil {
		rec, ok, _ := s.meta.GetJob(id)
		if !ok {
			return fmt.Errorf("unknown job %q", id)
		}
		// GATE-01 S3: a needs_review job's process is over — there is nothing to
		// cancel, and silently returning nil would look like success. Say what the
		// caller actually wants (reject) instead.
		if rec.Status == StatusNeedsReview {
			return fmt.Errorf("%w: job %q is awaiting review (%s) — use `job reject %s --note ...` to refuse it", ErrJobNotRunning, id, StatusNeedsReview, id)
		}
		// Known but evicted => terminal; cancelling a terminal job is a no-op.
		return nil
	}

	entry.mu.Lock()
	status := entry.result.Status
	terminal := isTerminal(status)
	cancel := entry.cancel
	if !terminal && status != StatusNeedsReview && cancel == nil {
		// F3: the job is live but its execute goroutine has not installed the
		// cancellable context yet. Record the intent; execute honours it as soon as the
		// context exists, so the cancel is never dropped.
		entry.cancelRequested = true
	}
	entry.mu.Unlock()

	if status == StatusNeedsReview {
		return fmt.Errorf("%w: job %q is awaiting review (%s) — use `job reject %q --note ...` to refuse it", ErrJobNotRunning, id, StatusNeedsReview, id)
	}
	if terminal {
		// Already done/failed/cancelled/timeout/rejected: no-op, deterministic.
		return nil
	}
	// E13: a real cancellation is being issued on a live job. The subsequent
	// finish() records the job.terminal(cancelled) event; this marks the user
	// intent. was_terminal is false here (the terminal job path returned above).
	s.recordEvent(id, EventJobCancelled, map[string]any{"was_terminal": false})
	if cancel != nil {
		cancel()
	}
	return nil
}

// Wait blocks until the job reaches a terminal state, then returns its final
// snapshot. It is primarily a test/HTTP helper. The boolean is false for an
// unknown job id.
//
// After SP3 a finished job is evicted from the in-memory map. A Wait caller that
// grabbed the live entry before eviction still unblocks on entry.done (the
// execute goroutine closes it after finish returns) and returns the terminal
// snapshot. A Wait that arrives only after eviction sees entry==nil and falls
// back to the metadata store: a known job is already terminal, so its persisted
// record is returned immediately without blocking; an unknown id returns false.
func (s *Service) Wait(id string) (JobResult, bool) {
	entry := s.entry(id)
	if entry == nil {
		if rec, ok, _ := s.meta.GetJob(id); ok {
			return fromRecord(rec), true
		}
		return JobResult{}, false
	}
	<-entry.done
	return entry.snapshot(), true
}

// WaitFor blocks until the job reaches a terminal state OR timeout elapses. It
// is the bounded counterpart of Wait used by the synchronous-submit HTTP path
// (design §6.1 / P1-a).
//
// ok=false means the wait timed out (the job is still running in the background
// — it is NOT cancelled) or the id is unknown. A job already evicted to a
// terminal DB record returns ok=true immediately via the metadata-store fallback
// (mirrors Wait). timeout<=0 degrades to Wait (block forever; test-only).
func (s *Service) WaitFor(id string, timeout time.Duration) (JobResult, bool) {
	entry := s.entry(id)
	if entry == nil {
		if rec, ok, _ := s.meta.GetJob(id); ok {
			return fromRecord(rec), true
		}
		return JobResult{}, false
	}
	if timeout <= 0 {
		<-entry.done
		return entry.snapshot(), true
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-entry.done:
		return entry.snapshot(), true
	case <-t.C:
		// Not terminal yet: return the current (running) snapshot with ok=false so
		// the caller can fall back to async polling; the job keeps running.
		return entry.snapshot(), false
	}
}

// entry returns the tracked job entry for id, or nil.
func (s *Service) entry(id string) *jobEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobs[id]
}

// isTerminal reports whether a status is a final state.
func isTerminal(status string) bool {
	switch status {
	// StatusRejected is a terminal outcome too (GATE-01 S3): a human refused the
	// delivery, so the job is over for retention/workflow purposes — it simply never
	// retries or auto-continues on its own.
	case StatusDone, StatusFailed, StatusCancelled, StatusTimeout, StatusRejected:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether status is a finished state. It is the exported
// counterpart of isTerminal, used by callers outside the package (e.g. the SSE
// stream handler) to decide when to stop polling.
func IsTerminal(status string) bool { return isTerminal(status) }

// IsFinished reports whether a job's PROCESS has ended — the question every
// "is there anything left to watch/attach to?" decision asks. It is IsTerminal OR
// needs_review (GATE-01 S3): a needs_review job is deliberately NOT terminal (it
// awaits a human, so retention keeps it and `job resume` refuses it) but its agent
// process is gone, so the log tail/SSE stream must close, the in-memory entry must
// be evicted, an attach must be refused, and crash recovery must not treat it as a
// running job to recover.
func IsFinished(status string) bool { return isFinished(status) }

// isFinished is the unexported counterpart of IsFinished, used in-package.
func isFinished(status string) bool {
	return isTerminal(status) || status == StatusNeedsReview
}

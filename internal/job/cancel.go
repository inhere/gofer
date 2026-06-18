package job

import (
	"fmt"
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
		if _, ok, _ := s.meta.GetJob(id); ok {
			// Known but evicted => terminal; cancelling a terminal job is a no-op.
			return nil
		}
		return fmt.Errorf("unknown job %q", id)
	}

	entry.mu.Lock()
	terminal := isTerminal(entry.result.Status)
	cancel := entry.cancel
	entry.mu.Unlock()

	if terminal {
		// Already done/failed/cancelled/timeout: no-op, deterministic.
		return nil
	}
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

// entry returns the tracked job entry for id, or nil.
func (s *Service) entry(id string) *jobEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobs[id]
}

// isTerminal reports whether a status is a final state.
func isTerminal(status string) bool {
	switch status {
	case StatusDone, StatusFailed, StatusCancelled, StatusTimeout:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether status is a finished state. It is the exported
// counterpart of isTerminal, used by callers outside the package (e.g. the SSE
// stream handler) to decide when to stop polling.
func IsTerminal(status string) bool { return isTerminal(status) }

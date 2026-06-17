package job

import (
	"fmt"
)

// Get returns the current snapshot of a job. The boolean is false when no job
// with that id is tracked in this process. The in-memory map is authoritative
// for live jobs; callers that need historical jobs after a restart can fall
// back to reading result.json via the store (GetPersisted).
func (s *Service) Get(id string) (JobResult, bool) {
	entry := s.entry(id)
	if entry == nil {
		return JobResult{}, false
	}
	return entry.snapshot(), true
}

// GetPersisted returns a job snapshot, falling back to result.json on disk when
// the job is not in the in-process map (e.g. after a restart). base is the
// result base dir for the job's project.
func (s *Service) GetPersisted(base, id string) (JobResult, bool) {
	if r, ok := s.Get(id); ok {
		return r, true
	}
	var r JobResult
	if err := s.newStore(base).ReadResult(id, &r); err != nil {
		return JobResult{}, false
	}
	return r, true
}

// Cancel requests cancellation of a running job. It is a stable no-op (returns
// nil) for an already-terminal job, so callers/tests get deterministic
// behaviour. It returns an error only when the job id is unknown.
func (s *Service) Cancel(id string) error {
	entry := s.entry(id)
	if entry == nil {
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
func (s *Service) Wait(id string) (JobResult, bool) {
	entry := s.entry(id)
	if entry == nil {
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

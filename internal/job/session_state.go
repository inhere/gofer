package job

import (
	"errors"
	"time"
)

var errSessionTransition = errors.New("invalid ACP session job transition")

// beginSessionTurn records the first or a later prompt turn before ACP receives it.
func (s *Service) beginSessionTurn(entry *jobEntry) error {
	entry.mu.Lock()
	if !entry.result.Session || (entry.result.Status != StatusRunning && entry.result.Status != StatusAwaitingInput) ||
		(entry.result.Status == StatusRunning && entry.result.TurnNo != 0) {
		entry.mu.Unlock()
		return errSessionTransition
	}
	entry.result.Status = StatusRunning
	entry.sessionCommandPending = false
	entry.result.IdleDeadlineAt = 0
	entry.result.TurnNo++
	snap := entry.result
	entry.mu.Unlock()
	if err := s.persist(snap); err != nil {
		return err
	}
	s.recordEvent(snap.ID, EventJobTurnStarted, map[string]any{"turn_no": snap.TurnNo})
	return nil
}

// endSessionTurn closes one prompt without making the job terminal.
func (s *Service) endSessionTurn(entry *jobEntry, stopReason string) error {
	entry.mu.Lock()
	if !entry.result.Session || entry.result.Status != StatusRunning || entry.result.TurnNo < 1 {
		entry.mu.Unlock()
		return errSessionTransition
	}
	entry.result.StopReason = stopReason
	snap := entry.result
	entry.mu.Unlock()
	if err := s.persist(snap); err != nil {
		return err
	}
	s.recordEvent(snap.ID, EventJobTurnEnded, map[string]any{"turn_no": snap.TurnNo, "stop_reason": stopReason})
	return nil
}

// awaitSessionInput starts the idle clock after a completed turn or empty start.
func (s *Service) awaitSessionInput(entry *jobEntry) error {
	entry.mu.Lock()
	if !entry.result.Session || entry.result.Status != StatusRunning {
		entry.mu.Unlock()
		return errSessionTransition
	}
	entry.result.Status = StatusAwaitingInput
	if entry.result.IdleTimeoutSec > 0 {
		entry.result.IdleDeadlineAt = s.nowFn().Add(time.Duration(entry.result.IdleTimeoutSec) * time.Second).Unix()
	}
	snap := entry.result
	entry.mu.Unlock()
	if err := s.persist(snap); err != nil {
		return err
	}
	s.recordEvent(snap.ID, EventJobAwaitingInput, map[string]any{
		"turn_no": snap.TurnNo, "idle_deadline_at": snap.IdleDeadlineAt,
	})
	return nil
}

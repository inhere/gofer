package job

import (
	"errors"
	"log/slog"
	"time"

	"github.com/inhere/gofer/internal/runner"
)

var errSessionTransition = errors.New("invalid ACP session job transition")

func (s *Service) sessionReady(entry *jobEntry, id string) {
	entry.mu.Lock()
	entry.result.SessionID = id
	if entry.result.MaxSessionSec > 0 && entry.result.MaxSessionDeadlineAt == 0 {
		entry.result.MaxSessionDeadlineAt = s.nowFn().Add(time.Duration(entry.result.MaxSessionSec) * time.Second).Unix()
	}
	snap := entry.result
	_ = s.persist(snap)
	entry.mu.Unlock()
}

func (s *Service) configureResidentACP(entry *jobEntry, request *runner.ACPRequest, timeoutSec int) {
	request.SessionCommands = entry.sessionCommands
	request.TurnTimeoutSec = timeoutSec
	request.IdleTimeoutSec = entry.result.IdleTimeoutSec
	request.MaxSessionSec = entry.result.MaxSessionSec
	request.IdleDeadlineAt = entry.result.IdleDeadlineAt
	request.MaxSessionDeadlineAt = entry.result.MaxSessionDeadlineAt
	request.InitialTurnNo = entry.result.TurnNo
	request.OnSessionReady = func(id string) { s.sessionReady(entry, id) }
	request.OnTurnStart = func() error {
		if err := s.beginSessionTurn(entry); err != nil {
			return err
		}
		s.resumeStall(entry)
		return nil
	}
	request.OnTurnEnd = func(reason string) error { return s.endSessionTurn(entry, reason) }
	request.OnAwaitInput = func() error {
		if err := s.awaitSessionInput(entry); err != nil {
			return err
		}
		s.pauseStall(entry)
		return nil
	}
}

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
	if err := s.persist(snap); err != nil {
		entry.mu.Unlock()
		return err
	}
	entry.mu.Unlock()
	if err := s.meta.ExtendJobTokenExpiry(snap.ID, s.nowFn().Unix()+int64(snap.TimeoutSec)+int64(snap.IdleTimeoutSec)+int64(jobTokenFallbackGrace.Seconds())); err != nil {
		slog.Warn("extend ACP session credential", "job_id", snap.ID, "err", err)
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
	if err := s.persist(snap); err != nil {
		entry.mu.Unlock()
		return err
	}
	entry.mu.Unlock()
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
	if entry.result.IdleTimeoutSec > 0 && entry.result.IdleDeadlineAt == 0 {
		entry.result.IdleDeadlineAt = s.nowFn().Add(time.Duration(entry.result.IdleTimeoutSec) * time.Second).Unix()
	}
	snap := entry.result
	if err := s.persist(snap); err != nil {
		entry.mu.Unlock()
		return err
	}
	entry.mu.Unlock()
	s.recordEvent(snap.ID, EventJobAwaitingInput, map[string]any{
		"turn_no": snap.TurnNo, "idle_deadline_at": snap.IdleDeadlineAt,
	})
	return nil
}

package job

import (
	"fmt"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
)

// SaySession queues the next prompt on the same resident ACP job.
func (s *Service) SaySession(id, message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return fmt.Errorf("%w: session message is empty", ErrInvalidRequest)
	}
	entry := s.entry(id)
	if entry == nil {
		if result, ok := s.Get(id); ok && result.Session && isTerminal(result.Status) {
			return fmt.Errorf("%w: session is ending or ended", ErrJobNotRunning)
		}
		if _, ok := s.Get(id); ok {
			return fmt.Errorf("%w: job %s has no live ACP session", ErrJobNotRunning, id)
		}
		return fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	entry.mu.Lock()
	if entry.result.Session && entry.sessionCommands == nil && isWorkerRemoteSession(s.cfg.Load(), entry.result) {
		if entry.result.Status != StatusAwaitingInput {
			entry.mu.Unlock()
			return fmt.Errorf("%w: job %s is not awaiting ACP session input", ErrJobNotRunning, id)
		}
		if entry.sessionCommandPending {
			entry.mu.Unlock()
			return fmt.Errorf("%w: session input already queued", ErrInvalidRequest)
		}
		sender, ok := s.workers.(SessionCommandSender)
		workerID := entry.result.WorkerID
		entry.mu.Unlock()
		if !ok || !sender.IsWorkerOnline(workerID) {
			return fmt.Errorf("%w: worker offline, retry later", ErrJobNotRunning)
		}
		if err := sender.SendSessionCommand(workerID, id, sessionCommandID(id), "say", message); err != nil {
			return err
		}
		entry.mu.Lock()
		entry.sessionCommandPending = true
		s.cancelSessionReplyTimerLocked(entry)
		snap := entry.result
		_ = s.persist(snap)
		entry.mu.Unlock()
		return nil
	}
	defer entry.mu.Unlock()
	if entry.sessionEnding {
		return fmt.Errorf("%w: session is ending", ErrJobNotRunning)
	}
	if !entry.result.Session || entry.sessionCommands == nil || entry.result.Status != StatusAwaitingInput {
		return fmt.Errorf("%w: job %s is not awaiting ACP session input", ErrJobNotRunning, id)
	}
	if entry.sessionCommandPending {
		return fmt.Errorf("%w: session input already queued", ErrInvalidRequest)
	}
	s.cancelSessionReplyTimerLocked(entry)
	entry.sessionCommandPending = true
	entry.sessionCommands <- runner.SessionCommand{Prompt: message}
	return nil
}

// EndSession lets the current turn finish, then closes the resident ACP process.
func (s *Service) EndSession(id string) error {
	entry := s.entry(id)
	if entry == nil {
		if _, ok := s.Get(id); ok {
			return fmt.Errorf("%w: job %s has no live ACP session", ErrJobNotRunning, id)
		}
		return fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	entry.mu.Lock()
	if entry.result.Session && entry.sessionCommands == nil && isWorkerRemoteSession(s.cfg.Load(), entry.result) {
		if isTerminal(entry.result.Status) || entry.sessionEnding {
			entry.mu.Unlock()
			return fmt.Errorf("%w: job %s has no live ACP session", ErrJobNotRunning, id)
		}
		sender, ok := s.workers.(SessionCommandSender)
		workerID := entry.result.WorkerID
		entry.mu.Unlock()
		if !ok || !sender.IsWorkerOnline(workerID) {
			return fmt.Errorf("%w: worker offline, retry later", ErrJobNotRunning)
		}
		if err := sender.SendSessionCommand(workerID, id, sessionCommandID(id), "end", ""); err != nil {
			return err
		}
		entry.mu.Lock()
		entry.sessionCommandPending = true
		entry.sessionEnding = true
		entry.manualEndRequested = true
		entry.result.SessionEnding = true
		s.cancelSessionReplyTimerLocked(entry)
		snap := entry.result
		_ = s.persist(snap)
		entry.mu.Unlock()
		return nil
	}
	if !entry.result.Session || entry.sessionCommands == nil || isTerminal(entry.result.Status) {
		entry.mu.Unlock()
		return fmt.Errorf("%w: job %s has no live ACP session", ErrJobNotRunning, id)
	}
	if entry.sessionCommandPending {
		entry.mu.Unlock()
		return fmt.Errorf("%w: session input already queued", ErrInvalidRequest)
	}
	s.cancelSessionReplyTimerLocked(entry)
	entry.sessionCommandPending = true
	entry.sessionEnding = true
	entry.manualEndRequested = true
	entry.result.SessionEnding = true
	if err := s.persist(entry.result); err != nil {
		entry.result.SessionEnding = false
		entry.manualEndRequested = false
		entry.sessionEnding = false
		entry.sessionCommandPending = false
		entry.mu.Unlock()
		return fmt.Errorf("persist session ending: %w", err)
	}
	entry.sessionCommands <- runner.SessionCommand{End: true}
	entry.mu.Unlock()
	return nil
}

func isWorkerRemoteSession(cfg *config.Config, result JobResult) bool {
	if cfg == nil || result.WorkerID == "" {
		return false
	}
	rc, ok := cfg.Runners[result.Runner]
	return ok && rc.Type == "worker"
}

func sessionCommandID(jobID string) string {
	return fmt.Sprintf("%s-%d", jobID, time.Now().UnixNano())
}

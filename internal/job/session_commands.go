package job

import (
	"fmt"
	"strings"

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
	if !entry.result.Session || entry.sessionCommands == nil || isTerminal(entry.result.Status) {
		entry.mu.Unlock()
		return fmt.Errorf("%w: job %s has no live ACP session", ErrJobNotRunning, id)
	}
	if entry.sessionCommandPending {
		entry.mu.Unlock()
		return fmt.Errorf("%w: session input already queued", ErrInvalidRequest)
	}
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

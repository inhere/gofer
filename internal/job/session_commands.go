package job

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
)

// ErrImagesNotSupported is returned by SaySessionBlocks when the session's agent did
// not advertise promptCapabilities.image (or its worker predates protocol v25).
// Nothing was sent: the caller falls back to delivering the images as files.
var ErrImagesNotSupported = errors.New("session does not accept image blocks")

// ImageInput is one image of a say: its media type and raw bytes.
type ImageInput = runner.ImageInput

// SaySession queues the next prompt on the same resident ACP job.
func (s *Service) SaySession(id, message string) error {
	return s.saySession(id, message, nil)
}

// SaySessionBlocks is SaySession with images: the text goes first, then the images in
// order, as ACP content blocks. When the session does not accept images it returns
// ErrImagesNotSupported without sending anything (ask SessionAcceptsImages first to
// route). With images the text may be empty.
func (s *Service) SaySessionBlocks(id, text string, images []ImageInput) error {
	return s.saySession(id, text, images)
}

// SessionAcceptsImages reports whether the live session job id takes image blocks:
// its ACP agent advertised promptCapabilities.image and, for a worker session, the
// worker reported it (protocol v25; an older worker never does, so false). It errors
// like SaySession does for an unknown or not-live job.
func (s *Service) SessionAcceptsImages(id string) (bool, error) {
	entry := s.entry(id)
	if entry == nil {
		if _, ok := s.Get(id); ok {
			return false, fmt.Errorf("%w: job %s has no live ACP session", ErrJobNotRunning, id)
		}
		return false, fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if !entry.result.Session {
		return false, fmt.Errorf("%w: job %s is not an ACP session", ErrJobNotRunning, id)
	}
	return entry.result.SessionImages, nil
}

func (s *Service) saySession(id, message string, images []ImageInput) error {
	permit, err := s.BeginUpgradeWork()
	if err != nil {
		return err
	}
	defer permit.Release()
	message = strings.TrimSpace(message)
	if message == "" && len(images) == 0 {
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
	if len(images) > 0 && entry.result.Session && !entry.result.SessionImages {
		entry.mu.Unlock()
		return fmt.Errorf("%w: job %s", ErrImagesNotSupported, id)
	}
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
		if len(images) > 0 {
			imgSender, ok := s.workers.(SessionSayImageSender)
			if !ok {
				return fmt.Errorf("%w: job %s (worker transport lacks image say)", ErrImagesNotSupported, id)
			}
			if err := imgSender.SendSessionSay(workerID, id, sessionCommandID(id), message, images); err != nil {
				return err
			}
		} else if err := sender.SendSessionCommand(workerID, id, sessionCommandID(id), "say", message); err != nil {
			return err
		}
		entry.mu.Lock()
		entry.sessionCommandPending = true
		entry.sessionCommandTurn = entry.result.TurnNo
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
	entry.sessionCommands <- runner.SessionCommand{Prompt: message, Images: images}
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

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

// The say refusal reasons (design 2026-11-03 架构: 把“轮次进行中”“已有排队”“worker 离线”
// “会话结束中”拆成可区分的错误). Each one is reachable with errors.Is while the
// historical error (ErrJobNotRunning / ErrInvalidRequest) and the rendered message
// stay exactly what the HTTP/CLI/MCP surfaces already report — callers that only
// check the old error keep working, the receipt path can tell "retry this later"
// (busy / queued / offline) from "the session is gone" (ending / no live session).
var (
	// ErrSessionTurnBusy: a turn is in progress, so the say cannot be queued now.
	ErrSessionTurnBusy = errors.New("session turn in progress")
	// ErrSessionInputQueued: a say is already waiting for the worker to consume it.
	ErrSessionInputQueued = errors.New("session input already queued")
	// ErrWorkerOffline: the session lives on a worker that is not connected.
	ErrWorkerOffline = errors.New("session worker offline")
	// ErrSessionEnding: the session is on its way out; nothing will consume a say.
	ErrSessionEnding = errors.New("session is ending")
	// ErrNoLiveSession: the job is not a resident session this server can reach.
	ErrNoLiveSession = errors.New("job has no live session")
)

// sessionCommandError attaches one of the reasons above to a refusal WITHOUT
// touching its message or the error it wraps: Error and Unwrap keep the historical
// text and classification, Is adds the specific reason.
type sessionCommandError struct {
	reason error
	cause  error
}

func (e *sessionCommandError) Error() string        { return e.cause.Error() }
func (e *sessionCommandError) Unwrap() error        { return e.cause }
func (e *sessionCommandError) Is(target error) bool { return target == e.reason }

// sayRefused wraps a refusal's historical error with its specific reason.
func sayRefused(reason, cause error) error {
	return &sessionCommandError{reason: reason, cause: cause}
}

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
	return s.saySessionCommand(id, message, images, "")
}

// saySessionCommand is saySession with a caller-chosen command id: an empty cmdID
// starts a NEW batch (its own id), while a non-empty one re-sends that batch. The
// worker drops a command id it has already seen and never recycles them, so a
// re-send must reuse the batch's id and a new batch must get a fresh one (design
// 2026-11-03 T2).
func (s *Service) saySessionCommand(id, message string, images []ImageInput, cmdID string) error {
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
			return sayRefused(ErrSessionEnding, fmt.Errorf("%w: session is ending or ended", ErrJobNotRunning))
		}
		if _, ok := s.Get(id); ok {
			return sayRefused(ErrNoLiveSession, fmt.Errorf("%w: job %s has no live ACP session", ErrJobNotRunning, id))
		}
		return fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	entry.mu.Lock()
	if len(images) > 0 && entry.result.Session && !entry.result.SessionImages {
		entry.mu.Unlock()
		return fmt.Errorf("%w: job %s", ErrImagesNotSupported, id)
	}
	if entry.result.Session && entry.sessionCommands == nil && isWorkerRemoteSession(s.cfg.Load(), entry.result) {
		if entry.sessionEnding {
			entry.mu.Unlock()
			return sayRefused(ErrSessionEnding, fmt.Errorf("%w: session is ending", ErrJobNotRunning))
		}
		if entry.result.Status != StatusAwaitingInput {
			entry.mu.Unlock()
			return sayRefused(ErrSessionTurnBusy, fmt.Errorf("%w: job %s is not awaiting ACP session input", ErrJobNotRunning, id))
		}
		if entry.sessionCommandPending {
			entry.mu.Unlock()
			return sayRefused(ErrSessionInputQueued, fmt.Errorf("%w: session input already queued", ErrInvalidRequest))
		}
		sender, ok := s.workers.(SessionCommandSender)
		workerID := entry.result.WorkerID
		if !ok || !sender.IsWorkerOnline(workerID) {
			entry.mu.Unlock()
			return sayRefused(ErrWorkerOffline, fmt.Errorf("%w: worker offline, retry later", ErrJobNotRunning))
		}
		// Reserve the single in-flight slot BEFORE sending (design 2026-11-03 异常): two
		// racers used to pass the check together and the worker dropped the second
		// frame without the host ever learning it, so the receipt vanished silently.
		// The command id belongs to the batch, so a re-send can reuse it.
		if cmdID == "" {
			cmdID = sessionCommandID(id)
		}
		entry.sessionCommandID = cmdID
		entry.sessionCommandPending = true
		entry.sessionCommandTurn = entry.result.TurnNo
		entry.mu.Unlock()
		var sendErr error
		if len(images) > 0 {
			imgSender, ok := s.workers.(SessionSayImageSender)
			if !ok {
				sendErr = fmt.Errorf("%w: job %s (worker transport lacks image say)", ErrImagesNotSupported, id)
			} else {
				sendErr = imgSender.SendSessionSay(workerID, id, cmdID, message, images)
			}
		} else {
			sendErr = sender.SendSessionCommand(workerID, id, cmdID, "say", message)
		}
		if sendErr != nil {
			entry.mu.Lock()
			// A worker that answered by ending the session keeps the reservation: the
			// session is gone, not the say's fault (design 2026-11-03 T2).
			if !entry.sessionEnding {
				entry.sessionCommandPending = false
				entry.sessionCommandTurn = 0
				entry.sessionCommandID = ""
			}
			entry.mu.Unlock()
			return sendErr
		}
		entry.mu.Lock()
		s.cancelSessionReplyTimerLocked(entry)
		snap := entry.result
		_ = s.persist(snap)
		entry.mu.Unlock()
		logJobSay(id, message, len(images))
		return nil
	}
	defer entry.mu.Unlock()
	if entry.sessionEnding {
		return sayRefused(ErrSessionEnding, fmt.Errorf("%w: session is ending", ErrJobNotRunning))
	}
	if !entry.result.Session || entry.sessionCommands == nil || entry.result.Status != StatusAwaitingInput {
		return sayRefused(ErrSessionTurnBusy, fmt.Errorf("%w: job %s is not awaiting ACP session input", ErrJobNotRunning, id))
	}
	if entry.sessionCommandPending {
		return sayRefused(ErrSessionInputQueued, fmt.Errorf("%w: session input already queued", ErrInvalidRequest))
	}
	s.cancelSessionReplyTimerLocked(entry)
	entry.sessionCommandPending = true
	entry.sessionCommands <- runner.SessionCommand{Prompt: message, Images: images}
	logJobSay(id, message, len(images))
	return nil
}

// resendSessionSay re-sends ONE batch of a worker say that is already reserved
// (design 2026-11-03 送达确认: 超时重发只限 worker 父 job，用稳定 CmdID). It sends only
// while that exact batch still owns the slot, the session is still awaiting input
// and no turn has started since it was reserved — otherwise the first attempt was
// consumed (or superseded) and a second frame would only confuse the worker. The
// caller (the tick, T9) decides when an unconfirmed batch is worth re-sending.
func (s *Service) resendSessionSay(id, cmdID, message string) error {
	entry := s.entry(id)
	if entry == nil {
		return sayRefused(ErrNoLiveSession, fmt.Errorf("%w: job %s has no live ACP session", ErrJobNotRunning, id))
	}
	entry.mu.Lock()
	if entry.sessionEnding {
		entry.mu.Unlock()
		return sayRefused(ErrSessionEnding, fmt.Errorf("%w: session is ending", ErrJobNotRunning))
	}
	if !entry.result.Session || entry.sessionCommands != nil {
		entry.mu.Unlock()
		return sayRefused(ErrNoLiveSession, fmt.Errorf("%w: job %s has no live ACP session", ErrJobNotRunning, id))
	}
	if !entry.sessionCommandPending || entry.sessionCommandID != cmdID {
		entry.mu.Unlock()
		return sayRefused(ErrSessionInputQueued, fmt.Errorf("%w: session input already queued", ErrInvalidRequest))
	}
	if entry.result.Status != StatusAwaitingInput || entry.sessionCommandTurn != entry.result.TurnNo {
		entry.mu.Unlock()
		return sayRefused(ErrSessionTurnBusy, fmt.Errorf("%w: job %s is not awaiting ACP session input", ErrJobNotRunning, id))
	}
	sender, ok := s.workers.(SessionCommandSender)
	workerID := entry.result.WorkerID
	entry.mu.Unlock()
	if !ok || !sender.IsWorkerOnline(workerID) {
		return sayRefused(ErrWorkerOffline, fmt.Errorf("%w: worker offline, retry later", ErrJobNotRunning))
	}
	if err := sender.SendSessionCommand(workerID, id, cmdID, "say", message); err != nil {
		return err
	}
	entry.mu.Lock()
	s.cancelSessionReplyTimerLocked(entry)
	snap := entry.result
	_ = s.persist(snap)
	entry.mu.Unlock()
	logJobSay(id, message, 0)
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

package sessionrelay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/i18n"
)

// The mailbox's own delivery paths (design 2026-10-16「二、gofer 信箱」, plan M5). Letters and
// the wake prompt are NOT web messages, so they take the same transports as Deliver /
// SendMessage but none of their web semantics: no person's web prefix on the text, no
// turn answered (never Say), no outbox row, no audit decision, no session state change. What
// the transport reports is all that is left of a delivery; the mailbox keeps its own state
// in the letter.

// Delivery channels reported by RawResult.Path besides PathCommand and PathTmux.
const PathMessenger = messengerChannel

// RawResult is where a raw delivery went.
type RawResult struct {
	// Path is PathCommand, PathTmux or PathMessenger.
	Path string
	// JobID is the internal job that carried the text ("" when none was needed).
	JobID string
}

// DeliverLetter hands text to the LIVE process of a session through its agent's
// deliver_command (path C: `suga send`, `codex queue`) and nothing else. text goes through
// verbatim. The failure vocabulary is Deliver's: ReasonNotRunning (exit 3 — the process is
// not there, the caller leaves the letter pending), DeliverFailedPrefix…, NotDeliveredPrefix…;
// an agent without a deliver_command is ReasonNoDeliverCommand. A job that outlived the
// wait window counts as delivered (it is on its way; the mailbox is at-least-once).
//
// by is the display label of the delivery ("gofer-mail"); origin is its ceiling owner —
// the letter's sender — so the internal job stays bounded by that caller (see
// ceiling_origin.go).
func (s *Service) DeliverLetter(ctx context.Context, sid, text, by, origin string) (RawResult, error) {
	a, err := s.Session(sid)
	if err != nil {
		return RawResult{}, err
	}
	if _, err := s.ceilingOwner(origin, "a mailbox letter"); err != nil {
		return RawResult{}, err
	}
	res, tried, err := s.runDeliverCommand(ctx, a, text, by, origin)
	if err != nil {
		return RawResult{}, err
	}
	if !tried {
		return RawResult{}, undeliverable(ReasonNoDeliverCommand, errors.New("the session's agent has no deliver_command"))
	}
	return RawResult{Path: PathCommand, JobID: res.JobID}, nil
}

// ReasonNoDeliverCommand: DeliverLetter was asked for an agent that declares no deliver_command.
const ReasonNoDeliverCommand = "no_deliver_command"

// Wake sends the mailbox's wake prompt to a session whose process is idle, so a new turn
// starts and the hook injects the pending letters at its beginning. Transports, in order: the
// agent's deliver_command (codex queue), the Claude SendMessage messenger (one-shot job),
// the tmux pane. text goes through verbatim — no web prefix — and whatever the target answers
// to a messenger wake is dropped: the one-shot messenger is not a resident listener, and the
// target's hook only records a reply to a message that starts with the web prefix, which the
// wake does not have. A failure of the first transport that actually ran ends the ladder
// (a second one could type the prompt twice); transports that did not apply (no
// deliver_command, no peer address, no pane) fall through.
//
// by is the display label of the wake ("gofer-mail"); origin is the ceiling owner behind the
// letters that caused it (see ceiling_origin.go).
func (s *Service) Wake(ctx context.Context, sid, text, by, origin string) (RawResult, error) {
	if _, err := s.ceilingOwner(origin, "a mailbox wake"); err != nil {
		return RawResult{}, err
	}
	a, err := s.Session(sid)
	if err != nil {
		return RawResult{}, err
	}
	if strings.TrimSpace(text) == "" {
		return RawResult{}, fmt.Errorf("%w: empty text", ErrInvalidInput)
	}
	res, tried, err := s.runDeliverCommand(ctx, a, text, by, origin)
	switch {
	case tried && err == nil:
		return RawResult{Path: PathCommand, JobID: res.JobID}, nil
	case tried && DeliverReason(err) != ReasonNotRunning:
		return RawResult{}, err
	}
	if s.wakeViaMessenger(a.PeerName, a.PeerMessaging, a.ProjectKey, a.Runner) {
		// The messenger opens in the project root it is submitted with. A session that
		// belongs to NO project has none to give, and its own cwd is a directory outside
		// every project: run the messenger in the default workspace root instead of
		// writing into an unrelated directory (gofer-vin1).
		cwd := a.Cwd
		if strings.TrimSpace(a.ProjectKey) == "" {
			cwd = "."
		}
		return s.wakeMessenger(ctx, a.ProjectKey, a.Runner, cwd, a.PeerName, text, by, origin)
	}
	inj, err := s.runTmuxInject(ctx, a, text, by, origin)
	if err != nil {
		return RawResult{}, err
	}
	return RawResult{Path: PathTmux, JobID: inj.JobID}, nil
}

func (s *Service) wakeViaMessenger(peerName string, peerMessaging bool, projectKey, runner string) bool {
	// projectKey may be EMPTY: a session that belongs to no project is reached through the
	// execution machine's default workspace, which the host resolves (gofer-vin1).
	return s.messagingEnabled && s.messenger != nil && peerMessaging &&
		strings.TrimSpace(peerName) != "" && strings.TrimSpace(runner) != ""
}

// wakeMessengerPrompt is the messenger job's prompt for a wake: the forwarded text carries
// no web prefix, and the messenger is told not to retell any answer. It also says that the
// wake notice IS the whole message: a live test had the messenger refuse to forward it
// because it found "no message body, only a pending-mail notice".
func wakeMessengerPrompt(lang i18n.Lang, name, text string) string {
	return i18n.T(lang, "sessionrelay.messenger.wake_prompt", "name", fmt.Sprintf("%q", name)) + "\n" + text
}

func (s *Service) wakeMessenger(ctx context.Context, projectKey, runner, cwd, peerName, text, by, origin string) (RawResult, error) {
	command := []string{s.messengerCommand, "-p", wakeMessengerPrompt(s.lang(projectKey), peerName, text), "--allowedTools", "SendMessage,ListAgents", "--name", MessengerSessionName}
	slot := s.messengerSlot(runner)
	select {
	case slot <- struct{}{}:
		defer func() { <-slot }()
	case <-ctx.Done():
		return RawResult{}, ctx.Err()
	}
	jobID, err := s.messenger.SubmitMessenger(projectKey, runner, cwd, command, "mail wake · "+peerName, origin)
	if err != nil {
		return RawResult{}, err
	}
	deadline := time.Now().Add(s.messengerTimeout)
	for {
		done, status, code, output, jerr := s.messenger.MessengerJob(jobID)
		if jerr != nil {
			return RawResult{}, jerr
		}
		if done {
			if status == "done" && code == 0 && messengerSaidSent(output) {
				return RawResult{Path: PathMessenger, JobID: jobID}, nil
			}
			reason := strings.TrimSpace(output)
			if reason == "" {
				reason = fmt.Sprintf("传话人 job %s 状态=%s exit=%d", jobID, status, code)
			}
			return RawResult{}, errors.New(reason)
		}
		if !time.Now().Before(deadline) {
			return RawResult{}, fmt.Errorf("传话人 job %s 超时未完成", jobID)
		}
		select {
		case <-ctx.Done():
			return RawResult{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

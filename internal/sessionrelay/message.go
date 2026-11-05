package sessionrelay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/msgprefix"
)

// Messenger is the narrow seam for a one-shot SendMessage job. The relay owns
// ordering and delivery semantics; the job/http assembly layer owns how a job
// is submitted and how its bounded output is read.
//
// output is the job's combined output (stdout + stderr): it is what a failure is
// reported with. stdout is STDOUT ALONE, the channel the messenger's receipt is read
// from — stderr carries warnings that must not vote on whether the text went out.
type Messenger interface {
	SubmitMessenger(projectKey, runner, cwd string, command []string, title, caller string) (string, error)
	MessengerJob(jobID string) (done bool, status string, exitCode int, output, stdout string, err error)
}

// MessengerAbandoner is the optional half of the one-shot messenger seam that settles a
// job gofer stops waiting for (gofer-kx53), the same question InjectResult answers for
// path A/C: when messengerTimeout elapses, did the text start on its way? The host
// re-reads the job and answers with the InjectResult vocabulary:
//   - Running: the job had begun, so the message may still land (「送达中」, written
//     back at the job's terminal state through InjectProbe);
//   - a non-empty Status without Running: the job had NOT begun and the host cancelled
//     it, so nothing arrives late (「未送达」, safe to retry);
//   - neither: the job ended meanwhile, and ExitCode/Output are its own.
//
// A messenger without it keeps the old behaviour (the timeout is a failure).
type MessengerAbandoner interface {
	AbandonMessengerJob(jobID string) (InjectResult, error)
}

// ResidentResult is the tool-level outcome of a resident SendMessage request: Sent
// says whether the messenger's tool call delivered the text, Reason why it did not
// (empty when the turn made no call, or when it succeeded). It is the resident
// counterpart of the receipt the one-shot messenger job writes to its stdout.
type ResidentResult struct {
	Sent   bool
	Reason string
}

// ResidentMessenger is an optional same-runner stream bridge. Implementations
// may keep one process per runner and must serialize requests; callers fall back
// to Messenger's one-shot job path when this capability is absent.
type ResidentMessenger interface {
	SendMessengerResident(ctx context.Context, runner, cwd, target string, command []string) (ResidentResult, error)
}

// ConfigureMessaging applies the server-scoped message bridge policy.
func (s *Service) ConfigureMessaging(enabled bool, command string, timeout, idle time.Duration) {
	s.messagingEnabled = enabled
	if strings.TrimSpace(command) != "" {
		s.messengerCommand = strings.TrimSpace(command)
	}
	if timeout > 0 {
		s.messengerTimeout = timeout
	}
	if idle > 0 {
		s.messengerIdle = idle
	}
}

func (s *Service) SetMessenger(m Messenger) { s.messenger = m }

func (s *Service) messageLock(sid string) *sync.Mutex {
	s.messagingMu.Lock()
	defer s.messagingMu.Unlock()
	if m := s.messagingLocks[sid]; m != nil {
		return m
	}
	m := &sync.Mutex{}
	s.messagingLocks[sid] = m
	return m
}

func (s *Service) messengerSlot(runner string) chan struct{} {
	s.messagingMu.Lock()
	defer s.messagingMu.Unlock()
	if slot := s.messengerSlots[runner]; slot != nil {
		return slot
	}
	slot := make(chan struct{}, 2)
	s.messengerSlots[runner] = slot
	return slot
}

// SendMessage chooses relay for a waiting turn and a messenger job otherwise.
// It records every transition in the session outbox before returning, making the
// web view truthful even when a runner goes offline between two requests.
//
// operator is the display label of the speaker (who the outbox and the messenger prompt say
// the message came from); origin is the caller-scope ceiling owner behind it (see
// ceiling_origin.go). A messenger / delivery job gofer runs for this message carries origin,
// never the label.
func (s *Service) SendMessage(ctx context.Context, sid, text, operator, origin string) (jobstore.SessionMessage, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return jobstore.SessionMessage{}, fmt.Errorf("%w: message required", ErrInvalidInput)
	}
	if _, err := s.ceilingOwner(origin, "a session message"); err != nil {
		return jobstore.SessionMessage{}, err
	}
	a, err := s.Session(sid)
	if err != nil {
		return jobstore.SessionMessage{}, err
	}
	if a.State == jobstore.SessionEnded {
		return jobstore.SessionMessage{}, fmt.Errorf("%w: session ended", ErrInvalidInput)
	}
	if !s.messagingEnabled {
		return jobstore.SessionMessage{}, fmt.Errorf("%w: session messaging disabled", ErrInvalidInput)
	}
	lock := s.messageLock(sid)
	lock.Lock()
	defer lock.Unlock()
	now := time.Now().Unix()
	m := jobstore.SessionMessage{ID: fmt.Sprintf("msg-%d", time.Now().UnixNano()), SessionID: sid,
		Text: text, Operator: operator, Status: jobstore.SessionMessageQueued, CreatedAt: now, UpdatedAt: now}
	if err := s.store.AppendSessionOutbox(m); err != nil {
		return jobstore.SessionMessage{}, err
	}
	// Phase 1 is judged on the TURN, not on the session state (gofer-fv6a): the state
	// is a summary that another beat can move while the turn stays open, and a message
	// sent through the messenger then sits unread inside a session that is blocked in
	// its own Stop hook. Deliver already routes on the open turn (ListOpenSessionTurns);
	// the two must agree, or a web reply and a job-end notice reach the same waiting
	// agent by different paths — one instantly, the other at the next human prompt.
	openTurn := false
	if turns, oerr := s.store.ListOpenSessionTurns(sid, 1); oerr != nil {
		// A read that fails must not fail the message: fall back to the state the last
		// heartbeat left, which is all this ladder had before gofer-fv6a.
		slog.Warn("sessionrelay: list open turns failed", "session_id", sid, "err", oerr)
		openTurn = a.State == jobstore.SessionWaitingReply
	} else {
		openTurn = len(turns) > 0
	}
	if openTurn {
		d, err := s.Say(sid, text, operator)
		switch {
		case err == nil:
			m.DecisionID = d.ID
			m.Status, m.Channel, m.UpdatedAt = jobstore.SessionMessageDelivered, PathTurn, time.Now().Unix()
			_ = s.store.AppendSessionOutbox(m)
			return m, nil
		case !errors.Is(err, ErrNoOpenTurn):
			return s.failMessage(m, "relay: "+err.Error())
		}
		// The turn settled between the read and the answer: fall through to the
		// terminal paths, exactly as Deliver does.
	}
	// A session with no Claude SendMessage address (any self-built agent, an old
	// Claude Code) is reached through the routed deliver ladder instead: the agent's
	// own deliver_command (path C), then its tmux pane (path A). Never a takeover from
	// here — that moves the session to a new process and needs its own confirmation
	// (the web's takeover button calls deliver with allow_takeover).
	if strings.TrimSpace(a.PeerName) == "" || !a.PeerMessaging {
		res, derr := s.Deliver(ctx, sid, text, operator, origin, false)
		if derr == nil {
			// gofer-qzv5: the delivery job outlived the wait window (a server update
			// waiting for idle used to hold it for minutes). The text is on its way, not
			// lost: record 「送达中」 and let the terminal write-back say how it ended.
			m.Channel, m.JobID, m.DecisionID, m.UpdatedAt = res.Path, res.JobID, res.DecisionID, time.Now().Unix()
			m.Status = jobstore.SessionMessageDelivered
			if res.Delivering {
				m.Status = jobstore.SessionMessageDelivering
			}
			_ = s.store.AppendSessionOutbox(m)
			return m, nil
		}
		reason := DeliverReason(derr)
		if reason == "" {
			return s.failMessage(m, derr.Error())
		}
		detail := derr.Error()
		var ue *UndeliverableError
		if errors.As(derr, &ue) && ue.Err != nil {
			detail = ue.Err.Error()
		}
		// A delivery that provably never happened is 「未送达」: the reason names what
		// stopped it, and a retry cannot deliver the text twice.
		if deliveryNeverStarted(reason) {
			return s.undeliveredMessage(m, reason+": "+detail)
		}
		return s.failMessage(m, reason+": "+detail)
	}
	if strings.TrimSpace(a.ProjectKey) == "" {
		return s.failMessage(m, i18n.T(s.lang(a.ProjectKey), "sessionrelay.reason.no_project"))
	}
	if s.messenger == nil {
		return s.failMessage(m, i18n.T(s.lang(a.ProjectKey), "sessionrelay.reason.no_messenger"))
	}
	if strings.TrimSpace(a.PeerName) == "" || !a.PeerMessaging {
		return s.failMessage(m, i18n.T(s.lang(a.ProjectKey), "sessionrelay.reason.no_address"))
	}
	if strings.TrimSpace(a.Runner) == "" {
		return s.failMessage(m, i18n.T(s.lang(a.ProjectKey), "sessionrelay.reason.no_runner"))
	}
	lang := s.lang(a.ProjectKey)
	target := s.messengerTargetFor(a)
	if resident, ok := s.messenger.(ResidentMessenger); ok && s.IsServerLocalRunner(a.Runner) {
		command := []string{s.messengerCommand, "-p", messengerPrompt(lang, target, operator, text), "--allowedTools", "SendMessage,ListAgents", "--name", MessengerSessionName}
		res, rerr := resident.SendMessengerResident(ctx, localRunnerKey, a.Cwd, target.Name, command)
		if rerr == nil {
			// A resident turn's SendMessage tool_result is the truth (design 「投递结果判定」):
			// a tool-level failure is a failure the web must show, and it does NOT become a
			// one-shot job — a second attempt could hand the same text over twice while the
			// first one landed. An empty Reason means the turn called no SendMessage at all.
			if res.Sent {
				m.Status, m.Channel, m.UpdatedAt = jobstore.SessionMessageDelivered, messengerChannel, time.Now().Unix()
				_ = s.store.AppendSessionOutbox(m)
				return m, nil
			}
			reason := strings.TrimSpace(res.Reason)
			if reason == "" {
				reason = i18n.T(lang, "sessionrelay.reason.messenger_no_send")
			}
			return s.failMessage(m, reason)
		}
		// Startup/early process failures fall back to the durable one-shot job. A
		// timeout after a request was written is terminal for this message so a
		// retry cannot accidentally deliver it twice.
		if !strings.Contains(rerr.Error(), "start") && !strings.Contains(rerr.Error(), "exited") && !strings.Contains(rerr.Error(), "unavailable") {
			return s.failMessage(m, rerr.Error())
		}
	}
	slot := s.messengerSlot(a.Runner)
	select {
	case slot <- struct{}{}:
		defer func() { <-slot }()
	case <-ctx.Done():
		return s.failMessage(m, ctx.Err().Error())
	}
	command := []string{s.messengerCommand, "-p", messengerPrompt(lang, target, operator, text), "--allowedTools", "SendMessage,ListAgents", "--name", MessengerSessionName}
	jobID, err := s.messenger.SubmitMessenger(a.ProjectKey, a.Runner, a.Cwd, command, "session messenger · "+target.Name, origin)
	if err != nil {
		return s.failMessage(m, err.Error())
	}
	m.JobID = jobID
	_ = s.store.AppendSessionOutbox(m)
	deadline := time.Now().Add(s.messengerTimeout)
	for {
		done, status, code, output, stdout, jerr := s.messenger.MessengerJob(jobID)
		if jerr != nil {
			return s.failMessage(m, jerr.Error())
		}
		if done {
			if status == "done" && code == 0 && messengerSaidSent(stdout) {
				m.Status, m.Channel, m.UpdatedAt = jobstore.SessionMessageDelivered, messengerChannel, time.Now().Unix()
				_ = s.store.AppendSessionOutbox(m)
				return m, nil
			}
			reason := strings.TrimSpace(output)
			if reason == "" {
				reason = i18n.T(s.lang(a.ProjectKey), "sessionrelay.reason.messenger_job_state", "job", jobID, "status", status, "code", code)
			}
			return s.failMessage(m, reason)
		}
		if !time.Now().Before(deadline) {
			return s.settleTimedOutMessenger(s.lang(a.ProjectKey), m, jobID)
		}
		select {
		case <-ctx.Done():
			return s.failMessage(m, ctx.Err().Error())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// SendMessageWithImages delivers an already placed image list through the target agent's
// deliver_command. It deliberately bypasses the open-turn answer path: image arguments are
// part of the live process command, not of a hook answer.
func (s *Service) SendMessageWithImages(ctx context.Context, sid, text, operator, origin string, imagePaths []string) (jobstore.SessionMessage, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return jobstore.SessionMessage{}, fmt.Errorf("%w: message required", ErrInvalidInput)
	}
	if _, err := s.ceilingOwner(origin, "a session message"); err != nil {
		return jobstore.SessionMessage{}, err
	}
	a, err := s.Session(sid)
	if err != nil {
		return jobstore.SessionMessage{}, err
	}
	if a.State == jobstore.SessionEnded {
		return jobstore.SessionMessage{}, fmt.Errorf("%w: session ended", ErrInvalidInput)
	}
	if !s.messagingEnabled {
		return jobstore.SessionMessage{}, fmt.Errorf("%w: session messaging disabled", ErrInvalidInput)
	}
	lock := s.messageLock(sid)
	lock.Lock()
	defer lock.Unlock()
	if strings.TrimSpace(a.Runner) == "" {
		return jobstore.SessionMessage{}, undeliverable(ReasonNoRunner, errors.New(i18n.T(s.lang(a.ProjectKey), "sessionrelay.reason.no_runner")))
	}
	now := time.Now().Unix()
	m := jobstore.SessionMessage{ID: fmt.Sprintf("msg-%d", time.Now().UnixNano()), SessionID: sid, Text: text, Operator: operator, Status: jobstore.SessionMessageQueued, CreatedAt: now, UpdatedAt: now}
	if err := s.store.AppendSessionOutbox(m); err != nil {
		return jobstore.SessionMessage{}, err
	}
	res, err := s.DeliverWithImages(ctx, sid, text, operator, origin, imagePaths)
	if err != nil {
		reason := DeliverReason(err)
		if reason == "" {
			reason = err.Error()
		}
		var ue *UndeliverableError
		detail := err.Error()
		if errors.As(err, &ue) && ue.Err != nil {
			detail = ue.Err.Error()
		}
		if deliveryNeverStarted(reason) {
			return s.undeliveredMessage(m, reason+": "+detail)
		}
		return s.failMessage(m, reason+": "+detail)
	}
	m.Status, m.Channel, m.JobID, m.DecisionID, m.UpdatedAt = jobstore.SessionMessageDelivered, res.Path, res.JobID, res.DecisionID, time.Now().Unix()
	if res.Delivering {
		m.Status = jobstore.SessionMessageDelivering
	}
	_ = s.store.AppendSessionOutbox(m)
	return m, nil
}

// resident messenger is keyed by it).
const localRunnerKey = config.BuiltinLocalRunner

// IsServerLocalRunner reports whether a session's runner label means the server's
// own machine. A hook-registered session carries "server" (the CLI spelling, see
// resolveHookRunner) while jobs and /v1/runners say "local" (G043): both are the
// same runner — and so is a stored label from before registration normalized it —
// so every comparison goes through here instead of matching one literal.
func (s *Service) IsServerLocalRunner(label string) bool {
	label = strings.TrimSpace(label)
	if label == "" {
		return false
	}
	// Labels are matched case-insensitively ("Server" from a hand-set env).
	return config.IsLocalRunnerName(label)
}

// MessengerSessionName is the Claude Code session name the messenger runs under, so
// the receiving session sees a web message as coming from "gofer-web" — the person
// on the web — rather than from an auto-named session of the target's directory
// (gofer-02fu).
const MessengerSessionName = msgprefix.MessengerName

func messengerPrompt(lang i18n.Lang, target messengerTarget, operator, text string) string {
	if strings.TrimSpace(operator) == "" {
		operator = "web"
	}
	// The second sentence of the instruction keeps the resident messenger from retelling
	// the target's answer: gofer shows that answer on the web itself (gofer-6er0). It must
	// not contain "] " — the original text is cut after the first one.
	return messengerInstruction(lang, target, "prompt") +
		"\n" + messengerLine(lang, operator, text)
}

// messengerInstruction renders the messenger's own instruction, the part the prompt puts
// before the forwarded text (design 「提示词」). Without an address it is the text gofer has
// always sent, word for word: such a session keeps behaving exactly as before. With one,
// it names the address to write into `to` verbatim and says what a tool failure means —
// never fall back to the name while another live session on the same runner carries it
// (the human picked THIS process), fall back once when nothing else has it.
func messengerInstruction(lang i18n.Lang, target messengerTarget, base string) string {
	key := "sessionrelay.messenger." + base
	switch {
	case target.Socket == "":
	case target.Fallback:
		key += "_address_fallback"
	default:
		key += "_address"
	}
	return i18n.T(lang, key, "name", fmt.Sprintf("%q", target.Name), "address", target.Socket)
}

// messengerLine is the line the messenger forwards, exactly as the target session
// reads it: a person's message wears the web prefix in the TARGET's language
// ("[来自 web，<user>] …" / "[from web, <user>] …"), a gofer system notice is passed
// through under its own prefix — the two must never look alike (gofer-11gk).
func messengerLine(lang i18n.Lang, operator, text string) string {
	if IsSystemNoticeOperator(operator) {
		return SystemNoticeLine(text)
	}
	return msgprefix.FormatIn(lang, msgprefix.WebMessage, operator, text)
}

func (s *Service) failMessage(m jobstore.SessionMessage, reason string) (jobstore.SessionMessage, error) {
	m.Status, m.Error, m.UpdatedAt = jobstore.SessionMessageFailed, reason, time.Now().Unix()
	_ = s.store.AppendSessionOutbox(m)
	return m, fmt.Errorf("%s", reason)
}

// undeliveredMessage is failMessage for a delivery that provably never happened: the
// record says 「未送达」, which is what the web opens a retry for (gofer-qzv5: a failure
// of an attempt may already have delivered the text, so it is not retryable).
func (s *Service) undeliveredMessage(m jobstore.SessionMessage, reason string) (jobstore.SessionMessage, error) {
	m.Status, m.Error, m.UpdatedAt = jobstore.SessionMessageUndelivered, reason, time.Now().Unix()
	_ = s.store.AppendSessionOutbox(m)
	return m, fmt.Errorf("%s", reason)
}

// messengerChannel is the outbox channel of a message the one-shot messenger carried.
const messengerChannel = "messenger"

// messengerSentMarkers are the "I forwarded it" receipts a messenger agent prints.
// The zh prompt asks for the first, the English prompt for `sent`; a reader accepts
// both (design 2026-10-27-i18n-design.md §5).
var messengerSentMarkers = []string{"已发送", "sent"}

// messengerSaidSent reports whether a messenger's STDOUT carries a forwarding receipt
// on a line of its own: 「已发送」 or `sent` once the surrounding punctuation / symbols a
// model likes to wrap it in (quotes, asterisks, a full stop) are trimmed, compared
// case-insensitively. Two limits are deliberate:
//   - only stdout votes: stderr carries warnings, and a warning mentioning the receipt
//     used to be enough to report a lost message as delivered;
//   - the whole line or its first sentence must match, and a receipt EARLIER in the
//     output still counts — the target often answers in the same run (「已收到」 on the
//     last line), and a model may explain after the receipt (「已发送。消息已投递到…」),
//     while a failure narrative ("the message was not sent", "send failed: sent",
//     「已发送失败」) is not a receipt.
func messengerSaidSent(stdout string) bool {
	for _, line := range strings.Split(stdout, "\n") {
		if isSentMarker(trimReceiptLine(line)) || isSentMarker(trimReceiptLine(firstSentence(line))) {
			return true
		}
	}
	return false
}

func isSentMarker(s string) bool {
	return strings.EqualFold(s, messengerSentMarkers[0]) || strings.EqualFold(s, messengerSentMarkers[1])
}

// firstSentence cuts line at its first sentence end. A comma does not end one, so
// 「已发送，但对方拒收」 stays a narrative.
func firstSentence(line string) string {
	if i := strings.IndexAny(line, "。.！!"); i >= 0 {
		return line[:i]
	}
	return line
}

// trimReceiptLine removes whitespace and then the leading / trailing punctuation and
// symbols a receipt may be wrapped in.
func trimReceiptLine(line string) string {
	line = strings.TrimSpace(line)
	return strings.TrimSpace(strings.TrimFunc(line, func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSymbol(r)
	}))
}

// settleTimedOutMessenger decides what the expiry of messengerTimeout means for the text
// (gofer-kx53), with the same honesty as a path A/C delivery (gofer-qzv5): a job that
// began may still hand it over, so the message is 「送达中」 and the job's terminal state
// writes the outcome back; a job that never began is cancelled by the host and the
// message is 「未送达」 — a retry cannot deliver it twice. Only a messenger that cannot
// tell (no MessengerAbandoner) falls back to the old plain failure.
func (s *Service) settleTimedOutMessenger(lang i18n.Lang, m jobstore.SessionMessage, jobID string) (jobstore.SessionMessage, error) {
	timeout := i18n.T(lang, "sessionrelay.reason.messenger_timeout", "job", jobID)
	ab, ok := s.messenger.(MessengerAbandoner)
	if !ok {
		return s.failMessage(m, timeout)
	}
	res, err := ab.AbandonMessengerJob(jobID)
	if err != nil {
		return s.failMessage(m, i18n.T(lang, "sessionrelay.reason.messenger_timeout_error", "timeout", timeout, "error", err.Error()))
	}
	switch {
	case res.Running:
		m.Status, m.Channel, m.UpdatedAt = jobstore.SessionMessageDelivering, messengerChannel, s.nowFn().Unix()
		if err := s.store.AppendSessionOutbox(m); err != nil {
			return jobstore.SessionMessage{}, err
		}
		s.notePendingDelivery(m.SessionID, jobID)
		return m, nil
	case strings.TrimSpace(res.Status) != "":
		return s.undeliveredMessage(m, notStarted(lang, res).Error())
	case res.ExitCode == 0 && messengerSaidSent(res.Stdout):
		m.Status, m.Channel, m.UpdatedAt = jobstore.SessionMessageDelivered, messengerChannel, s.nowFn().Unix()
		_ = s.store.AppendSessionOutbox(m)
		return m, nil
	}
	reason := strings.TrimSpace(res.Output)
	if reason == "" {
		reason = i18n.T(lang, "sessionrelay.reason.messenger_timeout_exit", "timeout", timeout, "code", res.ExitCode)
	}
	return s.failMessage(m, reason)
}

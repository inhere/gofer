package sessionrelay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// Messenger is the narrow seam for a one-shot SendMessage job. The relay owns
// ordering and delivery semantics; the job/http assembly layer owns how a job
// is submitted and how its bounded output is read.
type Messenger interface {
	SubmitMessenger(projectKey, runner, cwd string, command []string, title, caller string) (string, error)
	MessengerJob(jobID string) (done bool, status string, exitCode int, output string, err error)
}

// ResidentMessenger is an optional same-runner stream bridge. Implementations
// may keep one process per runner and must serialize requests; callers fall back
// to Messenger's one-shot job path when this capability is absent.
type ResidentMessenger interface {
	SendMessengerResident(ctx context.Context, runner, cwd, target string, command []string) (string, error)
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
func (s *Service) SendMessage(ctx context.Context, sid, text, operator string) (jobstore.SessionMessage, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return jobstore.SessionMessage{}, fmt.Errorf("%w: message required", ErrInvalidInput)
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
	if a.State == jobstore.SessionWaitingReply {
		if _, err := s.Say(sid, text, operator); err != nil {
			return s.failMessage(m, "relay: "+err.Error())
		}
		m.Status, m.Channel, m.UpdatedAt = jobstore.SessionMessageDelivered, PathTurn, time.Now().Unix()
		_ = s.store.AppendSessionOutbox(m)
		return m, nil
	}
	// A session with no Claude SendMessage address (any self-built agent, an old
	// Claude Code) is reached through the routed deliver ladder instead: the agent's
	// own deliver_command (path C), then its tmux pane (path A). Never a takeover from
	// here — that moves the session to a new process and needs its own confirmation
	// (the web's takeover button calls deliver with allow_takeover).
	if strings.TrimSpace(a.PeerName) == "" || !a.PeerMessaging {
		res, derr := s.Deliver(ctx, sid, text, operator, false)
		if derr == nil {
			m.Status, m.Channel, m.JobID, m.UpdatedAt = jobstore.SessionMessageDelivered, res.Path, res.JobID, time.Now().Unix()
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
		return s.failMessage(m, reason+": "+detail)
	}
	if strings.TrimSpace(a.ProjectKey) == "" {
		return s.failMessage(m, "该会话所在目录不属于任何已配置项目，无法派发传话人；请把目录加入项目，或在会话所在 runner 上配置项目")
	}
	if s.messenger == nil {
		return s.failMessage(m, "传话人执行器未配置")
	}
	if strings.TrimSpace(a.PeerName) == "" || !a.PeerMessaging {
		return s.failMessage(m, "会话没有可用的 Claude SendMessage 地址")
	}
	if strings.TrimSpace(a.Runner) == "" {
		return s.failMessage(m, "会话未登记执行机")
	}
	if resident, ok := s.messenger.(ResidentMessenger); ok && s.IsServerLocalRunner(a.Runner) {
		command := []string{s.messengerCommand, "-p", messengerPrompt(a.PeerName, operator, text), "--allowedTools", "SendMessage,ListAgents"}
		output, rerr := resident.SendMessengerResident(ctx, localRunnerKey, a.Cwd, a.PeerName, command)
		if rerr == nil {
			m.Status, m.Channel, m.UpdatedAt = jobstore.SessionMessageDelivered, "messenger", time.Now().Unix()
			_ = s.store.AppendSessionOutbox(m)
			_ = output
			return m, nil
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
	command := []string{s.messengerCommand, "-p", messengerPrompt(a.PeerName, operator, text), "--allowedTools", "SendMessage,ListAgents"}
	jobID, err := s.messenger.SubmitMessenger(a.ProjectKey, a.Runner, a.Cwd, command, "session messenger · "+a.PeerName, operator)
	if err != nil {
		return s.failMessage(m, err.Error())
	}
	m.JobID = jobID
	_ = s.store.AppendSessionOutbox(m)
	deadline := time.Now().Add(s.messengerTimeout)
	for {
		done, status, code, output, jerr := s.messenger.MessengerJob(jobID)
		if jerr != nil {
			return s.failMessage(m, jerr.Error())
		}
		if done {
			if status == "done" && code == 0 && strings.Contains(output, "已发送") {
				m.Status, m.Channel, m.UpdatedAt = jobstore.SessionMessageDelivered, "messenger", time.Now().Unix()
				_ = s.store.AppendSessionOutbox(m)
				return m, nil
			}
			reason := strings.TrimSpace(output)
			if reason == "" {
				reason = fmt.Sprintf("传话人 job %s 状态=%s exit=%d", jobID, status, code)
			}
			return s.failMessage(m, reason)
		}
		if !time.Now().Before(deadline) {
			return s.failMessage(m, fmt.Sprintf("传话人 job %s 超时", jobID))
		}
		select {
		case <-ctx.Done():
			return s.failMessage(m, ctx.Err().Error())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// localRunnerKey is the canonical key of the server's built-in runner (the
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

func messengerPrompt(name, operator, text string) string {
	if strings.TrimSpace(operator) == "" {
		operator = "web"
	}
	// The second sentence keeps the resident messenger from retelling the target's
	// answer: gofer shows that answer on the web itself (gofer-6er0). It must not
	// contain "] " — the original text is cut after the first one.
	return fmt.Sprintf("请使用 SendMessage 将下面消息原样转发给 Claude Code 会话 %q。不要改写。只回复‘已发送’或失败原因。"+
		"对方会话之后发给你的回复会由 gofer 直接显示给 web 用户：收到时不要转述、不要回复它，只输出‘已收到’。\n%s，%s] %s",
		name, WebMessagePrefix, operator, text)
}

func (s *Service) failMessage(m jobstore.SessionMessage, reason string) (jobstore.SessionMessage, error) {
	m.Status, m.Error, m.UpdatedAt = jobstore.SessionMessageFailed, reason, time.Now().Unix()
	_ = s.store.AppendSessionOutbox(m)
	return m, fmt.Errorf("%s", reason)
}

package sessionrelay

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// Messenger is the narrow seam for a one-shot SendMessage job. The relay owns
// ordering and delivery semantics; the job/http assembly layer owns how a job
// is submitted and how its bounded output is read.
type Messenger interface {
	SubmitMessenger(projectKey, runner, cwd string, command []string, title, caller string) (string, error)
	MessengerJob(jobID string) (done bool, status string, exitCode int, output string, err error)
}

// ConfigureMessaging applies the server-scoped message bridge policy.
func (s *Service) ConfigureMessaging(enabled bool, command string, timeout time.Duration) {
	s.messagingEnabled = enabled
	if strings.TrimSpace(command) != "" {
		s.messengerCommand = strings.TrimSpace(command)
	}
	if timeout > 0 {
		s.messengerTimeout = timeout
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
	if s.messenger == nil {
		return s.failMessage(m, "传话人执行器未配置")
	}
	if strings.TrimSpace(a.PeerName) == "" || !a.PeerMessaging {
		return s.failMessage(m, "会话没有可用的 Claude SendMessage 地址")
	}
	if strings.TrimSpace(a.Runner) == "" {
		return s.failMessage(m, "会话未登记执行机")
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

func messengerPrompt(name, operator, text string) string {
	if strings.TrimSpace(operator) == "" {
		operator = "web"
	}
	return fmt.Sprintf("请使用 SendMessage 将下面消息原样转发给 Claude Code 会话 %q。不要改写。只回复‘已发送’或失败原因。\n[来自 web，%s] %s", name, operator, text)
}

func (s *Service) failMessage(m jobstore.SessionMessage, reason string) (jobstore.SessionMessage, error) {
	m.Status, m.Error, m.UpdatedAt = jobstore.SessionMessageFailed, reason, time.Now().Unix()
	_ = s.store.AppendSessionOutbox(m)
	return m, fmt.Errorf("%s", reason)
}

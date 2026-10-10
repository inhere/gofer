package sessionrelay

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/jobstore"
)

// WebMessagePrefix opens every message the messenger delivers ("[来自 web，<用户>] …").
// The target's hook uses it to recognise a SendMessage that answers one.
const WebMessagePrefix = "[来自 web"

// replyDedupeWindow is how long an identical reply text counts as the same reply:
// the target's hook and the messenger both report a reply that reached both.
const replyDedupeWindow = 10 * time.Minute

// maxReplyBytes caps one stored reply (the outbox is a bounded log).
const maxReplyBytes = 64 * 1024

var replyMu sync.Mutex

// RecordReply stores a session's own answer to a web message in its outbox, where
// the web conversation shows it as the session's reply (gofer-6er0). source is a
// jobstore.SessionReplySource*; peer is the address it was sent to. A session-
// sourced reply must come from the session's owner (same rule as a heartbeat).
// An identical text recorded in the last replyDedupeWindow is the same reply:
// nothing new is stored (a session-sourced report upgrades a messenger-sourced
// one). created reports whether a new record was written.
func (s *Service) RecordReply(sid, text, peer, source, caller string) (m jobstore.SessionMessage, created bool, err error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return jobstore.SessionMessage{}, false, fmt.Errorf("%w: reply text required", ErrInvalidInput)
	}
	switch source {
	case jobstore.SessionReplySourceSession:
		if strings.TrimSpace(caller) == "" {
			if _, ok, gerr := s.store.GetAgentSession(sid); gerr != nil {
				return jobstore.SessionMessage{}, false, gerr
			} else if !ok {
				return jobstore.SessionMessage{}, false, ErrUnknownSession
			}
			return jobstore.SessionMessage{}, false, ErrSessionHeartbeatOwner
		}
		a, claimed, cerr := s.store.ClaimAgentSessionOwner(sid, caller)
		if cerr != nil {
			return jobstore.SessionMessage{}, false, cerr
		}
		if a.SessionID == "" {
			return jobstore.SessionMessage{}, false, ErrUnknownSession
		}
		if !claimed {
			return jobstore.SessionMessage{}, false, ErrSessionHeartbeatOwner
		}
	case jobstore.SessionReplySourceMessenger:
		if _, err := s.Session(sid); err != nil {
			return jobstore.SessionMessage{}, false, err
		}
	default:
		return jobstore.SessionMessage{}, false, fmt.Errorf("%w: unknown reply source %q", ErrInvalidInput, source)
	}
	text = capReply(text)
	replyMu.Lock()
	defer replyMu.Unlock()
	list, err := s.store.ReadSessionOutbox(sid)
	if err != nil {
		return jobstore.SessionMessage{}, false, err
	}
	now := s.now()
	replyTo := ""
	for i := len(list) - 1; i >= 0; i-- {
		prev := list[i]
		if prev.Direction == jobstore.SessionMessageReply {
			if prev.Text == text && now.Sub(time.Unix(prev.CreatedAt, 0)) <= replyDedupeWindow {
				if source == jobstore.SessionReplySourceSession && prev.Source != source {
					prev.Source, prev.Peer, prev.UpdatedAt = source, orPeer(peer, prev.Peer), now.Unix()
					if err := s.store.AppendSessionOutbox(prev); err != nil {
						return jobstore.SessionMessage{}, false, err
					}
				}
				return prev, false, nil
			}
			continue
		}
		if replyTo == "" && prev.Status == jobstore.SessionMessageDelivered {
			replyTo = prev.ID
		}
	}
	m = jobstore.SessionMessage{ID: fmt.Sprintf("reply-%d", now.UnixNano()), SessionID: sid, Text: text,
		Status: jobstore.SessionMessageDelivered, Channel: "send_message", Direction: jobstore.SessionMessageReply,
		Source: source, Peer: strings.TrimSpace(peer), ReplyTo: replyTo, CreatedAt: now.Unix(), UpdatedAt: now.Unix()}
	if err := s.store.AppendSessionOutbox(m); err != nil {
		return jobstore.SessionMessage{}, false, err
	}
	return m, true, nil
}

// RecordMessengerReply is the fallback path: a resident messenger on runner
// received a message from the Claude session named peerName. It is stored as
// that session's reply when exactly one live gofer session on that runner has
// that name (a name is only unique among the sessions alive at one time).
func (s *Service) RecordMessengerReply(runner, peerName, from, body string) (jobstore.SessionMessage, bool, error) {
	peerName = strings.TrimSpace(peerName)
	if peerName == "" || strings.TrimSpace(body) == "" {
		return jobstore.SessionMessage{}, false, fmt.Errorf("%w: peer name and body required", ErrInvalidInput)
	}
	list, err := s.store.ListAgentSessions(jobstore.ListSessionsOpts{})
	if err != nil {
		return jobstore.SessionMessage{}, false, err
	}
	var match []jobstore.AgentSession
	for _, a := range list {
		if a.PeerName == peerName && s.sameRunner(a.Runner, runner) {
			match = append(match, a)
		}
	}
	if len(match) != 1 {
		return jobstore.SessionMessage{}, false, fmt.Errorf("%w: %d live sessions named %q on runner %q", ErrInvalidInput, len(match), peerName, runner)
	}
	return s.RecordReply(match[0].SessionID, body, from, jobstore.SessionReplySourceMessenger, "")
}

func (s *Service) sameRunner(label, runner string) bool {
	if s.IsServerLocalRunner(label) && s.IsServerLocalRunner(runner) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(label), strings.TrimSpace(runner))
}

func (s *Service) now() time.Time {
	if s.nowFn != nil {
		return s.nowFn()
	}
	return time.Now()
}

func capReply(text string) string {
	if len(text) <= maxReplyBytes {
		return text
	}
	const suffix = "\n[已截断]"
	limit := maxReplyBytes - len(suffix)
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit] + suffix
}

func orPeer(peer, prev string) string {
	if strings.TrimSpace(peer) != "" {
		return strings.TrimSpace(peer)
	}
	return prev
}

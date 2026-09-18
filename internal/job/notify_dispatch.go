package job

import (
	"log/slog"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/notify"
)

// Notification event types that are NOT job lifecycle events (OBS-07a). They
// never appear in DefaultTriggerEvents, so an existing webhook that omits its
// `events` filter keeps its current traffic: subscribing is opt-in by listing
// the type explicitly.
const (
	// EventSessionWaiting fires when a relayed terminal session stops and opens a
	// turn waiting for a human reply (session relay, SESS-01).
	EventSessionWaiting = "session.waiting"
	// EventSessionAttention fires when a relayed session needs a human at the
	// TERMINAL — a permission prompt or a built-in question the agent raised.
	// Unlike session.waiting this cannot be answered on the web (it is Claude
	// Code's own dialog, not a relay turn), so the notification exists to get the
	// human back to the keyboard, not to hand them a reply box.
	EventSessionAttention = "session.attention"
	// EventSessionHandedOff fires when a session is taken over by a new interactive
	// pty job (session relay §9.1 B, `--resume`): the conversation continues in that
	// job's terminal, so the notification carries the link to it. Like the other two
	// session events it is NOT a default trigger — a webhook subscribes explicitly.
	EventSessionHandedOff = "session.handed_off"
	// EventSessionTakeoverReleased fires when the takeover job ends and the session
	// goes back to its ORIGINAL terminal (SUP-02 R1) — the dual of the event above:
	// whoever was told the conversation had moved learns it moved back. reason names
	// why the job ended ("job_done" / "job_failed" / …).
	EventSessionTakeoverReleased = "session.takeover_released"
)

// NotifyEvent enqueues a pre-rendered notification for every webhook subscribed
// to eventType (and to projectKey, when the webhook filters projects). Unlike
// the job-event path it needs neither a job nor a row in `events`: the body is
// rendered per webhook kind now and stored on the delivery, so the queue's
// retry/backoff/audit apply unchanged.
//
// It is best-effort and never fails the caller: notification is a side channel,
// so every problem is logged and swallowed. Returns how many deliveries were
// enqueued (0 when nothing subscribes — the common case).
func (s *Service) NotifyEvent(eventType, projectKey string, msg notify.Message) int {
	cfg := s.config()
	if cfg == nil || cfg.Server.Notification == nil || len(cfg.Server.Notification.Webhooks) == 0 {
		return 0
	}
	if projectKey != "" {
		if proj, ok := cfg.Projects[projectKey]; ok && !proj.IsNotifyEnabled() {
			return 0 // project opted out of notification
		}
	}
	targets := notify.MatchWebhooks(cfg.Server.Notification, eventType, projectKey)
	if len(targets) == 0 {
		return 0
	}
	msg.EventType = eventType
	if msg.At == 0 {
		msg.At = s.nowFn().Unix()
	}
	sink := s.deliveries
	if sink == nil {
		sink = s.meta
	}
	n := 0
	for _, w := range targets {
		body, err := notify.RenderMessage(w.Kind, msg)
		if err != nil {
			slog.Warn("NotifyEvent: render", "type", eventType, "kind", w.Kind, "err", err)
			continue
		}
		if _, err := sink.InsertDelivery(jobstore.Delivery{
			Target:      w.URL,
			Status:      jobstore.DeliveryPending,
			NextRetryAt: msg.At, // due now
			CreatedAt:   msg.At,
			Body:        string(body),
			EventType:   eventType,
		}); err != nil {
			slog.Warn("NotifyEvent: insert delivery", "type", eventType, "target", w.URL, "err", err)
			continue
		}
		n++
	}
	return n
}

// NotifySessionWaiting is the session-relay notifier (SESS-01 → OBS-07a): a
// relayed session stopped and is waiting for a human reply. It satisfies the
// sessionrelay.Notifier seam, so that package stays free of notification and
// config concerns.
func (s *Service) NotifySessionWaiting(sessionID, projectKey, title, lastMessage string, turn int64) {
	label := strings.TrimSpace(title)
	if label == "" {
		label = shortID(sessionID)
	}
	s.NotifyEvent(EventSessionWaiting, projectKey, notify.Message{
		Title:     "会话等待回复 · " + label,
		Text:      lastMessage,
		Link:      s.webURL("/sessions?sid=" + sessionID),
		LinkLabel: "打开会话回复",
	})
}

// NotifySessionAttention is raised when a relayed session enters
// needs_attention (SESS-01 → OBS-07a). The link goes to the session page so the
// human can see WHICH session is blocked, but the answer has to happen in the
// terminal — the message says so.
func (s *Service) NotifySessionAttention(sessionID, projectKey, title, detail string) {
	label := strings.TrimSpace(title)
	if label == "" {
		label = shortID(sessionID)
	}
	text := strings.TrimSpace(detail)
	if text == "" {
		text = "会话在等待确认（权限或选择），需要回到终端处理。"
	}
	s.NotifyEvent(EventSessionAttention, projectKey, notify.Message{
		Title:     "会话需要确认 · " + label,
		Text:      text,
		Link:      s.webURL("/sessions?sid=" + sessionID),
		LinkLabel: "查看会话",
	})
}

// NotifySessionHandedOff is raised when a session is taken over by a new
// interactive pty job (SESS-01 §9.1 B, the `--resume` path): the conversation
// continues in THAT job's terminal, so the link goes to the job (which attaches
// the pty), not back to the session drawer that has nothing left to answer.
func (s *Service) NotifySessionHandedOff(sessionID, projectKey, title, jobID string) {
	label := strings.TrimSpace(title)
	if label == "" {
		label = shortID(sessionID)
	}
	s.NotifyEvent(EventSessionHandedOff, projectKey, notify.Message{
		Title:     "会话已在 web 接管 · " + label,
		Text:      "已用 `--resume` 起新进程接管该会话，请在该终端继续对话。",
		Link:      s.webURL("/jobs/" + jobID + "?attach=1"),
		LinkLabel: "打开接管终端",
	})
}

// NotifySessionTakeoverReleased is the DUAL of NotifySessionHandedOff (SUP-02 R1):
// the pty job that held the session ended, so the conversation is back with the
// terminal that registered it — the web can answer it again and a human who was
// told to move to the job's terminal can come back. reason is why the job ended
// ("job_done" / "job_failed" / …), which the message states so an operator can tell
// "the work finished" from "the takeover crashed".
func (s *Service) NotifySessionTakeoverReleased(sessionID, projectKey, title, jobID, reason string) {
	label := strings.TrimSpace(title)
	if label == "" {
		label = shortID(sessionID)
	}
	s.NotifyEvent(EventSessionTakeoverReleased, projectKey, notify.Message{
		Title:     "会话已释放接管 · " + label,
		Text:      "接管该会话的 job " + shortID(jobID) + " 已结束（" + reason + "），会话回到原终端，可继续中继。",
		Link:      s.webURL("/sessions?sid=" + sessionID),
		LinkLabel: "查看会话",
	})
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

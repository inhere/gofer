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

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

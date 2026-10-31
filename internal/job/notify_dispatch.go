package job

import (
	"log/slog"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/desktop"
	"github.com/inhere/gofer/internal/i18n"
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
	// EventSessionNudgePaused fires when a session nudge (N2 §E, `gofer session nudge`)
	// paused itself after 3 consecutive delivery failures, so the person learns the
	// reminders stopped reaching the session. NOT a default trigger: a webhook subscribes
	// by listing the type in its `events`.
	EventSessionNudgePaused = "session.nudge_paused"
)

// NotifyEvent enqueues a pre-rendered notification for every webhook subscribed
// to eventType (and to projectKey, when the webhook filters projects). Unlike
// the job-event path it needs neither a job nor a row in `events`: the body is
// rendered per webhook kind now and stored on the delivery, so the queue's
// retry/backoff/audit apply unchanged.
//
// Every matching event hook (AUTO-04) gets a row too, carrying the GENERIC JSON
// rendering (the same body a generic webhook receives), and — like the job-event
// path — without the notification pause switches or the project's notify_enabled.
//
// It is best-effort and never fails the caller: notification is a side channel,
// so every problem is logged and swallowed. Returns how many deliveries were
// enqueued (0 when nothing subscribes — the common case).
func (s *Service) NotifyEvent(eventType, projectKey string, msg notify.Message) int {
	cfg := s.config()
	if cfg == nil {
		return 0
	}
	msg.EventType = eventType
	if msg.At == 0 {
		msg.At = s.nowFn().Unix()
	}
	if msg.Lang == "" {
		msg.Lang = s.notifyLang(projectKey)
	}
	s.notifyDesktop(cfg, eventType, projectKey, msg)
	return s.notifyWebhooks(cfg, eventType, projectKey, msg) + s.notifyEventHooks(cfg, eventType, projectKey, msg)
}

// notifyDesktop pops a desktop notification on the SERVER's own machine (gofer-3wce)
// when the desktop channel subscribes to eventType for projectKey. Like the webhook
// half it honours the per-project notify_enabled gate; unlike it, it needs no delivery
// row — the popup is fire-and-forget, with the dispatcher doing the coalescing.
func (s *Service) notifyDesktop(cfg *config.Config, eventType, projectKey string, msg notify.Message) {
	if !notify.MatchDesktop(cfg.Server.Notification, eventType, projectKey) {
		return
	}
	if projectKey != "" {
		if proj, ok := cfg.Projects[projectKey]; ok && !proj.IsNotifyEnabled() {
			return // project opted out of notification
		}
	}
	s.pushDesktop(desktop.Notification{
		EventType: eventType,
		Title:     msg.Title,
		Text:      msg.Text,
		Link:      msg.Link,
		At:        msg.At,
		Lang:      msg.Lang,
	})
}

// notifyDesktopJobEvent is the desktop half of a JOB event (gofer-3wce): it renders
// the same immediate message the IM adapters receive — the approval / question /
// session-reply shapes say what a person has to decide, and job.needs_review falls
// back to the job headline — and hands it to the coalescing dispatcher.
func (s *Service) notifyDesktopJobEvent(cfg *config.Config, jr JobResult, eventType, detailJSON string, at int64) {
	if !notify.MatchDesktop(cfg.Server.Notification, eventType, jr.ProjectKey) {
		return
	}
	if proj, ok := cfg.Projects[jr.ProjectKey]; ok && !proj.IsNotifyEnabled() {
		return
	}
	msg, ok := s.immediateMessage(jr, eventType, detailJSON, at)
	if !ok {
		return
	}
	msg.EventType = eventType
	if msg.At == 0 {
		msg.At = at
	}
	s.pushDesktop(desktop.Notification{
		EventType: eventType,
		Title:     msg.Title,
		Text:      msg.Text,
		Link:      msg.Link,
		At:        msg.At,
		Lang:      msg.Lang,
	})
}

// pushDesktop hands one notification to the dispatcher, which merges same-type ones
// and shows them on the desktop.
func (s *Service) pushDesktop(n desktop.Notification) {
	if d := s.desktopDispatcher(); d != nil {
		d.Push(n)
	}
}

// desktopDispatcher lazily builds the desktop dispatcher. The merge window is taken
// from the config at FIRST use and then frozen (a reload changing merge_sec takes
// effect on the next start) — the same rule the concurrency semaphores follow.
// desktopNotifier is a test seam: production uses the platform notifier.
func (s *Service) desktopDispatcher() *desktop.Dispatcher {
	s.desktopMu.Lock()
	defer s.desktopMu.Unlock()
	if s.desktop == nil {
		n := s.desktopNotifier
		if n == nil {
			n = desktop.NewSystemNotifier()
		}
		window := time.Duration(config.DefaultDesktopMergeSec) * time.Second
		if cfg := s.config(); cfg != nil && cfg.Server.Notification != nil {
			window = time.Duration(cfg.Server.Notification.Desktop.EffectiveMergeSec()) * time.Second
		}
		s.desktop = desktop.NewDispatcher(n, window)
	}
	return s.desktop
}

// closeDesktop flushes whatever the dispatcher still holds; shutdown calls it so a
// notification queued just before exit is not swallowed.
func (s *Service) closeDesktop() {
	s.desktopMu.Lock()
	d := s.desktop
	s.desktop = nil
	s.desktopMu.Unlock()
	d.Close()
}

// notifyWebhooks is NotifyEvent's webhook half: gated by the notification config and
// the project's notify_enabled, rendered per webhook kind.
func (s *Service) notifyWebhooks(cfg *config.Config, eventType, projectKey string, msg notify.Message) int {
	if !hasWebhooks(cfg) {
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
	sink := s.deliveryStore()
	n := 0
	for _, w := range targets {
		body, err := notify.RenderMessageWithLimit(w.Kind, msg, w.Keyword, w.EffectiveMaxTextRunes(cfg.Server.Notification.EffectiveMaxTextRunes()))
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

// notifyEventHooks is NotifyEvent's event-hook half (AUTO-04): one row per matching
// hook, carrying the generic JSON body that becomes the hook's stdin.
func (s *Service) notifyEventHooks(cfg *config.Config, eventType, projectKey string, msg notify.Message) int {
	hooks := notify.MatchEventHooks(cfg.Server.EventHooks, eventType, projectKey)
	if len(hooks) == 0 {
		return 0
	}
	body, err := notify.RenderMessage(notify.KindGeneric, msg, "")
	if err != nil {
		slog.Warn("NotifyEvent: render hook body", "type", eventType, "err", err)
		return 0
	}
	sink := s.deliveryStore()
	n := 0
	for _, h := range hooks {
		if _, err := sink.InsertDelivery(jobstore.Delivery{
			Target:      h.Target(),
			Status:      jobstore.DeliveryPending,
			NextRetryAt: msg.At, // due now
			CreatedAt:   msg.At,
			Body:        string(body),
			EventType:   eventType,
		}); err != nil {
			slog.Warn("NotifyEvent: insert hook delivery", "type", eventType, "hook", h.Name, "err", err)
			continue
		}
		n++
	}
	return n
}

// notifyLang is the language of a notification's own words. A notification is built in
// the background — no request carries a language — so it follows the project it
// belongs to, else the server (config.LanguageFor); projectKey "" is the server's.
func (s *Service) notifyLang(projectKey string) i18n.Lang {
	return s.config().LanguageFor(projectKey)
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
	lang := s.notifyLang(projectKey)
	s.NotifyEvent(EventSessionWaiting, projectKey, notify.Message{
		Title:     i18n.T(lang, "notify.session.waiting_title", "label", label),
		Text:      lastMessage,
		Link:      s.webURL("/sessions?sid=" + sessionID),
		LinkLabel: i18n.T(lang, "notify.session.waiting_link"),
		Lang:      lang,
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
	lang := s.notifyLang(projectKey)
	text := strings.TrimSpace(detail)
	if text == "" {
		text = i18n.T(lang, "notify.session.attention_default")
	}
	s.NotifyEvent(EventSessionAttention, projectKey, notify.Message{
		Title:     i18n.T(lang, "notify.session.attention_title", "label", label),
		Text:      text,
		Link:      s.webURL("/sessions?sid=" + sessionID),
		LinkLabel: i18n.T(lang, "notify.common.link_session"),
		Lang:      lang,
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
	lang := s.notifyLang(projectKey)
	s.NotifyEvent(EventSessionHandedOff, projectKey, notify.Message{
		Title:     i18n.T(lang, "notify.session.handed_off_title", "label", label),
		Text:      i18n.T(lang, "notify.session.handed_off_text"),
		Link:      s.webURL("/jobs/" + jobID + "?attach=1"),
		LinkLabel: i18n.T(lang, "notify.session.handed_off_link"),
		Lang:      lang,
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
	lang := s.notifyLang(projectKey)
	s.NotifyEvent(EventSessionTakeoverReleased, projectKey, notify.Message{
		Title:     i18n.T(lang, "notify.session.released_title", "label", label),
		Text:      i18n.T(lang, "notify.session.released_text", "job", shortID(jobID), "reason", reason),
		Link:      s.webURL("/sessions?sid=" + sessionID),
		LinkLabel: i18n.T(lang, "notify.common.link_session"),
		Lang:      lang,
	})
	// JOB-09: this event is part of the wakeup EVENT catalog (design §五.1), so it
	// must be in the job's durable event stream too — the matcher, the timeline and
	// GET /events all read that, while NotifyEvent above only reaches IM/webhooks.
	s.recordEvent(jobID, EventSessionTakeoverReleased, map[string]any{
		"session_id": sessionID,
		"reason":     reason,
	})
}

// NotifySessionNudgePaused tells the person a session nudge stopped itself after repeated
// delivery failures (sessionrelay.NudgeNotifier). The link opens the session drawer, where
// the nudge can be resumed once the session is reachable again.
func (s *Service) NotifySessionNudgePaused(sessionID, projectKey, title, nudgeID, reason string) {
	label := strings.TrimSpace(title)
	if label == "" {
		label = shortID(sessionID)
	}
	lang := s.notifyLang(projectKey)
	s.NotifyEvent(EventSessionNudgePaused, projectKey, notify.Message{
		Title:     i18n.T(lang, "notify.session.nudge_title", "label", label),
		Text:      i18n.T(lang, "notify.session.nudge_text", "nudge", nudgeID, "reason", reason),
		Link:      s.webURL("/sessions?sid=" + sessionID),
		LinkLabel: i18n.T(lang, "notify.common.link_session"),
		Lang:      lang,
	})
}

// shortID shortens a session or job id for a notice. A job id drops the job-
// marker first: the notice already says "job", and both the current
// job-20261021-212412-6a715d34 shape and the older prefix-less one then read as
// a timestamp.
func shortID(id string) string {
	id = strings.TrimPrefix(id, JobIDPrefix)
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// WebURL resolves a console path against server.web_base_url ("" when unset), so
// other services (the work-item reminders) can put a tappable link in a notification.
func (s *Service) WebURL(path string) string { return s.webURL(path) }

// NotifyWork is the work-item notifier (W1): work.remind and work.digest. It renders
// like every other IM notification (so max_text_runes applies) and is a no-op when no
// webhook subscribes.
// NotifyUpdateAvailable sends update.available (gofer-29gi): a newer gofer release
// was found. It is a server-wide event, so no project filter applies.
func (s *Service) NotifyUpdateAvailable(current, latest, description string) int {
	lang := s.notifyLang("")
	text := i18n.T(lang, "notify.update.text", "current", current, "latest", latest)
	if d := strings.TrimSpace(description); d != "" {
		text += "\n" + d
	}
	return s.NotifyEvent(notify.EventUpdateAvailable, "", notify.Message{
		Title: i18n.T(lang, "notify.update.title", "latest", latest), Text: text,
		Link: s.webURL("/settings/about"), LinkLabel: i18n.T(lang, "notify.update.link"), Lang: lang,
	})
}

func (s *Service) NotifyWork(eventType, projectKey, title, text, link, linkLabel string) int {
	n := s.NotifyEvent(eventType, projectKey, notify.Message{Title: title, Text: text, Link: link, LinkLabel: linkLabel})
	if n == 0 {
		// OBS-13: a work notification that reaches nobody must not be silent.
		slog.Warn("work.notify_no_subscriber", "event", "work.notify_no_subscriber", "type", eventType, "project", projectKey)
	} else {
		slog.Info("work.notify_sent", "event", "work.notify_sent", "type", eventType, "targets", n)
	}
	return n
}

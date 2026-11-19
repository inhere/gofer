package job

import (
	"net/url"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/notify"
	"github.com/inhere/gofer/internal/waitnotice"
)

// NotifyWaiting is the desktop / webhook / event-hook half of a 「等我」 notice (the Web Push
// half is webpush.Service.NotifyWaiting). The waiting-notice scan calls it once when a
// subject enters the list and once more when the reminder falls due; it is the only caller.
// The message carries a title (who waits for what) and a link, never the text of a turn.
func (s *Service) NotifyWaiting(n waitnotice.Notice) {
	lang := s.notifyLang(n.ProjectKey)
	link := ""
	if n.ThreadID != "" {
		link = s.webURL("/workbench?thread=" + url.QueryEscape(n.ThreadID))
	}
	s.NotifyEvent(EventSessionAwaitingReply, n.ProjectKey, notify.Message{
		Title:     waitnotice.Title(lang, n),
		Link:      link,
		LinkLabel: i18n.T(lang, "waitnotice.link.open"),
		Lang:      lang,
	})
}

package webpush

import (
	"log/slog"
	"net/url"

	"github.com/inhere/gofer/internal/waitnotice"
)

// kindWaiting is the queued event of a 「等我」 notice.
const kindWaiting = "waiting"

// NotifyWaiting queues the Web Push half of a 「等我」 notice (the desktop / webhook half is
// job.Service.NotifyWaiting); the waiting-notice scan calls both from one place. The push
// carries the title, the subject key as its tag (a later push about the same subject replaces
// the earlier one on the device) and a link to the subject's workbench thread; it never
// carries the text of a turn. It never blocks.
func (s *Service) NotifyWaiting(n waitnotice.Notice) {
	if !s.enqueue(queuedEvent{kind: kindWaiting, notice: n}) {
		slog.Warn("webpush waiting notice dropped", "subject", n.Key)
	}
}

// waitingPayload is the push of one notice.
func (s *Service) waitingPayload(n waitnotice.Notice) PushPayload {
	// ThreadID stays empty on purpose: the notice is sent once per stay by the scan, so the
	// 30s same-thread merge window (which would swallow it right behind a terminal push of
	// the same thread) has nothing to dedupe.
	return PushPayload{
		Title: waitnotice.Title(s.lang(n.ProjectKey), n),
		URL:   "/workbench?thread=" + url.QueryEscape(n.ThreadID),
		Tag:   n.Key,
	}
}

// dispatchWaiting pushes the notice to its owner; a notice without a known owner goes to
// every caller that sees the project, like the other pushes do.
func (s *Service) dispatchWaiting(n waitnotice.Notice) {
	payload := s.waitingPayload(n)
	seen := make(map[string]struct{})
	for _, raw := range s.userCallers() {
		callerID := normalizeCaller(raw)
		if _, dup := seen[callerID]; dup {
			continue
		}
		seen[callerID] = struct{}{}
		if n.CallerID != "" && normalizeCaller(n.CallerID) != callerID {
			continue
		}
		if !s.visible(callerID, n.ProjectKey) {
			continue
		}
		s.dispatchPayload(callerID, n.ProjectKey, payload, nil, "high")
	}
}

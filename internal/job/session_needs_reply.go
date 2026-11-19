package job

import "github.com/inhere/gofer/internal/needsreply"

// A managed ACP session takes the same needs-reply convention a terminal session gets from
// the hook relay (needsreply.Reminder): once, in front of the first turn it runs, so the
// agent knows the marker line. The runner then reads that line off the turn's final message
// (runner/acp needs_reply.go) and the verdict rides the job.awaiting_input event as
// needs_reply — a stored fact the 「等我」 list reads (today / workbench), so it survives a
// server restart.

// needsReplyConvention returns the convention text for the next turn of entry's session,
// separated from the prompt that follows; "" once it has been given.
func (s *Service) needsReplyConvention(entry *jobEntry) string {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.needsReplyTaught {
		return ""
	}
	entry.needsReplyTaught = true
	return needsreply.Reminder + "\n\n"
}

// awaitingInputDetail is the detail of the job.awaiting_input event; needs_reply appears
// only on a turn that carried the marker.
func awaitingInputDetail(snap JobResult, needsReply bool) map[string]any {
	detail := map[string]any{"turn_no": snap.TurnNo, "idle_deadline_at": snap.IdleDeadlineAt}
	if needsReply {
		detail["needs_reply"] = true
	}
	return detail
}

// AwaitingNeedsReply reports whether the session job is waiting in awaiting_input after a
// turn that carried the needs-reply marker. A worker puts it on the status frame it sends
// the hub, which records it on its own job.awaiting_input event.
func (s *Service) AwaitingNeedsReply(jobID string) bool {
	entry := s.entry(jobID)
	if entry == nil {
		return false
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return entry.result.Status == StatusAwaitingInput && entry.awaitNeedsReply
}

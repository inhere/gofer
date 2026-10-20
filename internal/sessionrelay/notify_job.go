package sessionrelay

// notify_job.go is the DELIVERY half of gofer-3db4: telling a session that a job
// it asked for has ended. It rides the existing session messenger (SendMessage),
// the same mechanism the web's "send a message" button uses, so a notice reaches
// a terminal session that is NOT currently waiting for anything — the case the
// Stop-hook wait and its catch-up cannot cover, because they only run when that
// session stops by itself.
//
// Only the caller's explicit request brings a job here: the notification is off
// by default, and every delivery costs one messenger process (sessionrelay's
// one-shot job or the resident session), so it is spent only where a human or an
// agent asked for it (see job.JobRequest.Notify).

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// JobDoneTag opens every "your job finished" notice — this package's and the Stop
// hook's catch-up line alike (hookrelay.JobDoneTag aliases it, and its tests count
// the tag). One spelling means a session recognises both as the same message.
const JobDoneTag = "[gofer job 完成]"

// OperatorGofer is the operator (the message's source, as the outbox and the
// receiving session see it) of a gofer SYSTEM notice. It is what tells a notice
// apart from a person's message ("web", a user name): web messages arrive as
// "[来自 web，<user>] …" while this one is sent as gofer itself.
const OperatorGofer = "gofer"

// JobEndedNotice is the terminal snapshot one notice is rendered from. It is the
// narrow projection the relay needs — the caller (the assembled hub, holding a
// finished job) owns the job's full state.
type JobEndedNotice struct {
	// TargetSession is the session to tell; empty is an error (a caller without a
	// target must not call this at all).
	TargetSession string
	JobID         string
	Title         string
	Status        string
	ExitCode      int
	// Duration is the job's wall-clock run time (0 when either stamp is missing).
	Duration time.Duration
	// WrapUp is the agent's OWN reason for ending early ("max_steps" / "max_tokens"
	// / "timeout"); "" drops the field (the agent answered).
	WrapUp string
}

// FormatJobEndedNotice renders the one-line notice. It starts with JobDoneTag and
// states the job's identity, its outcome and where to look next; it says out loud
// that it is a system notification, because the receiving session cannot tell a
// gofer notice from a person's message otherwise.
func FormatJobEndedNotice(n JobEndedNotice) string {
	var b strings.Builder
	b.WriteString(JobDoneTag)
	b.WriteString(" ")
	b.WriteString(strings.TrimSpace(n.JobID))
	if title := strings.TrimSpace(n.Title); title != "" {
		b.WriteString(" ")
		b.WriteString(title)
	}
	b.WriteString(" status=")
	b.WriteString(strings.TrimSpace(n.Status))
	b.WriteString(" exit=")
	b.WriteString(strconv.Itoa(n.ExitCode))
	b.WriteString(" 耗时")
	b.WriteString(formatNoticeDuration(n.Duration))
	if wrap := strings.TrimSpace(n.WrapUp); wrap != "" {
		b.WriteString(" wrap_up=")
		b.WriteString(wrap)
	}
	b.WriteString("（这是 gofer 系统通知，不是用户消息；详情 `gofer job show ")
	b.WriteString(strings.TrimSpace(n.JobID))
	b.WriteString("`，日志 `gofer job logs ")
	b.WriteString(strings.TrimSpace(n.JobID))
	b.WriteString("`）")
	return b.String()
}

// formatNoticeDuration renders a run time compactly ("0s" / "45s" / "12m30s").
func formatNoticeDuration(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	return d.Truncate(time.Second).String()
}

// NotifyJobEnded tells a session that a job ended, through the messenger.
//
// Delivery is at-most-once per (session, job) and survives a server restart,
// because the durable record is the session's job WATCH — the row the Stop hook
// and its catch-up read:
//
//   - the watch is claimed by deleting it (Submit registered it when the
//     notification was requested); a claim that removes nothing means somebody
//     else already delivered this notice — the hook, an earlier ended-hook, or the
//     same event before a restart — and nothing is sent;
//   - the message is sent only after the claim, so a duplicate terminal event
//     (a needs_review job later accepted) can never send a second notice even
//     though the ended-hook runs again;
//   - a FAILED delivery puts the watch back, so the existing Stop-hook /
//     catch-up path still delivers it the next time that session stops. Losing the
//     notice silently is the one outcome worth avoiding.
//
// Every outcome is logged, never returned to the job: a notification is a side
// channel and must not influence how the job ended.
func (s *Service) NotifyJobEnded(ctx context.Context, n JobEndedNotice) error {
	target := strings.TrimSpace(n.TargetSession)
	jobID := strings.TrimSpace(n.JobID)
	if target == "" || jobID == "" {
		return fmt.Errorf("%w: job end notification needs a target session and a job id", ErrInvalidInput)
	}
	claimed, err := s.store.RemoveSessionJobWatch(target, jobID)
	if err != nil {
		return fmt.Errorf("claim job end notification: %w", err)
	}
	if !claimed {
		// Already delivered (or removed) — see the at-most-once note above.
		return nil
	}
	text := FormatJobEndedNotice(n)
	// The notice is gofer's own message (by = OperatorGofer), but the delivery belongs to the
	// session's OWNER: a relay job gofer dispatches for it is bounded by that caller's ceiling.
	if _, err := s.SendMessage(ctx, target, text, OperatorGofer, s.sessionOwner(target)); err != nil {
		if _, rerr := s.store.AddSessionJobWatch(target, jobID); rerr != nil {
			slog.Warn("sessionrelay: restore watch after failed job end notification",
				"session_id", target, "job_id", jobID, "err", rerr)
		}
		return fmt.Errorf("send job end notification: %w", err)
	}
	slog.Info("sessionrelay: job end notification delivered",
		"event", "sessionrelay.job_end_notified", "session_id", target, "job_id", jobID,
		"status", strings.TrimSpace(n.Status))
	return nil
}

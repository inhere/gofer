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

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/msgprefix"
)

// JobDoneTag opens every "your job finished" notice — this package's and the Stop
// hook's catch-up line alike (hookrelay.JobDoneTag aliases it, and its tests count
// the tag). One spelling means a session recognises both as the same message.
//
// It is the message's whole prefix (msgprefix.JobDone), not a second bracket behind
// another one: the notice used to arrive as "[gofer 通知] [gofer job 完成] …" and
// the doubled bracket was half of what made system text unreadable at a glance.
const JobDoneTag = msgprefix.JobDoneOpen

// OperatorGofer is the operator (the message's source, as the outbox and the
// receiving session see it) of a gofer SYSTEM notice. It is what tells a notice
// apart from a person's message ("web", a user name): a person's message arrives
// prefixed "[来自 web，<user>] …", a notice as gofer itself ("[gofer · <type>] …").
const OperatorGofer = "gofer"

// IsSystemNoticeOperator reports whether an operator label means "gofer is
// speaking" rather than a person: the message then goes out as a system notice
// (msgprefix's system prefixes) and never as a web message.
func IsSystemNoticeOperator(operator string) bool {
	return strings.TrimSpace(operator) == OperatorGofer
}

// SystemNoticeLine renders the delivered line of a gofer notice: a text that
// already opens with a system prefix is passed through as is; one that carries
// none (an older caller) gets the generic hint prefix, because a message labeled
// as gofer's must never reach a session looking like a person's.
func SystemNoticeLine(text string) string {
	t := strings.TrimSpace(text)
	if msgprefix.IsSystem(t) {
		return t
	}
	return msgprefix.Format(msgprefix.Hint, "", t)
}

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
	// Runner is the machine the job ran on, already a display name
	// (config.RunnerDisplayName); "" drops the field (gofer-okz3 ④).
	Runner string
}

// FormatJobEndedNotice renders the one-line notice IN LANG. It opens with the
// job-done prefix (never a person's "[来自 web，…]", which means somebody typed it),
// and states the job's identity, its outcome and where to look next; it says out loud
// that it is a system notification, because the receiving session's rules tell it
// that a prefixed message of this shape is gofer speaking.
func FormatJobEndedNotice(lang i18n.Lang, n JobEndedNotice) string {
	var b strings.Builder
	b.WriteString(msgprefix.PrefixIn(lang, msgprefix.JobDone, ""))
	b.WriteString(strings.TrimSpace(n.JobID))
	if title := strings.TrimSpace(n.Title); title != "" {
		b.WriteString(" ")
		b.WriteString(title)
	}
	b.WriteString(" status=")
	b.WriteString(strings.TrimSpace(n.Status))
	b.WriteString(" exit=")
	b.WriteString(strconv.Itoa(n.ExitCode))
	if runner := strings.TrimSpace(n.Runner); runner != "" {
		b.WriteString(" runner=")
		b.WriteString(runner)
	}
	b.WriteString(" ")
	b.WriteString(i18n.T(lang, "sessionrelay.notice.duration", "duration", formatNoticeDuration(n.Duration)))
	if wrap := strings.TrimSpace(n.WrapUp); wrap != "" {
		b.WriteString(" wrap_up=")
		b.WriteString(wrap)
	}
	b.WriteString(i18n.T(lang, "sessionrelay.notice.tail", "id", strings.TrimSpace(n.JobID)))
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
// One case is decided BEFORE anything is claimed (gofer-fv6a): a session whose
// Stop hook is PARKED on its watched jobs (announced as StopParkUntil) is told by
// that hook, not by the messenger. The hook polls the watches; a notice claimed
// here would be handed to the messenger, whose peer message cannot reach an agent
// blocked in its own Stop hook — it was read at the next human prompt (~25 minutes
// late in the reported case) while the watch that would have woken it was gone.
// Leaving the watch in place keeps the at-most-once rule untouched: the hook claims
// it (CompleteSessionWatches) exactly when it hands the notice over.
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
	lang := i18n.Default
	if a, ok, err := s.store.GetAgentSession(target); err != nil {
		return fmt.Errorf("read notification target: %w", err)
	} else if ok {
		if StopHookParked(a, s.now().Unix()) {
			slog.Info("sessionrelay: job end notice left to the parked Stop hook",
				"event", "sessionrelay.job_end_deferred_to_hook", "session_id", target, "job_id", jobID,
				"parked_until", a.StopParkUntil)
			return nil
		}
		lang = s.lang(a.ProjectKey)
	}
	claimed, err := s.store.RemoveSessionJobWatch(target, jobID)
	if err != nil {
		return fmt.Errorf("claim job end notification: %w", err)
	}
	if !claimed {
		// Already delivered (or removed) — see the at-most-once note above.
		return nil
	}
	text := FormatJobEndedNotice(lang, n)
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
